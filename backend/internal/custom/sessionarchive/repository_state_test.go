package sessionarchive

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestAddBlobRefLocksAndOnlyAttachesReadyBlob(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	blobID := int64(7)
	event := CaptureEvent{Meta: CaptureMeta{Purpose: PurposeResponse, SequenceNo: 9}, Observation: Observation{ObservedBytes: 10, StoredBytes: 10}}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.status FROM session_archive_sessions").WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery("SELECT status FROM session_archive_blobs").WithArgs(blobID).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("ready"))
	mock.ExpectExec("INSERT INTO session_archive_blob_refs.*ON CONFLICT \\(owner_type,owner_id,purpose,sequence_no\\) DO UPDATE").WithArgs(blobID, "request", int64(3), PurposeResponse, "", "", "", int64(10), int64(10), false, "", int64(9), event.Meta.OccurredAt).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	require.NoError(t, repository.AddBlobRef(context.Background(), ProjectionIDs{RequestID: 3}, event, &blobID))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAddBlobRefRejectsDeletingBlob(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	blobID := int64(7)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.status FROM session_archive_sessions").WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery("SELECT status FROM session_archive_blobs").WithArgs(blobID).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("deleting"))
	mock.ExpectRollback()
	err = repository.AddBlobRef(context.Background(), ProjectionIDs{RequestID: 3}, CaptureEvent{Meta: CaptureMeta{Purpose: PurposeResponse}}, &blobID)
	require.ErrorContains(t, err, "not ready")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAddBlobRefRejectsDeletingOwnerBeforeLockingBlob(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	blobID := int64(7)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.status FROM session_archive_sessions").WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("deleting"))
	mock.ExpectRollback()
	err = repository.AddBlobRef(context.Background(), ProjectionIDs{RequestID: 3}, CaptureEvent{Meta: CaptureMeta{Purpose: PurposeResponse}}, &blobID)
	require.ErrorContains(t, err, "owner is deleting")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClaimGCBlobsRechecksZeroReferences(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	mock.ExpectQuery("UPDATE session_archive_blobs SET status='deleting'.*NOT EXISTS.*session_archive_blob_refs").WithArgs(10).WillReturnRows(sqlmock.NewRows([]string{"id", "object_key"}))
	blobs, err := repository.ClaimGCBlobs(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, blobs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRecoverStalePendingSchedulesUnreferencedObjectsForGC(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	mock.ExpectExec("UPDATE session_archive_blobs b SET status='gc_pending'.*status='failed'.*status='pending'.*NOT EXISTS.*session_archive_blob_refs").WillReturnResult(sqlmock.NewResult(0, 2))

	recovered, err := repository.RecoverStalePending(context.Background())

	require.NoError(t, err)
	require.Equal(t, int64(2), recovered)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAddStorageFailureRefPersistsMissingBodyWithoutOverwritingExistingRef(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	event := CaptureEvent{
		Meta:        CaptureMeta{Purpose: PurposeResponse, SequenceNo: 9},
		Observation: Observation{ObservedSHA256: "observed", ObservedBytes: 10, DroppedReason: "storage_failed"},
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT s.status FROM session_archive_sessions").WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectExec("INSERT INTO session_archive_blob_refs.*ON CONFLICT \\(owner_type,owner_id,purpose,sequence_no\\) DO NOTHING").
		WithArgs(nil, "request", int64(3), PurposeResponse, "", "", "observed", int64(10), int64(0), false, "storage_failed", int64(9), event.Meta.OccurredAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, repository.AddStorageFailureRef(context.Background(), ProjectionIDs{RequestID: 3}, event))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScheduleOrphanReadyBlobsRequiresAgeAndZeroReferences(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	mock.ExpectExec("UPDATE session_archive_blobs b SET status='gc_pending'.*status='ready'.*updated_at<NOW.*NOT EXISTS.*session_archive_blob_refs.*FOR UPDATE SKIP LOCKED LIMIT").
		WithArgs("600 seconds", 25).
		WillReturnResult(sqlmock.NewResult(0, 2))

	scheduled, err := repository.ScheduleOrphanReadyBlobs(context.Background(), 10*time.Minute, 25)

	require.NoError(t, err)
	require.Equal(t, int64(2), scheduled)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDeleteExpiredCorrelationFencesIsBatchLimited(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	mock.ExpectExec("DELETE FROM session_archive_correlation_fences.*expires_at<=NOW.*LIMIT \\$1").WithArgs(50).WillReturnResult(sqlmock.NewResult(0, 4))

	deleted, err := repository.DeleteExpiredCorrelationFences(context.Background(), 50)

	require.NoError(t, err)
	require.Equal(t, int64(4), deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}
