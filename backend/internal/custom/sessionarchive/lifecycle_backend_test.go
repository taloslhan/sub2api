package sessionarchive

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestDeleteSessionsChecksBackendBeforeAnyMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT b.storage_backend.*WHERE NOT .*storage_backend=ANY`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"storage_backend"}).AddRow(StorageBackendS3))
	mock.ExpectRollback()

	deleted, released, err := deleteSessionsTx(context.Background(), tx, []int64{91}, time.Minute, []string{StorageBackendFilesystem})

	require.ErrorIs(t, err, ErrStorageBackendUnavailable)
	require.Zero(t, deleted)
	require.Zero(t, released)
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet(), "backend gate must run before fences, refs, sessions, or blobs are mutated")
}

func TestDeleteExpiredSessionsFiltersBlockedBackendsBeforeLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock`).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery(`SELECT s.id.*NOT EXISTS .*storage_backend=ANY.*ORDER BY s.expires_at,s.id FOR UPDATE SKIP LOCKED LIMIT \$1`).
		WithArgs(1, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectCommit()

	deleted, err := repository.DeleteExpiredSessions(context.Background(), 1, time.Minute, []string{StorageBackendFilesystem})

	require.NoError(t, err)
	require.Zero(t, deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeletionJobDefersBlockedBatchWithoutBlockingNextDueJob(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	blockedPayload, err := json.Marshal(deletionTarget{SessionIDs: []int64{41}})
	require.NoError(t, err)
	emptyPayload, err := json.Marshal(deletionTarget{})
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id,normalized_filter,processed_count.*next_retry_at<=NOW\(\).*FOR UPDATE SKIP LOCKED LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "normalized_filter", "processed_count"}).AddRow(7, blockedPayload, 0))
	mock.ExpectExec(`UPDATE session_archive_deletion_jobs SET status='running'`).
		WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SAVEPOINT session_archive_delete_batch`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT b.storage_backend.*WHERE NOT .*storage_backend=ANY`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"storage_backend"}).AddRow(StorageBackendS3))
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT session_archive_delete_batch`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE session_archive_deletion_jobs SET retry_count=retry_count\+1.*next_retry_at=NOW\(\)\+INTERVAL '1 minute'`).
		WithArgs(int64(7), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processed, err := repository.ProcessDeletionJob(context.Background(), 10, time.Minute, []string{StorageBackendFilesystem})
	require.NoError(t, err)
	require.True(t, processed)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id,normalized_filter,processed_count.*next_retry_at<=NOW\(\).*FOR UPDATE SKIP LOCKED LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "normalized_filter", "processed_count"}).AddRow(8, emptyPayload, 0))
	mock.ExpectExec(`UPDATE session_archive_deletion_jobs SET status='completed'`).
		WithArgs(int64(8)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	processed, err = repository.ProcessDeletionJob(context.Background(), 10, time.Minute, []string{StorageBackendFilesystem})
	require.NoError(t, err)
	require.True(t, processed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimGCBlobsFiltersUnavailableBackendBeforeLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	mock.ExpectQuery(`UPDATE session_archive_blobs SET status='deleting'.*storage_backend=ANY\(\$2\).*ORDER BY b.id FOR UPDATE SKIP LOCKED LIMIT \$1`).
		WithArgs(1, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "storage_backend", "object_key"}).AddRow(5, StorageBackendFilesystem, "cas/v1/key.sar"))

	blobs, err := repository.ClaimGCBlobs(context.Background(), 1, []string{StorageBackendFilesystem})

	require.NoError(t, err)
	require.Equal(t, []GCBlob{{ID: 5, StorageBackend: StorageBackendFilesystem, ObjectKey: "cas/v1/key.sar"}}, blobs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCountBlockedExpiredSessionsTreatsEmptyReadySetAsAllBlocked(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT COUNT\(\*\).*WHERE NOT .*storage_backend=ANY`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	blocked, err := repository.CountBlockedExpiredSessions(context.Background(), []string{})

	require.NoError(t, err)
	require.Equal(t, int64(3), blocked)
	require.NoError(t, mock.ExpectationsWereMet())
}
