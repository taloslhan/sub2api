package sessionarchive

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestEnsureProjectionCompletedAttemptPersistsCompletionAndUpstreamStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)

	occurredAt := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	duration := 125 * time.Millisecond
	event := CaptureEvent{Meta: CaptureMeta{
		Kind: EventAttempt, CorrelationRequestID: "corr-1", TenantID: 1, UserID: 2, APIKeyID: 3,
		Protocol: "messages", AttemptNo: 2, AccountID: 9, TransformType: "messages:tls_fingerprint",
		UpstreamRequestID: "req-upstream", UpstreamStatus: 200, Status: "completed",
		Duration: duration, OccurredAt: occurredAt,
	}}

	expectExistingProjection(mock, event.Meta, 11, 12, 13)
	mock.ExpectQuery("INSERT INTO session_archive_attempts AS current .*completed_at.*ON CONFLICT").
		WithArgs(int64(13), 2, int64(9), "messages:tls_fingerprint", "req-upstream", 200, "completed", "", "", duration.Milliseconds(), false, occurredAt, occurredAt.Add(duration)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(14))
	mock.ExpectCommit()

	ids, err := repository.EnsureProjection(context.Background(), event, 5*time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(14), ids.AttemptID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureProjectionTerminalUpsertsFinalAttempt(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)

	occurredAt := time.Date(2026, 8, 28, 10, 1, 0, 0, time.UTC)
	event := CaptureEvent{Meta: CaptureMeta{
		Kind: EventTerminal, CorrelationRequestID: "corr-2", TenantID: 1, UserID: 2, APIKeyID: 3,
		Protocol: "responses", AttemptNo: 3, Status: "failed", ErrorClass: "upstream_http",
		ErrorCode: "503", OccurredAt: occurredAt,
	}}

	expectExistingProjection(mock, event.Meta, 21, 22, 23)
	mock.ExpectExec("UPDATE session_archive_requests SET status=").
		WithArgs(int64(23), "failed", "upstream_http", "503", false, "", occurredAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE session_archive_turns SET status=").
		WithArgs(int64(22), "failed", occurredAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE session_archive_sessions SET status=").
		WithArgs(int64(21), "failed", occurredAt, "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO session_archive_attempts AS current .*is_final.*TRUE.*ON CONFLICT").
		WithArgs(int64(23), 3, int64(0), "", "", 0, "failed", "upstream_http", "503", int64(0), occurredAt).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(24))
	mock.ExpectCommit()

	ids, err := repository.EnsureProjection(context.Background(), event, 5*time.Minute)
	require.NoError(t, err)
	require.Equal(t, int64(24), ids.AttemptID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectExistingProjection(mock sqlmock.Sqlmock, meta CaptureMeta, sessionID, turnID, requestID int64) {
	mock.ExpectBegin()
	isolationKey := "1:2:3:" + meta.Protocol
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(isolationKey).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(isolationKey + ":" + meta.CorrelationRequestID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM session_archive_correlation_fences").
		WithArgs(meta.CorrelationRequestID, meta.TenantID, meta.UserID, meta.APIKeyID, meta.Protocol).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT s.id,t.id,r.id FROM session_archive_requests").
		WithArgs(meta.CorrelationRequestID, meta.TenantID, meta.UserID, meta.APIKeyID, meta.Protocol).
		WillReturnRows(sqlmock.NewRows([]string{"session_id", "turn_id", "request_id"}).AddRow(sessionID, turnID, requestID))
}
