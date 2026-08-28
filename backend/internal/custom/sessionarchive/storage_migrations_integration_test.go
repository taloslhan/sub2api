//go:build integration

package sessionarchive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStorageBackendMigrationsStageLegacyAndFinalConstraints(t *testing.T) {
	ctx := context.Background()
	conn, err := sessionArchiveIntegrationDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	schema := fmt.Sprintf("session_archive_migration_%d", time.Now().UnixNano())
	require.Regexp(t, regexp.MustCompile(`^[a-z0-9_]+$`), schema)
	_, err = conn.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SET search_path TO public")
		_, _ = conn.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	}()
	_, err = conn.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `
		CREATE TABLE session_archive_blobs (
			id BIGSERIAL PRIMARY KEY,
			stored_plaintext_sha256 CHAR(64) NOT NULL,
			stored_bytes BIGINT NOT NULL,
			compressed_bytes BIGINT NOT NULL DEFAULT 0,
			ciphertext_bytes BIGINT NOT NULL DEFAULT 0,
			gzip_version SMALLINT NOT NULL DEFAULT 1,
			format_version SMALLINT NOT NULL DEFAULT 1,
			key_id VARCHAR(128) NOT NULL,
			object_key TEXT NOT NULL,
			status VARCHAR(32) NOT NULL DEFAULT 'pending',
			owner_token VARCHAR(64) NOT NULL DEFAULT '',
			lease_expires_at TIMESTAMPTZ,
			gc_after TIMESTAMPTZ,
			retry_count INT NOT NULL DEFAULT 0,
			last_error VARCHAR(512) NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (stored_plaintext_sha256,format_version,key_id),
			UNIQUE (object_key)
		);
		CREATE TABLE session_archive_deletion_jobs (
			id BIGSERIAL PRIMARY KEY,
			status VARCHAR(32) NOT NULL DEFAULT 'pending'
		);`)
	require.NoError(t, err)

	compat, err := os.ReadFile("../../../migrations/236_session_archive_storage_backends_compat.sql")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, string(compat))
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, string(compat))
	require.NoError(t, err)

	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oldReserveSQL := "INSERT INTO session_archive_blobs (stored_plaintext_sha256,stored_bytes,compressed_bytes,ciphertext_bytes,gzip_version,format_version,key_id,object_key,status,owner_token,lease_expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,NOW()+$10::interval) ON CONFLICT (stored_plaintext_sha256,format_version,key_id) DO NOTHING RETURNING id,object_key,status"
	var id int64
	var objectKey, status string
	err = conn.QueryRowContext(ctx, oldReserveSQL, hash, 1, 1, 1, 1, 1, "v1", "archive/v1/a.sar", "owner", "1 minute").Scan(&id, &objectKey, &status)
	require.NoError(t, err)
	err = conn.QueryRowContext(ctx, oldReserveSQL, hash, 1, 1, 1, 1, 1, "v1", "archive/v1/a.sar", "owner", "1 minute").Scan(&id, &objectKey, &status)
	require.ErrorIs(t, err, sql.ErrNoRows, "236 must retain the legacy ON CONFLICT arbiter")
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT id,object_key,status FROM session_archive_blobs WHERE stored_plaintext_sha256=$1 AND format_version=$2 AND key_id=$3", hash, 1, "v1").Scan(&id, &objectKey, &status))
	var backend string
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT storage_backend FROM session_archive_blobs WHERE id=$1", id).Scan(&backend))
	require.Equal(t, StorageBackendS3, backend)
	var uniqueConstraints int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_constraint WHERE conrelid='session_archive_blobs'::regclass AND contype='u'").Scan(&uniqueConstraints))
	require.Equal(t, 4, uniqueConstraints, "236 must retain two legacy and add two backend-aware constraints")
	var namedConstraints int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_constraint WHERE conrelid='session_archive_blobs'::regclass AND conname IN ('uq_session_archive_blobs_backend_cas','uq_session_archive_blobs_backend_object_key')").Scan(&namedConstraints))
	require.Equal(t, 2, namedConstraints)
	_, err = conn.ExecContext(ctx, "INSERT INTO session_archive_blobs (storage_backend,stored_plaintext_sha256,stored_bytes,format_version,key_id,object_key) VALUES ('invalid',$1,1,1,'v1','invalid/v1/a.sar')", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	require.Error(t, err, "236 must reject unknown storage backends")
	_, err = conn.ExecContext(ctx, "INSERT INTO session_archive_pg_objects (object_key,total_bytes,chunk_count) VALUES ('cas/v1/fk.sar',1,1)")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "INSERT INTO session_archive_pg_object_chunks (object_key,sequence_no,data) VALUES ('cas/v1/fk.sar',0,$1)", []byte{1})
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "DELETE FROM session_archive_pg_objects WHERE object_key='cas/v1/fk.sar'")
	require.NoError(t, err)
	var chunks int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_archive_pg_object_chunks WHERE object_key='cas/v1/fk.sar'").Scan(&chunks))
	require.Zero(t, chunks, "PostgreSQL object deletion must cascade to chunks")

	finalize, err := os.ReadFile("../../../migrations/237_session_archive_storage_backends_finalize.sql")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, string(finalize))
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, string(finalize))
	require.NoError(t, err)
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_constraint WHERE conrelid='session_archive_blobs'::regclass AND contype='u'").Scan(&uniqueConstraints))
	require.Equal(t, 2, uniqueConstraints)

	_, err = conn.ExecContext(ctx, "INSERT INTO session_archive_blobs (storage_backend,stored_plaintext_sha256,stored_bytes,format_version,key_id,object_key) VALUES ('filesystem',$1,1,1,'v1','cas/v1/a.sar'),('postgresql',$1,1,1,'v1','cas/v1/a.sar')", hash)
	require.NoError(t, err, "237 must allow the same CAS identity and object key across backends")
	_, err = conn.ExecContext(ctx, "INSERT INTO session_archive_blobs (storage_backend,stored_plaintext_sha256,stored_bytes,format_version,key_id,object_key) VALUES ('filesystem',$1,1,1,'v1','cas/v1/duplicate.sar')", hash)
	require.Error(t, err, "same-backend CAS identity must remain unique")
}
