package sessionarchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

var ErrCorrelationFenced = errors.New("session archive correlation is fenced after deletion")

type Repository struct {
	db         *sql.DB
	digestKey  []byte
	digestKeys [][]byte
}

func NewRepositoryWithDigestKey(db *sql.DB, digestKey []byte) (*Repository, error) {
	repository, err := NewRepository(db)
	if err != nil {
		return nil, err
	}
	if len(digestKey) != 32 {
		return nil, errors.New("session archive stable ID digest key must be 32 bytes")
	}
	repository.digestKey = append([]byte(nil), digestKey...)
	repository.digestKeys = [][]byte{append([]byte(nil), digestKey...)}
	return repository, nil
}

func NewRepositoryWithDigestKeys(db *sql.DB, activeKeyID string, keys map[string][]byte) (*Repository, error) {
	active, ok := keys[activeKeyID]
	if !ok {
		return nil, errors.New("session archive active digest key is unavailable")
	}
	repository, err := NewRepositoryWithDigestKey(db, active)
	if err != nil {
		return nil, err
	}
	for id, key := range keys {
		if id == activeKeyID {
			continue
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("session archive digest key %s must be 32 bytes", id)
		}
		repository.digestKeys = append(repository.digestKeys, append([]byte(nil), key...))
	}
	return repository, nil
}

func NewRepository(db *sql.DB) (*Repository, error) {
	if db == nil {
		return nil, errors.New("session archive repository requires database")
	}
	return &Repository{db: db}, nil
}

func (r *Repository) CheckSchema(ctx context.Context) error {
	var present bool
	err := r.db.QueryRowContext(ctx, "SELECT to_regclass('session_archive_sessions') IS NOT NULL").Scan(&present)
	if err != nil {
		return fmt.Errorf("check session archive schema: %w", err)
	}
	if !present {
		return errors.New("session archive schema is missing")
	}
	return nil
}

func (r *Repository) PoliciesFor(ctx context.Context, identity PolicyIdentity) ([]Policy, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id, scope_type, scope_id, state, capture_raw_request, capture_upstream_request, capture_response, capture_tools, capture_attachments, payload_max_bytes, retention_days, created_at, updated_at FROM session_archive_policies WHERE (scope_type='global' AND scope_id=0) OR (scope_type='group' AND scope_id=$1) OR (scope_type='user' AND scope_id=$2) OR (scope_type='api_key' AND scope_id=$3)", identity.GroupID, identity.UserID, identity.APIKeyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPolicies(rows)
}

func (r *Repository) ListPolicies(ctx context.Context) ([]Policy, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT id, scope_type, scope_id, state, capture_raw_request, capture_upstream_request, capture_response, capture_tools, capture_attachments, payload_max_bytes, retention_days, created_at, updated_at FROM session_archive_policies ORDER BY CASE scope_type WHEN 'global' THEN 0 WHEN 'group' THEN 1 WHEN 'user' THEN 2 ELSE 3 END, scope_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPolicies(rows)
}

func scanPolicies(rows *sql.Rows) ([]Policy, error) {
	var policies []Policy
	for rows.Next() {
		var policy Policy
		if err := rows.Scan(&policy.ID, &policy.ScopeType, &policy.ScopeID, &policy.State, &policy.CaptureRawRequest, &policy.CaptureUpstreamRequest, &policy.CaptureResponse, &policy.CaptureTools, &policy.CaptureAttachments, &policy.PayloadMaxBytes, &policy.RetentionDays, &policy.CreatedAt, &policy.UpdatedAt); err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (r *Repository) UpsertPolicy(ctx context.Context, policy Policy) (Policy, error) {
	query := "INSERT INTO session_archive_policies (scope_type,scope_id,state,capture_raw_request,capture_upstream_request,capture_response,capture_tools,capture_attachments,payload_max_bytes,retention_days) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (scope_type,scope_id) DO UPDATE SET state=EXCLUDED.state,capture_raw_request=EXCLUDED.capture_raw_request,capture_upstream_request=EXCLUDED.capture_upstream_request,capture_response=EXCLUDED.capture_response,capture_tools=EXCLUDED.capture_tools,capture_attachments=EXCLUDED.capture_attachments,payload_max_bytes=EXCLUDED.payload_max_bytes,retention_days=EXCLUDED.retention_days,updated_at=NOW() RETURNING id,created_at,updated_at"
	err := r.db.QueryRowContext(ctx, query, policy.ScopeType, policy.ScopeID, policy.State, policy.CaptureRawRequest, policy.CaptureUpstreamRequest, policy.CaptureResponse, policy.CaptureTools, policy.CaptureAttachments, policy.PayloadMaxBytes, policy.RetentionDays).Scan(&policy.ID, &policy.CreatedAt, &policy.UpdatedAt)
	return policy, err
}

func (r *Repository) DeletePolicy(ctx context.Context, scope PolicyScope, scopeID int64) error {
	if scope == ScopeGlobal {
		_, err := r.db.ExecContext(ctx, "UPDATE session_archive_policies SET state='off',updated_at=NOW() WHERE scope_type='global' AND scope_id=0")
		return err
	}
	_, err := r.db.ExecContext(ctx, "DELETE FROM session_archive_policies WHERE scope_type=$1 AND scope_id=$2", scope, scopeID)
	return err
}

type ProjectionIDs struct {
	SessionID int64
	TurnID    int64
	RequestID int64
	AttemptID int64
}

// EnsureProjection 通过 correlation advisory lock 串行化同一请求事件，避免多 worker 重排产生重复投影。
func (r *Repository) EnsureProjection(ctx context.Context, event CaptureEvent, mergeWindow time.Duration) (ProjectionIDs, error) {
	if strings.TrimSpace(event.Meta.CorrelationRequestID) == "" {
		return ProjectionIDs{}, errors.New("correlation_request_id is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectionIDs{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if event.Meta.StableSessionID != "" && len(r.digestKey) != 32 {
		return ProjectionIDs{}, errors.New("stable ID digest key is unavailable")
	}
	event.Meta.NormalizedMessageChain = HashMessageChainItems(event.Meta.NormalizedMessageChain)
	isolationKey := fmt.Sprintf("%d:%d:%d:%s", event.Meta.TenantID, event.Meta.UserID, event.Meta.APIKeyID, event.Meta.Protocol)
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", isolationKey); err != nil {
		return ProjectionIDs{}, err
	}
	correlationKey := isolationKey + ":" + event.Meta.CorrelationRequestID
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", correlationKey); err != nil {
		return ProjectionIDs{}, err
	}
	var fenced bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM session_archive_correlation_fences WHERE correlation_request_id=$1 AND tenant_id=$2 AND user_id=$3 AND api_key_id=$4 AND protocol=$5 AND expires_at>NOW())", event.Meta.CorrelationRequestID, event.Meta.TenantID, event.Meta.UserID, event.Meta.APIKeyID, event.Meta.Protocol).Scan(&fenced); err != nil {
		return ProjectionIDs{}, err
	}
	if fenced {
		return ProjectionIDs{}, ErrCorrelationFenced
	}
	var ids ProjectionIDs
	err = tx.QueryRowContext(ctx, "SELECT s.id,t.id,r.id FROM session_archive_requests r JOIN session_archive_turns t ON t.id=r.turn_id JOIN session_archive_sessions s ON s.id=t.session_id WHERE r.correlation_request_id=$1 AND s.tenant_id=$2 AND COALESCE(s.user_id,0)=$3 AND COALESCE(s.api_key_id,0)=$4 AND s.protocol=$5 ORDER BY r.id DESC LIMIT 1", event.Meta.CorrelationRequestID, event.Meta.TenantID, event.Meta.UserID, event.Meta.APIKeyID, event.Meta.Protocol).Scan(&ids.SessionID, &ids.TurnID, &ids.RequestID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ProjectionIDs{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		sessionID, method, createErr := r.resolveOrCreateSession(ctx, tx, event, mergeWindow)
		if createErr != nil {
			return ProjectionIDs{}, createErr
		}
		ids.SessionID = sessionID
		chainJSON, _ := json.Marshal(event.Meta.NormalizedMessageChain)
		err = tx.QueryRowContext(ctx, "INSERT INTO session_archive_turns (session_id,sequence_no,protocol_turn_id,message_chain_digest,message_chain_hashes,status,started_at) VALUES ($1,CASE WHEN $2>0 THEN $2 ELSE (SELECT COALESCE(MAX(sequence_no),0)+1 FROM session_archive_turns WHERE session_id=$1) END,$3,$4,$5,'active',$6) RETURNING id", sessionID, event.Meta.TurnSequenceNo, event.Meta.ProtocolTurnID, DigestMessageChain(event.Meta.NormalizedMessageChain), chainJSON, event.Meta.OccurredAt).Scan(&ids.TurnID)
		if err != nil {
			return ProjectionIDs{}, err
		}
		policyJSON, _ := json.Marshal(event.Meta.Policy)
		metadataJSON, _ := json.Marshal(AllowMetadata(stringMapToHeader(event.Meta.Metadata)))
		err = tx.QueryRowContext(ctx, "INSERT INTO session_archive_requests (turn_id,correlation_request_id,billing_request_id,client_request_id,upstream_request_id,endpoint,model,status,policy_snapshot,metadata,started_at) VALUES ($1,$2,$3,$4,$5,$6,$7,'active',$8,$9,$10) RETURNING id", ids.TurnID, event.Meta.CorrelationRequestID, event.Meta.BillingRequestID, event.Meta.ClientRequestID, event.Meta.UpstreamRequestID, event.Meta.Endpoint, event.Meta.Model, policyJSON, metadataJSON, event.Meta.OccurredAt).Scan(&ids.RequestID)
		if err != nil {
			return ProjectionIDs{}, err
		}
		_, _ = tx.ExecContext(ctx, "UPDATE session_archive_sessions SET merge_method=$2 WHERE id=$1 AND merge_method='new'", sessionID, method)
	}
	if event.Meta.Kind == EventAttempt {
		status := normalizeStatus(event.Meta.Status)
		var completedAt any
		if status != "active" {
			completedAt = event.Meta.OccurredAt.Add(event.Meta.Duration)
		}
		query := "INSERT INTO session_archive_attempts AS current (request_id,attempt_no,account_id,transform_type,upstream_request_id,upstream_status,status,error_class,error_code,duration_ms,is_final,started_at,completed_at) VALUES ($1,$2,NULLIF($3,0),$4,$5,NULLIF($6,0),$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (request_id,attempt_no) DO UPDATE SET account_id=COALESCE(EXCLUDED.account_id,current.account_id),transform_type=COALESCE(NULLIF(EXCLUDED.transform_type,''),current.transform_type),upstream_request_id=COALESCE(NULLIF(EXCLUDED.upstream_request_id,''),current.upstream_request_id),upstream_status=COALESCE(EXCLUDED.upstream_status,current.upstream_status),status=CASE WHEN current.is_final OR (EXCLUDED.status='active' AND current.completed_at IS NOT NULL) THEN current.status ELSE EXCLUDED.status END,error_class=COALESCE(NULLIF(EXCLUDED.error_class,''),current.error_class),error_code=COALESCE(NULLIF(EXCLUDED.error_code,''),current.error_code),duration_ms=GREATEST(current.duration_ms,EXCLUDED.duration_ms),is_final=current.is_final OR EXCLUDED.is_final,started_at=LEAST(current.started_at,EXCLUDED.started_at),completed_at=CASE WHEN EXCLUDED.completed_at IS NULL THEN current.completed_at WHEN current.completed_at IS NULL OR EXCLUDED.duration_ms>=current.duration_ms THEN EXCLUDED.completed_at ELSE current.completed_at END RETURNING id"
		err = tx.QueryRowContext(ctx, query, ids.RequestID, event.Meta.AttemptNo, event.Meta.AccountID, event.Meta.TransformType, event.Meta.UpstreamRequestID, event.Meta.UpstreamStatus, status, event.Meta.ErrorClass, event.Meta.ErrorCode, event.Meta.Duration.Milliseconds(), event.Meta.FinalAttempt, event.Meta.OccurredAt, completedAt).Scan(&ids.AttemptID)
		if err != nil {
			return ProjectionIDs{}, err
		}
	}
	if event.Meta.Kind == EventTerminal {
		status := normalizeStatus(event.Meta.Status)
		if _, err = tx.ExecContext(ctx, "UPDATE session_archive_requests SET status=$2,error_class=$3,error_code=$4,client_disconnected=$5,upstream_request_id=COALESCE(NULLIF($6,''),upstream_request_id),completed_at=$7 WHERE id=$1", ids.RequestID, status, event.Meta.ErrorClass, event.Meta.ErrorCode, event.Meta.ClientDisconnected, event.Meta.UpstreamRequestID, event.Meta.OccurredAt); err != nil {
			return ProjectionIDs{}, err
		}
		_, _ = tx.ExecContext(ctx, "UPDATE session_archive_turns SET status=$2,completed_at=$3 WHERE id=$1", ids.TurnID, status, event.Meta.OccurredAt)
		_, _ = tx.ExecContext(ctx, "UPDATE session_archive_sessions SET status=CASE WHEN $2='failed' THEN 'failed' ELSE 'completed' END,last_active_at=$3,last_model=COALESCE(NULLIF($4,''),last_model) WHERE id=$1", ids.SessionID, status, event.Meta.OccurredAt, event.Meta.Model)
		if event.Meta.AttemptNo > 0 {
			query := "INSERT INTO session_archive_attempts AS current (request_id,attempt_no,account_id,transform_type,upstream_request_id,upstream_status,status,error_class,error_code,duration_ms,is_final,started_at,completed_at) VALUES ($1,$2,NULLIF($3,0),$4,$5,NULLIF($6,0),$7,$8,$9,$10,TRUE,$11,$11) ON CONFLICT (request_id,attempt_no) DO UPDATE SET account_id=COALESCE(EXCLUDED.account_id,current.account_id),transform_type=COALESCE(NULLIF(EXCLUDED.transform_type,''),current.transform_type),upstream_request_id=COALESCE(NULLIF(EXCLUDED.upstream_request_id,''),current.upstream_request_id),upstream_status=COALESCE(EXCLUDED.upstream_status,current.upstream_status),status=EXCLUDED.status,error_class=COALESCE(NULLIF(EXCLUDED.error_class,''),current.error_class),error_code=COALESCE(NULLIF(EXCLUDED.error_code,''),current.error_code),duration_ms=GREATEST(current.duration_ms,EXCLUDED.duration_ms),is_final=TRUE,started_at=LEAST(current.started_at,EXCLUDED.started_at),completed_at=EXCLUDED.completed_at RETURNING id"
			err = tx.QueryRowContext(ctx, query, ids.RequestID, event.Meta.AttemptNo, event.Meta.AccountID, event.Meta.TransformType, event.Meta.UpstreamRequestID, event.Meta.UpstreamStatus, status, event.Meta.ErrorClass, event.Meta.ErrorCode, event.Meta.Duration.Milliseconds(), event.Meta.OccurredAt).Scan(&ids.AttemptID)
			if err != nil {
				return ProjectionIDs{}, err
			}
		}
	}
	if event.Observation.Truncated {
		_, _ = tx.ExecContext(ctx, "UPDATE session_archive_requests SET has_truncated=TRUE WHERE id=$1", ids.RequestID)
	}
	if err := tx.Commit(); err != nil {
		return ProjectionIDs{}, err
	}
	return ids, nil
}

func stringMapToHeader(input map[string]string) map[string][]string {
	output := make(map[string][]string, len(input))
	for key, value := range input {
		output[key] = []string{value}
	}
	return output
}

func (r *Repository) resolveOrCreateSession(ctx context.Context, tx *sql.Tx, event CaptureEvent, window time.Duration) (int64, string, error) {
	identity := MergeIdentity{TenantID: event.Meta.TenantID, UserID: event.Meta.UserID, APIKeyID: event.Meta.APIKeyID, Protocol: event.Meta.Protocol}
	stableDigest := DigestStableID(r.digestKey, event.Meta.StableSessionID)
	stableDigests := make([]string, 0, len(r.digestKeys))
	for _, key := range r.digestKeys {
		if digest := DigestStableID(key, event.Meta.StableSessionID); digest != "" {
			stableDigests = append(stableDigests, digest)
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT s.id,s.tenant_id,COALESCE(s.user_id,0),COALESCE(s.api_key_id,0),s.protocol,s.stable_id_digest,COALESCE(t.message_chain_hashes,'[]'::jsonb),s.last_active_at FROM session_archive_sessions s LEFT JOIN LATERAL (SELECT message_chain_hashes FROM session_archive_turns WHERE session_id=s.id ORDER BY sequence_no DESC LIMIT 1) t ON TRUE WHERE s.status<>'deleting' AND s.tenant_id=$1 AND COALESCE(s.user_id,0)=$2 AND COALESCE(s.api_key_id,0)=$3 AND s.protocol=$4 AND ((COALESCE(array_length($6::text[],1),0)>0 AND s.stable_id_digest=ANY($6::text[])) OR (COALESCE(array_length($6::text[],1),0)=0 AND s.last_active_at >= $5)) ORDER BY s.last_active_at DESC LIMIT 32", identity.TenantID, identity.UserID, identity.APIKeyID, identity.Protocol, event.Meta.OccurredAt.Add(-window), pq.Array(stableDigests))
	if err != nil {
		return 0, "", err
	}
	var candidates []MergeCandidate
	for rows.Next() {
		var candidate MergeCandidate
		var chainJSON []byte
		if err := rows.Scan(&candidate.SessionID, &candidate.Identity.TenantID, &candidate.Identity.UserID, &candidate.Identity.APIKeyID, &candidate.Identity.Protocol, &candidate.StableIDDigest, &chainJSON, &candidate.LastActiveAt); err != nil {
			_ = rows.Close()
			return 0, "", err
		}
		_ = json.Unmarshal(chainJSON, &candidate.MessageChain)
		if candidate.StableIDDigest != "" {
			candidate.StableIDDigest = stableDigest
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return 0, "", err
	}
	decision := ChooseMergeCandidate(identity, stableDigest, event.Meta.NormalizedMessageChain, event.Meta.OccurredAt, window, candidates)
	if decision.Matched {
		_, err := tx.ExecContext(ctx, "UPDATE session_archive_sessions SET last_active_at=$2,last_model=COALESCE(NULLIF($3,''),last_model) WHERE id=$1 AND status<>'deleting'", decision.SessionID, event.Meta.OccurredAt, event.Meta.Model)
		return decision.SessionID, decision.Method, err
	}
	policyJSON, _ := json.Marshal(event.Meta.Policy)
	retentionDays := event.Meta.Policy.RetentionDays
	if retentionDays < 1 {
		retentionDays = 30
	}
	coverage := event.Meta.CaptureCoverage
	if coverage == "" {
		coverage = "full"
	}
	var id int64
	query := "INSERT INTO session_archive_sessions (tenant_id,user_id,api_key_id,group_id,protocol,client,first_model,last_model,status,capture_coverage,stable_id_digest,merge_method,policy_snapshot,created_at,last_active_at,expires_at) VALUES ($1,NULLIF($2,0),NULLIF($3,0),NULLIF($4,0),$5,$6,$7,$7,'active',$8,$9,$10,$11,$12,$12,$13) RETURNING id"
	err = tx.QueryRowContext(ctx, query, event.Meta.TenantID, event.Meta.UserID, event.Meta.APIKeyID, event.Meta.GroupID, event.Meta.Protocol, event.Meta.Client, event.Meta.Model, coverage, stableDigest, decision.Method, policyJSON, event.Meta.OccurredAt, event.Meta.OccurredAt.Add(time.Duration(retentionDays)*24*time.Hour)).Scan(&id)
	return id, decision.Method, err
}

func normalizeStatus(status string) string {
	switch status {
	case "completed", "failed", "cancelled":
		return status
	default:
		return "active"
	}
}

type BlobRecord struct {
	ID        int64
	Info      EncodingInfo
	ObjectKey string
	Status    string
}

func (r *Repository) ReserveBlob(ctx context.Context, info EncodingInfo, objectKey, ownerToken string, lease time.Duration) (BlobRecord, bool, error) {
	record := BlobRecord{Info: info}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return BlobRecord{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	query := "INSERT INTO session_archive_blobs (stored_plaintext_sha256,stored_bytes,compressed_bytes,ciphertext_bytes,gzip_version,format_version,key_id,object_key,status,owner_token,lease_expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,NOW()+$10::interval) ON CONFLICT (stored_plaintext_sha256,format_version,key_id) DO NOTHING RETURNING id,object_key,status"
	err = tx.QueryRowContext(ctx, query, info.StoredPlaintextSHA256, info.StoredBytes, info.CompressedBytes, info.CiphertextBytes, info.GZIPVersion, info.FormatVersion, info.KeyID, objectKey, ownerToken, intervalLiteral(lease)).Scan(&record.ID, &record.ObjectKey, &record.Status)
	if err == nil {
		return record, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BlobRecord{}, false, err
	}
	var leaseExpired bool
	err = tx.QueryRowContext(ctx, "SELECT id,object_key,status,COALESCE(lease_expires_at<=NOW(),TRUE) FROM session_archive_blobs WHERE stored_plaintext_sha256=$1 AND format_version=$2 AND key_id=$3 FOR UPDATE", info.StoredPlaintextSHA256, info.FormatVersion, info.KeyID).Scan(&record.ID, &record.ObjectKey, &record.Status, &leaseExpired)
	if err != nil {
		return BlobRecord{}, false, err
	}
	owner := false
	if record.Status == "failed" || record.Status == "gc_pending" || (record.Status == "pending" && leaseExpired) {
		result, updateErr := tx.ExecContext(ctx, "UPDATE session_archive_blobs SET status='pending',owner_token=$2,lease_expires_at=NOW()+$3::interval,gc_after=NULL,last_error='',updated_at=NOW() WHERE id=$1 AND status<>'deleting'", record.ID, ownerToken, intervalLiteral(lease))
		if updateErr != nil {
			return BlobRecord{}, false, updateErr
		}
		rows, _ := result.RowsAffected()
		owner = rows == 1
		if owner {
			record.Status = "pending"
		}
	}
	if err := tx.Commit(); err != nil {
		return BlobRecord{}, false, err
	}
	return record, owner, nil
}

func (r *Repository) MarkBlobReady(ctx context.Context, id int64, ownerToken string) error {
	result, err := r.db.ExecContext(ctx, "UPDATE session_archive_blobs SET status='ready',owner_token='',lease_expires_at=NULL,last_error='',updated_at=NOW() WHERE id=$1 AND status='pending' AND owner_token=$2", id, ownerToken)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return errors.New("blob upload ownership lost")
	}
	return nil
}

func (r *Repository) MarkBlobFailed(ctx context.Context, id int64, ownerToken string, cause error) error {
	message := "unknown"
	if cause != nil {
		message = cause.Error()
	}
	if len(message) > 512 {
		message = message[:512]
	}
	_, err := r.db.ExecContext(ctx, "UPDATE session_archive_blobs SET status='failed',retry_count=retry_count+1,last_error=$3,owner_token='',lease_expires_at=NULL,updated_at=NOW() WHERE id=$1 AND owner_token=$2", id, ownerToken, message)
	return err
}

func (r *Repository) AddBlobRef(ctx context.Context, ids ProjectionIDs, event CaptureEvent, blobID *int64) error {
	return r.addBlobRef(ctx, ids, event, blobID, false)
}

func (r *Repository) AddStorageFailureRef(ctx context.Context, ids ProjectionIDs, event CaptureEvent) error {
	return r.addBlobRef(ctx, ids, event, nil, true)
}

func (r *Repository) addBlobRef(ctx context.Context, ids ProjectionIDs, event CaptureEvent, blobID *int64, preserveExisting bool) error {
	ownerType, ownerID := "request", ids.RequestID
	if event.Meta.Kind == EventAttempt && ids.AttemptID > 0 {
		ownerType, ownerID = "attempt", ids.AttemptID
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var ownerStatus string
	var ownerQuery string
	switch ownerType {
	case "session":
		ownerQuery = "SELECT status FROM session_archive_sessions WHERE id=$1 FOR UPDATE"
	case "turn":
		ownerQuery = "SELECT s.status FROM session_archive_sessions s JOIN session_archive_turns t ON t.session_id=s.id WHERE t.id=$1 FOR UPDATE OF s"
	case "request":
		ownerQuery = "SELECT s.status FROM session_archive_sessions s JOIN session_archive_turns t ON t.session_id=s.id JOIN session_archive_requests r ON r.turn_id=t.id WHERE r.id=$1 FOR UPDATE OF s"
	case "attempt":
		ownerQuery = "SELECT s.status FROM session_archive_sessions s JOIN session_archive_turns t ON t.session_id=s.id JOIN session_archive_requests r ON r.turn_id=t.id JOIN session_archive_attempts a ON a.request_id=r.id WHERE a.id=$1 FOR UPDATE OF s"
	default:
		return errors.New("invalid blob ref owner type")
	}
	if err := tx.QueryRowContext(ctx, ownerQuery, ownerID).Scan(&ownerStatus); err != nil {
		return err
	}
	if ownerStatus == "deleting" {
		return errors.New("archive owner is deleting")
	}
	if blobID != nil {
		var status string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM session_archive_blobs WHERE id=$1 FOR UPDATE", *blobID).Scan(&status); err != nil {
			return err
		}
		if status == "gc_pending" {
			if _, err := tx.ExecContext(ctx, "UPDATE session_archive_blobs SET status='ready',gc_after=NULL,updated_at=NOW() WHERE id=$1", *blobID); err != nil {
				return err
			}
			status = "ready"
		}
		if status != "ready" {
			return fmt.Errorf("blob %d is not ready for reference: %s", *blobID, status)
		}
	}
	conflictAction := "DO UPDATE SET blob_id=EXCLUDED.blob_id,direction=EXCLUDED.direction,content_type=EXCLUDED.content_type,observed_sha256=EXCLUDED.observed_sha256,observed_bytes=EXCLUDED.observed_bytes,stored_bytes=EXCLUDED.stored_bytes,truncated=EXCLUDED.truncated,dropped_reason=EXCLUDED.dropped_reason,occurred_at=EXCLUDED.occurred_at"
	if preserveExisting {
		conflictAction = "DO NOTHING"
	}
	query := "INSERT INTO session_archive_blob_refs (blob_id,owner_type,owner_id,purpose,direction,content_type,observed_sha256,observed_bytes,stored_bytes,truncated,dropped_reason,sequence_no,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT (owner_type,owner_id,purpose,sequence_no) " + conflictAction
	if _, err := tx.ExecContext(ctx, query, blobID, ownerType, ownerID, event.Meta.Purpose, event.Meta.Direction, event.Meta.ContentType, event.Observation.ObservedSHA256, event.Observation.ObservedBytes, event.Observation.StoredBytes, event.Observation.Truncated, event.Observation.DroppedReason, event.Meta.SequenceNo, event.Meta.OccurredAt); err != nil {
		return err
	}
	return tx.Commit()
}

func intervalLiteral(duration time.Duration) string {
	seconds := int64(duration.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%d seconds", seconds)
}
