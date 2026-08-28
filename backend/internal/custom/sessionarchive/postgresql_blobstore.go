package sessionarchive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
)

type PostgreSQLBlobStore struct {
	db        *sql.DB
	chunkSize int
}

func NewPostgreSQLBlobStore(db *sql.DB, chunkSize int) (*PostgreSQLBlobStore, error) {
	if db == nil {
		return nil, errors.New("postgresql archive store requires database")
	}
	if chunkSize < 64*1024 || chunkSize > 8*1024*1024 {
		return nil, errors.New("invalid postgresql archive chunk size")
	}
	return &PostgreSQLBlobStore{db: db, chunkSize: chunkSize}, nil
}

func (s *PostgreSQLBlobStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if err := validatePortableBlobKey(key); err != nil {
		return err
	}
	if body == nil || size < 0 {
		return errors.New("invalid blob body or size")
	}
	chunkCount64 := int64(0)
	if size > 0 {
		chunkCount64 = (size + int64(s.chunkSize) - 1) / int64(s.chunkSize)
	}
	if chunkCount64 > int64(^uint(0)>>1) {
		return errors.New("postgresql archive object has too many chunks")
	}
	chunkCount := int(chunkCount64)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var inserted string
	err = tx.QueryRowContext(ctx, "INSERT INTO session_archive_pg_objects (object_key,total_bytes,chunk_count) VALUES ($1,$2,$3) ON CONFLICT (object_key) DO NOTHING RETURNING object_key", key, size, chunkCount).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		if err := validatePostgreSQLObjectTx(ctx, tx, key, size, chunkCount); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return fmt.Errorf("reserve postgresql archive object: %w", err)
	}

	buffer := make([]byte, s.chunkSize)
	contextBody := &contextReader{ctx: ctx, reader: body}
	remaining := size
	for sequence := 0; sequence < chunkCount; sequence++ {
		want := int64(s.chunkSize)
		if remaining < want {
			want = remaining
		}
		chunk := buffer[:int(want)]
		if _, err := io.ReadFull(contextBody, chunk); err != nil {
			return fmt.Errorf("read postgresql archive chunk %d: %w", sequence, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO session_archive_pg_object_chunks (object_key,sequence_no,data) VALUES ($1,$2,$3)", key, sequence, chunk); err != nil {
			return fmt.Errorf("write postgresql archive chunk %d: %w", sequence, err)
		}
		remaining -= want
	}
	var extra [1]byte
	if n, readErr := contextBody.Read(extra[:]); n != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		if readErr == nil {
			readErr = errors.New("blob body exceeds declared size")
		}
		return fmt.Errorf("validate postgresql archive object size: %w", readErr)
	}
	return tx.Commit()
}

func validatePostgreSQLObjectTx(ctx context.Context, tx *sql.Tx, key string, size int64, chunks int) error {
	var storedSize, dataSize int64
	var storedChunks, actualChunks int
	var minSequence, maxSequence sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT total_bytes,chunk_count FROM session_archive_pg_objects WHERE object_key=$1 FOR SHARE", key).Scan(&storedSize, &storedChunks)
	if err == nil {
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(octet_length(data)),0),MIN(sequence_no),MAX(sequence_no) FROM session_archive_pg_object_chunks WHERE object_key=$1", key).Scan(&actualChunks, &dataSize, &minSequence, &maxSequence)
	}
	if err != nil {
		return fmt.Errorf("validate existing postgresql archive object: %w", err)
	}
	continuous := chunks == 0 && !minSequence.Valid && !maxSequence.Valid
	if chunks > 0 {
		continuous = minSequence.Valid && maxSequence.Valid && minSequence.Int64 == 0 && maxSequence.Int64 == int64(chunks-1)
	}
	if storedSize != size || dataSize != size || storedChunks != chunks || actualChunks != chunks || !continuous {
		return errors.New("postgresql archive object conflicts with existing or incomplete object")
	}
	return nil
}

func (s *PostgreSQLBlobStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := validatePortableBlobKey(key); err != nil {
		return nil, err
	}
	// FOR SHARE keeps Delete from cascading chunks away while the caller streams
	// the object. PostgreSQL rejects locking clauses in read-only transactions,
	// so this intentionally uses a regular transaction even though Get only reads.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	var totalBytes int64
	var chunkCount int
	if err := tx.QueryRowContext(ctx, "SELECT total_bytes,chunk_count FROM session_archive_pg_objects WHERE object_key=$1 FOR SHARE", key).Scan(&totalBytes, &chunkCount); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("open postgresql archive object: %w", err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT sequence_no,data FROM session_archive_pg_object_chunks WHERE object_key=$1 ORDER BY sequence_no", key)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return &postgresqlBlobReader{ctx: ctx, tx: tx, rows: rows, totalBytes: totalBytes, chunkCount: chunkCount}, nil
}

type postgresqlBlobReader struct {
	ctx        context.Context
	tx         *sql.Tx
	rows       *sql.Rows
	totalBytes int64
	chunkCount int
	nextChunk  int
	readBytes  int64
	buffer     []byte
	offset     int
	closed     bool
}

func (r *postgresqlBlobReader) Read(dst []byte) (int, error) {
	if r.closed {
		return 0, fsClosedError("postgresql archive reader")
	}
	if len(dst) == 0 {
		return 0, nil
	}
	if err := r.ctx.Err(); err != nil {
		_ = r.Close()
		return 0, err
	}
	for r.offset >= len(r.buffer) {
		r.buffer, r.offset = nil, 0
		if !r.rows.Next() {
			err := r.rows.Err()
			if err == nil && (r.nextChunk != r.chunkCount || r.readBytes != r.totalBytes) {
				err = errors.New("postgresql archive object is incomplete")
			}
			_ = r.Close()
			if err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		var sequence int
		if err := r.rows.Scan(&sequence, &r.buffer); err != nil {
			_ = r.Close()
			return 0, err
		}
		if sequence != r.nextChunk || len(r.buffer) > 8*1024*1024 {
			_ = r.Close()
			return 0, errors.New("postgresql archive chunk sequence or size is invalid")
		}
		r.nextChunk++
		r.readBytes += int64(len(r.buffer))
	}
	n := copy(dst, r.buffer[r.offset:])
	r.offset += n
	return n, nil
}

func (r *postgresqlBlobReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	rowsErr := r.rows.Close()
	txErr := r.tx.Rollback()
	if rowsErr != nil {
		return rowsErr
	}
	if txErr != nil && !errors.Is(txErr, sql.ErrTxDone) {
		return txErr
	}
	return nil
}

type fsClosedError string

func (e fsClosedError) Error() string { return string(e) + " is closed" }

func (s *PostgreSQLBlobStore) Delete(ctx context.Context, key string) error {
	if err := validatePortableBlobKey(key); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM session_archive_pg_objects WHERE object_key=$1", key)
	return err
}

func (s *PostgreSQLBlobStore) List(ctx context.Context, prefix, cursor string, limit int32) (BlobListPage, error) {
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), "/")
	if prefix != "" {
		if err := validatePortableBlobKey(prefix); err != nil {
			return BlobListPage{}, err
		}
	}
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	last, err := decodeKeysetCursor(cursor)
	if err != nil {
		return BlobListPage{}, err
	}
	likePrefix := "%"
	if prefix != "" {
		likePrefix = escapeLike(prefix) + "/%"
	}
	rows, err := s.db.QueryContext(ctx, "SELECT object_key FROM session_archive_pg_objects WHERE object_key LIKE $1 ESCAPE '\\' AND object_key>$2 ORDER BY object_key LIMIT $3", likePrefix, last, limit+1)
	if err != nil {
		return BlobListPage{}, fmt.Errorf("list postgresql archive objects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	page := BlobListPage{Keys: make([]string, 0, limit)}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return BlobListPage{}, err
		}
		if len(page.Keys) == int(limit) {
			page.NextCursor = encodeKeysetCursor(page.Keys[len(page.Keys)-1])
			break
		}
		page.Keys = append(page.Keys, key)
	}
	return page, rows.Err()
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "%", "\\%")
	return strings.ReplaceAll(value, "_", "\\_")
}

func (s *PostgreSQLBlobStore) SelfCheck(ctx context.Context) error {
	return selfCheckBlobStore(ctx, s, "self-check")
}
