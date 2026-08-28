package sessionarchive

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestEnsureProjectionRejectsFencedCorrelationWithoutRecreatingSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	event := CaptureEvent{Meta: CaptureMeta{TenantID: 1, UserID: 2, APIKeyID: 3, Protocol: "openai", CorrelationRequestID: "corr-deleted"}}

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("1:2:3:openai").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("1:2:3:openai:corr-deleted").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT EXISTS.*session_archive_correlation_fences").
		WithArgs("corr-deleted", int64(1), int64(2), int64(3), "openai").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	_, err = repository.EnsureProjection(context.Background(), event, 5*time.Minute)

	require.ErrorIs(t, err, ErrCorrelationFenced)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPersistCorrelationFencesUsesProjectionLockOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)

	mock.ExpectQuery("SELECT DISTINCT s.tenant_id.*session_archive_requests.*ORDER BY").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "user_id", "api_key_id", "protocol", "correlation_request_id"}).
			AddRow(1, 2, 3, "openai", "corr-a").
			AddRow(1, 2, 3, "openai", "corr-b"))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("1:2:3:openai").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("1:2:3:openai:corr-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("1:2:3:openai:corr-b").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO session_archive_correlation_fences").
		WithArgs(sqlmock.AnyArg(), "604800 seconds").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectRollback()

	require.NoError(t, persistCorrelationFencesTx(context.Background(), tx, []int64{9}, correlationFenceTTL))
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}
