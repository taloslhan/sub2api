package sessionarchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

const (
	// Collector 队列和上传租约都远短于 7 天；该窗口跨进程重启覆盖迟到重试，
	// 同时允许维护任务有界清理不会永久增长的 correlation fence。
	correlationFenceTTL = 7 * 24 * time.Hour
	orphanReadyBlobAge  = 10 * time.Minute
)

type DeletionJob struct {
	ID                int64
	Status            string
	MatchedSessions   int64
	ProcessedSessions int64
	DeletedSessions   int64
	FailedSessions    int64
	ReleasedBlobs     int64
	LastError         string
	CreatedAt         time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
}

type deletionTarget struct {
	SessionIDs []int64
	Filter     SessionFilter
}

func (r *Repository) CreateDeletionJob(ctx context.Context, requestedBy int64, sessionIDs []int64, filter SessionFilter) (DeletionJob, error) {
	target := deletionTarget{SessionIDs: append([]int64(nil), sessionIDs...), Filter: filter}
	payload, err := json.Marshal(target)
	if err != nil {
		return DeletionJob{}, err
	}
	var job DeletionJob
	err = r.db.QueryRowContext(ctx, "INSERT INTO session_archive_deletion_jobs (requested_by,normalized_filter,target_count) VALUES ($1,$2,$3) RETURNING id,status,target_count,processed_count,deleted_count,released_blob_count,failed_count,created_at", requestedBy, payload, len(sessionIDs)).Scan(&job.ID, &job.Status, &job.MatchedSessions, &job.ProcessedSessions, &job.DeletedSessions, &job.ReleasedBlobs, &job.FailedSessions, &job.CreatedAt)
	return job, err
}

func (r *Repository) ListDeletionJobs(ctx context.Context, page, pageSize int) ([]DeletionJob, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_archive_deletion_jobs").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.QueryContext(ctx, "SELECT id,status,target_count,processed_count,deleted_count,released_blob_count,failed_count,last_error,created_at,started_at,completed_at FROM session_archive_deletion_jobs ORDER BY id DESC LIMIT $1 OFFSET $2", pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var jobs []DeletionJob
	for rows.Next() {
		var job DeletionJob
		if err := rows.Scan(&job.ID, &job.Status, &job.MatchedSessions, &job.ProcessedSessions, &job.DeletedSessions, &job.ReleasedBlobs, &job.FailedSessions, &job.LastError, &job.CreatedAt, &job.StartedAt, &job.FinishedAt); err != nil {
			return nil, 0, err
		}
		jobs = append(jobs, job)
	}
	return jobs, total, rows.Err()
}

func (r *Repository) GetDeletionJob(ctx context.Context, id int64) (DeletionJob, error) {
	var job DeletionJob
	err := r.db.QueryRowContext(ctx, "SELECT id,status,target_count,processed_count,deleted_count,released_blob_count,failed_count,last_error,created_at,started_at,completed_at FROM session_archive_deletion_jobs WHERE id=$1", id).Scan(&job.ID, &job.Status, &job.MatchedSessions, &job.ProcessedSessions, &job.DeletedSessions, &job.ReleasedBlobs, &job.FailedSessions, &job.LastError, &job.CreatedAt, &job.StartedAt, &job.FinishedAt)
	return job, err
}

func (r *Repository) ProcessDeletionJob(ctx context.Context, batch int, gcGrace time.Duration) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	var payload []byte
	var processed int64
	err = tx.QueryRowContext(ctx, "SELECT id,normalized_filter,processed_count FROM session_archive_deletion_jobs WHERE status IN ('pending','running') ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1").Scan(&id, &payload, &processed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var target deletionTarget
	if err := json.Unmarshal(payload, &target); err != nil {
		_, _ = tx.ExecContext(ctx, "UPDATE session_archive_deletion_jobs SET status='failed',last_error='invalid normalized filter',completed_at=NOW(),updated_at=NOW() WHERE id=$1", id)
		return true, tx.Commit()
	}
	if batch < 1 {
		batch = 100
	}
	start := int(processed)
	if start >= len(target.SessionIDs) {
		_, _ = tx.ExecContext(ctx, "UPDATE session_archive_deletion_jobs SET status='completed',completed_at=NOW(),updated_at=NOW() WHERE id=$1", id)
		return true, tx.Commit()
	}
	end := start + batch
	if end > len(target.SessionIDs) {
		end = len(target.SessionIDs)
	}
	_, _ = tx.ExecContext(ctx, "UPDATE session_archive_deletion_jobs SET status='running',started_at=COALESCE(started_at,NOW()),updated_at=NOW() WHERE id=$1", id)
	if _, err := tx.ExecContext(ctx, "SAVEPOINT session_archive_delete_batch"); err != nil {
		return true, err
	}
	deleted, released, deleteErr := deleteSessionsTx(ctx, tx, target.SessionIDs[start:end], gcGrace)
	if deleteErr != nil {
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT session_archive_delete_batch"); err != nil {
			return true, err
		}
		message := deleteErr.Error()
		if len(message) > 512 {
			message = message[:512]
		}
		_, _ = tx.ExecContext(ctx, "UPDATE session_archive_deletion_jobs SET retry_count=retry_count+1,last_error=$2,updated_at=NOW() WHERE id=$1", id, message)
		return true, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT session_archive_delete_batch"); err != nil {
		return true, err
	}
	status := "running"
	var completedAt any
	if end == len(target.SessionIDs) {
		status = "completed"
		completedAt = time.Now().UTC()
	}
	_, err = tx.ExecContext(ctx, "UPDATE session_archive_deletion_jobs SET status=$2,processed_count=$3,deleted_count=deleted_count+$4,released_blob_count=released_blob_count+$5,last_error='',completed_at=$6,updated_at=NOW() WHERE id=$1", id, status, end, deleted, released, completedAt)
	if err != nil {
		return true, err
	}
	return true, tx.Commit()
}

func deleteSessionsTx(ctx context.Context, tx *sql.Tx, sessionIDs []int64, gcGrace time.Duration) (int64, int64, error) {
	if len(sessionIDs) == 0 {
		return 0, 0, nil
	}
	if err := persistCorrelationFencesTx(ctx, tx, sessionIDs, correlationFenceTTL); err != nil {
		return 0, 0, err
	}
	_, err := tx.ExecContext(ctx, "UPDATE session_archive_sessions SET status='deleting',deleting_at=NOW() WHERE id=ANY($1)", pq.Array(sessionIDs))
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT br.blob_id FROM session_archive_blob_refs br WHERE br.blob_id IS NOT NULL AND ((br.owner_type='session' AND br.owner_id=ANY($1)) OR (br.owner_type='turn' AND br.owner_id IN (SELECT id FROM session_archive_turns WHERE session_id=ANY($1))) OR (br.owner_type='request' AND br.owner_id IN (SELECT r.id FROM session_archive_requests r JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=ANY($1))) OR (br.owner_type='attempt' AND br.owner_id IN (SELECT a.id FROM session_archive_attempts a JOIN session_archive_requests r ON r.id=a.request_id JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=ANY($1))))", pq.Array(sessionIDs))
	if err != nil {
		return 0, 0, err
	}
	var blobIDs []int64
	for rows.Next() {
		var blobID int64
		if err := rows.Scan(&blobID); err != nil {
			_ = rows.Close()
			return 0, 0, err
		}
		blobIDs = append(blobIDs, blobID)
	}
	_ = rows.Close()
	_, err = tx.ExecContext(ctx, "DELETE FROM session_archive_blob_refs br WHERE (br.owner_type='session' AND br.owner_id=ANY($1)) OR (br.owner_type='turn' AND br.owner_id IN (SELECT id FROM session_archive_turns WHERE session_id=ANY($1))) OR (br.owner_type='request' AND br.owner_id IN (SELECT r.id FROM session_archive_requests r JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=ANY($1))) OR (br.owner_type='attempt' AND br.owner_id IN (SELECT a.id FROM session_archive_attempts a JOIN session_archive_requests r ON r.id=a.request_id JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=ANY($1)))", pq.Array(sessionIDs))
	if err != nil {
		return 0, 0, err
	}
	deleteResult, err := tx.ExecContext(ctx, "DELETE FROM session_archive_sessions WHERE id=ANY($1)", pq.Array(sessionIDs))
	if err != nil {
		return 0, 0, err
	}
	deleted, _ := deleteResult.RowsAffected()
	released := int64(0)
	for _, blobID := range blobIDs {
		result, err := tx.ExecContext(ctx, "UPDATE session_archive_blobs SET status='gc_pending',gc_after=NOW()+$2::interval,updated_at=NOW() WHERE id=$1 AND status IN ('ready','failed') AND NOT EXISTS (SELECT 1 FROM session_archive_blob_refs WHERE blob_id=$1)", blobID, intervalLiteral(gcGrace))
		if err != nil {
			return deleted, released, err
		}
		n, _ := result.RowsAffected()
		released += n
	}
	return deleted, released, nil
}

type correlationFenceKey struct {
	tenantID             int64
	userID               int64
	apiKeyID             int64
	protocol             string
	correlationRequestID string
}

func persistCorrelationFencesTx(ctx context.Context, tx *sql.Tx, sessionIDs []int64, ttl time.Duration) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT s.tenant_id,COALESCE(s.user_id,0),COALESCE(s.api_key_id,0),s.protocol,r.correlation_request_id FROM session_archive_sessions s JOIN session_archive_turns t ON t.session_id=s.id JOIN session_archive_requests r ON r.turn_id=t.id WHERE s.id=ANY($1) AND r.correlation_request_id<>'' ORDER BY s.tenant_id,COALESCE(s.user_id,0),COALESCE(s.api_key_id,0),s.protocol,r.correlation_request_id", pq.Array(sessionIDs))
	if err != nil {
		return err
	}
	keys := make([]correlationFenceKey, 0, len(sessionIDs))
	for rows.Next() {
		var key correlationFenceKey
		if err := rows.Scan(&key.tenantID, &key.userID, &key.apiKeyID, &key.protocol, &key.correlationRequestID); err != nil {
			_ = rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	lastIsolationKey := ""
	for _, key := range keys {
		isolationKey := fmt.Sprintf("%d:%d:%d:%s", key.tenantID, key.userID, key.apiKeyID, key.protocol)
		if isolationKey != lastIsolationKey {
			if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", isolationKey); err != nil {
				return err
			}
			lastIsolationKey = isolationKey
		}
		correlationKey := isolationKey + ":" + key.correlationRequestID
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", correlationKey); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO session_archive_correlation_fences (tenant_id,user_id,api_key_id,protocol,correlation_request_id,expires_at) SELECT DISTINCT s.tenant_id,COALESCE(s.user_id,0),COALESCE(s.api_key_id,0),s.protocol,r.correlation_request_id,NOW()+$2::interval FROM session_archive_sessions s JOIN session_archive_turns t ON t.session_id=s.id JOIN session_archive_requests r ON r.turn_id=t.id WHERE s.id=ANY($1) AND r.correlation_request_id<>'' ON CONFLICT (tenant_id,user_id,api_key_id,protocol,correlation_request_id) DO UPDATE SET expires_at=GREATEST(session_archive_correlation_fences.expires_at,EXCLUDED.expires_at)", pq.Array(sessionIDs), intervalLiteral(ttl))
	return err
}

func (r *Repository) DeleteExpiredSessions(ctx context.Context, limit int, gcGrace time.Duration) (int64, error) {
	if limit < 1 {
		limit = 100
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var leader bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock(694208311321144028)").Scan(&leader); err != nil || !leader {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM session_archive_sessions WHERE expires_at<=NOW() AND status<>'deleting' ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT $1", limit)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if _, _, err := deleteSessionsTx(ctx, tx, ids, gcGrace); err != nil {
		return 0, err
	}
	return int64(len(ids)), tx.Commit()
}

type GCBlob struct {
	ID        int64
	ObjectKey string
}

func (r *Repository) ClaimGCBlobs(ctx context.Context, limit int) ([]GCBlob, error) {
	if limit < 1 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, "UPDATE session_archive_blobs SET status='deleting',updated_at=NOW() WHERE id IN (SELECT b.id FROM session_archive_blobs b WHERE b.status='gc_pending' AND b.gc_after<=NOW() AND NOT EXISTS (SELECT 1 FROM session_archive_blob_refs br WHERE br.blob_id=b.id) ORDER BY b.id FOR UPDATE SKIP LOCKED LIMIT $1) AND NOT EXISTS (SELECT 1 FROM session_archive_blob_refs br WHERE br.blob_id=session_archive_blobs.id) RETURNING id,object_key", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var blobs []GCBlob
	for rows.Next() {
		var blob GCBlob
		if err := rows.Scan(&blob.ID, &blob.ObjectKey); err != nil {
			return nil, err
		}
		blobs = append(blobs, blob)
	}
	return blobs, rows.Err()
}

func (r *Repository) FinishGCBlob(ctx context.Context, id int64, deleteErr error, retryDelay time.Duration) error {
	if deleteErr == nil {
		_, err := r.db.ExecContext(ctx, "DELETE FROM session_archive_blobs WHERE id=$1 AND status='deleting' AND NOT EXISTS (SELECT 1 FROM session_archive_blob_refs WHERE blob_id=$1)", id)
		return err
	}
	message := deleteErr.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	_, err := r.db.ExecContext(ctx, "UPDATE session_archive_blobs SET status='gc_pending',gc_after=NOW()+$2::interval,retry_count=retry_count+1,last_error=$3,updated_at=NOW() WHERE id=$1 AND status='deleting'", id, intervalLiteral(retryDelay), message)
	return err
}

func (r *Repository) RecoverStalePending(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, "UPDATE session_archive_blobs b SET status='gc_pending',gc_after=NOW(),owner_token='',lease_expires_at=NULL,retry_count=retry_count+1,last_error=CASE WHEN b.status='pending' THEN 'pending upload lease expired; object cleanup scheduled' ELSE 'failed upload object cleanup scheduled' END,updated_at=NOW() WHERE (b.status='failed' OR (b.status='pending' AND b.lease_expires_at<NOW())) AND NOT EXISTS (SELECT 1 FROM session_archive_blob_refs br WHERE br.blob_id=b.id)")
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repository) RecoverStaleDeleting(ctx context.Context, staleAfter time.Duration) (int64, error) {
	result, err := r.db.ExecContext(ctx, "UPDATE session_archive_blobs b SET status='gc_pending',gc_after=NOW(),retry_count=retry_count+1,last_error='stale deleting blob reclaimed',updated_at=NOW() WHERE b.status='deleting' AND b.updated_at<NOW()-$1::interval AND NOT EXISTS (SELECT 1 FROM session_archive_blob_refs br WHERE br.blob_id=b.id)", intervalLiteral(staleAfter))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repository) ScheduleOrphanReadyBlobs(ctx context.Context, staleAfter time.Duration, limit int) (int64, error) {
	if limit < 1 {
		limit = 100
	}
	result, err := r.db.ExecContext(ctx, "UPDATE session_archive_blobs b SET status='gc_pending',gc_after=NOW(),last_error='ready blob remained unreferenced',updated_at=NOW() WHERE b.id IN (SELECT candidate.id FROM session_archive_blobs candidate WHERE candidate.status='ready' AND candidate.updated_at<NOW()-$1::interval AND NOT EXISTS (SELECT 1 FROM session_archive_blob_refs br WHERE br.blob_id=candidate.id) ORDER BY candidate.id FOR UPDATE SKIP LOCKED LIMIT $2) AND b.status='ready' AND NOT EXISTS (SELECT 1 FROM session_archive_blob_refs br WHERE br.blob_id=b.id)", intervalLiteral(staleAfter), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repository) DeleteExpiredCorrelationFences(ctx context.Context, limit int) (int64, error) {
	if limit < 1 {
		limit = 100
	}
	result, err := r.db.ExecContext(ctx, "DELETE FROM session_archive_correlation_fences WHERE ctid IN (SELECT ctid FROM session_archive_correlation_fences WHERE expires_at<=NOW() ORDER BY expires_at LIMIT $1)", limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *Repository) BlobBacklogs(ctx context.Context) (pending, gc int64, err error) {
	err = r.db.QueryRowContext(ctx, "SELECT COUNT(*) FILTER (WHERE status IN ('pending','failed')),COUNT(*) FILTER (WHERE status IN ('gc_pending','deleting')) FROM session_archive_blobs").Scan(&pending, &gc)
	return
}
