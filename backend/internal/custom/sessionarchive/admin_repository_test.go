package sessionarchive

import (
	"context"
	"database/sql"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

var contentRecordColumns = []string{
	"id", "owner_type", "owner_id", "purpose", "direction", "content_type",
	"observed_sha256", "observed_bytes", "stored_bytes", "truncated", "dropped_reason",
	"sequence_no", "occurred_at", "blob_id", "storage_backend", "stored_plaintext_sha256", "blob_stored_bytes",
	"compressed_bytes", "ciphertext_bytes", "gzip_version", "format_version", "key_id", "object_key",
}

func TestAcquireRequestReadLeaseHoldsTransactionUntilExplicitRelease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.id .*r.id=\\$1.*s.status<>'deleting' FOR SHARE OF s").
		WithArgs(int64(91)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectRollback()

	lease, err := repository.AcquireRequestReadLease(context.Background(), 91)
	require.NoError(t, err)
	require.NotNil(t, lease)
	lease.Release()
	lease.Release()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAcquireSessionReadLeaseRejectsDeletingSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM session_archive_sessions WHERE id=\\$1 AND status<>'deleting' FOR SHARE").
		WithArgs(int64(92)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	lease, err := repository.AcquireSessionReadLease(context.Background(), 92)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.Nil(t, lease)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRequestContentsLocksActiveSessionAndReturnsEveryOrderedRef(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	firstAt := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	secondAt := firstAt.Add(time.Second)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.id .*s.status<>'deleting' FOR SHARE OF s").WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))
	mock.ExpectQuery("SELECT br.id.*br.owner_type='request'.*ORDER BY br.occurred_at,br.sequence_no,br.id").
		WithArgs(int64(11), PurposeResponse).
		WillReturnRows(sqlmock.NewRows(contentRecordColumns).
			AddRow(31, "request", 11, PurposeResponse, "gateway_to_client", "application/json", "a", 5, 5, false, "", 1, firstAt, 41, "s3", "hash-1", 5, 4, 20, 1, 1, "key-1", "archive/1").
			AddRow(32, "request", 11, PurposeResponse, "gateway_to_client", "application/json", "b", 7, 7, false, "", 2, secondAt, 42, "s3", "hash-2", 7, 6, 22, 1, 1, "key-1", "archive/2"))
	mock.ExpectCommit()

	records, err := repository.RequestContents(context.Background(), 11, "response")

	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, []int64{1, 2}, []int64{records[0].Ref.SequenceNo, records[1].Ref.SequenceNo})
	require.Equal(t, firstAt, records[0].Ref.OccurredAt)
	require.Equal(t, "gateway_to_client", records[1].Ref.Direction)
	require.True(t, records[0].Ref.Available)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRequestContentsReadsAttemptUpstreamRefs(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	occurredAt := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.id .*s.status<>'deleting' FOR SHARE OF s").WithArgs(int64(12)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))
	mock.ExpectQuery("SELECT br.id.*JOIN session_archive_attempts.*a.request_id=\\$1.*ORDER BY br.occurred_at,a.attempt_no,br.sequence_no,br.id").
		WithArgs(int64(12), PurposeUpstreamRequest).
		WillReturnRows(sqlmock.NewRows(contentRecordColumns).
			AddRow(33, "attempt", 21, PurposeUpstreamRequest, "gateway_to_upstream", "application/json", "c", 9, 9, false, "", 1, occurredAt, 43, "s3", "hash-3", 9, 8, 24, 1, 1, "key-1", "archive/3"))
	mock.ExpectCommit()

	records, err := repository.RequestContents(context.Background(), 12, "upstream")

	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "attempt", records[0].Ref.OwnerType)
	require.Equal(t, PurposeUpstreamRequest, records[0].Ref.Purpose)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRequestContentsReadsInlineAttachments(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	occurredAt := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.id .*s.status<>'deleting' FOR SHARE OF s").WithArgs(int64(14)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))
	mock.ExpectQuery("SELECT br.id.*br.owner_type='request'.*ORDER BY br.occurred_at,br.sequence_no,br.id").
		WithArgs(int64(14), PurposeAttachment).
		WillReturnRows(sqlmock.NewRows(contentRecordColumns).
			AddRow(34, "request", 14, PurposeAttachment, "client_to_gateway", "image/png", "d", 4, 4, false, "", 1, occurredAt, 44, "s3", "hash-4", 4, 4, 20, 1, 1, "key-1", "archive/4"))
	mock.ExpectCommit()

	records, err := repository.RequestContents(context.Background(), 14, "attachment")

	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, PurposeAttachment, records[0].Ref.Purpose)
	require.Equal(t, "image/png", records[0].Ref.ContentType)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRequestContentsRejectsDeletingSessionBeforeBlobLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.id .*s.status<>'deleting' FOR SHARE OF s").WithArgs(int64(13)).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	_, err = repository.RequestContents(context.Background(), 13, "response")

	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}
