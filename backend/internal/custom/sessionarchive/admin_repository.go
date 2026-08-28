package sessionarchive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"

	"github.com/lib/pq"
)

func (r *Repository) ListSessions(ctx context.Context, filter SessionFilter, page, pageSize int) (SessionPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	where, args := buildSessionWhere(filter)
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_archive_sessions s "+where, args...).Scan(&total); err != nil {
		return SessionPage{}, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	query := "SELECT s.id,s.user_id,COALESCE(u.username,''),COALESCE(u.email,''),s.api_key_id,COALESCE(k.name,''),s.group_id,COALESCE(g.name,''),s.protocol,s.client,s.first_model,s.last_model,s.status,s.capture_coverage,s.merge_method,(SELECT COUNT(*) FROM session_archive_turns t WHERE t.session_id=s.id),(SELECT COUNT(*) FROM session_archive_requests r JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=s.id),EXISTS(SELECT 1 FROM session_archive_requests r JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=s.id AND r.has_truncated),s.created_at,s.last_active_at,s.expires_at FROM session_archive_sessions s LEFT JOIN users u ON u.id=s.user_id LEFT JOIN api_keys k ON k.id=s.api_key_id LEFT JOIN groups g ON g.id=s.group_id " + where + fmt.Sprintf(" ORDER BY s.last_active_at DESC,s.id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return SessionPage{}, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]SessionSummary, 0, pageSize)
	for rows.Next() {
		var item SessionSummary
		var userID, apiKeyID, groupID sql.NullInt64
		if err := rows.Scan(&item.ID, &userID, &item.Username, &item.UserEmail, &apiKeyID, &item.APIKeyName, &groupID, &item.GroupName, &item.Protocol, &item.Client, &item.FirstModel, &item.LastModel, &item.Status, &item.CaptureCoverage, &item.MergeMethod, &item.TurnCount, &item.RequestCount, &item.HasTruncated, &item.CreatedAt, &item.LastActivityAt, &item.ExpiresAt); err != nil {
			return SessionPage{}, err
		}
		item.UserID, item.APIKeyID, item.GroupID = nullInt64Pointer(userID), nullInt64Pointer(apiKeyID), nullInt64Pointer(groupID)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SessionPage{}, err
	}
	return SessionPage{Items: items, Total: total, Page: page, PageSize: pageSize, Pages: int64(math.Ceil(float64(total) / float64(pageSize)))}, nil
}

func buildSessionWhere(filter SessionFilter) (string, []any) {
	clauses := []string{"s.status <> 'deleting'"}
	args := make([]any, 0, 10)
	add := func(format string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(format, len(args)))
	}
	if filter.TenantID != 0 {
		add("s.tenant_id=$%d", filter.TenantID)
	}
	if filter.UserID != 0 {
		add("s.user_id=$%d", filter.UserID)
	}
	if filter.APIKeyID != 0 {
		add("s.api_key_id=$%d", filter.APIKeyID)
	}
	if filter.GroupID != 0 {
		add("s.group_id=$%d", filter.GroupID)
	}
	if filter.Model != "" {
		add("s.last_model=$%d", filter.Model)
	}
	if filter.Client != "" {
		add("s.client=$%d", filter.Client)
	}
	if filter.Status != "" {
		add("s.status=$%d", filter.Status)
	}
	if filter.Protocol != "" {
		add("s.protocol=$%d", filter.Protocol)
	}
	if !filter.From.IsZero() {
		add("s.last_active_at >= $%d", filter.From)
	}
	if !filter.To.IsZero() {
		add("s.last_active_at <= $%d", filter.To)
	}
	if filter.CorrelationRequestID != "" {
		add("EXISTS(SELECT 1 FROM session_archive_requests ar JOIN session_archive_turns at ON at.id=ar.turn_id WHERE at.session_id=s.id AND ar.correlation_request_id=$%d)", filter.CorrelationRequestID)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

type SessionRecord struct {
	Summary              SessionSummary
	StableIdentifierHash string
	PolicySnapshot       json.RawMessage
}

func (r *Repository) GetSession(ctx context.Context, id int64) (SessionRecord, error) {
	page, err := r.ListSessions(ctx, SessionFilter{}, 1, 100)
	if err != nil {
		return SessionRecord{}, err
	}
	var summary SessionSummary
	for _, item := range page.Items {
		if item.ID == id {
			summary = item
			break
		}
	}
	if summary.ID == 0 {
		// Avoid loading all pages in the common direct-ID path.
		var record SessionRecord
		var userID, apiKeyID, groupID sql.NullInt64
		err := r.db.QueryRowContext(ctx, "SELECT s.id,s.user_id,COALESCE(u.username,''),COALESCE(u.email,''),s.api_key_id,COALESCE(k.name,''),s.group_id,COALESCE(g.name,''),s.protocol,s.client,s.first_model,s.last_model,s.status,s.capture_coverage,s.merge_method,(SELECT COUNT(*) FROM session_archive_turns t WHERE t.session_id=s.id),(SELECT COUNT(*) FROM session_archive_requests ar JOIN session_archive_turns t ON t.id=ar.turn_id WHERE t.session_id=s.id),EXISTS(SELECT 1 FROM session_archive_requests ar JOIN session_archive_turns t ON t.id=ar.turn_id WHERE t.session_id=s.id AND ar.has_truncated),s.created_at,s.last_active_at,s.expires_at,s.stable_id_digest,s.policy_snapshot FROM session_archive_sessions s LEFT JOIN users u ON u.id=s.user_id LEFT JOIN api_keys k ON k.id=s.api_key_id LEFT JOIN groups g ON g.id=s.group_id WHERE s.id=$1 AND s.status<>'deleting'", id).Scan(&record.Summary.ID, &userID, &record.Summary.Username, &record.Summary.UserEmail, &apiKeyID, &record.Summary.APIKeyName, &groupID, &record.Summary.GroupName, &record.Summary.Protocol, &record.Summary.Client, &record.Summary.FirstModel, &record.Summary.LastModel, &record.Summary.Status, &record.Summary.CaptureCoverage, &record.Summary.MergeMethod, &record.Summary.TurnCount, &record.Summary.RequestCount, &record.Summary.HasTruncated, &record.Summary.CreatedAt, &record.Summary.LastActivityAt, &record.Summary.ExpiresAt, &record.StableIdentifierHash, &record.PolicySnapshot)
		record.Summary.UserID, record.Summary.APIKeyID, record.Summary.GroupID = nullInt64Pointer(userID), nullInt64Pointer(apiKeyID), nullInt64Pointer(groupID)
		return record, err
	}
	var record SessionRecord
	record.Summary = summary
	err = r.db.QueryRowContext(ctx, "SELECT stable_id_digest,policy_snapshot FROM session_archive_sessions WHERE id=$1 AND status<>'deleting'", id).Scan(&record.StableIdentifierHash, &record.PolicySnapshot)
	return record, err
}

func (r *Repository) SessionTimeline(ctx context.Context, sessionID int64) ([]Turn, []Request, []Attempt, []BlobRef, error) {
	turnRows, err := r.db.QueryContext(ctx, "SELECT id,session_id,sequence_no,protocol_turn_id,message_chain_digest,status,started_at,completed_at FROM session_archive_turns WHERE session_id=$1 ORDER BY sequence_no,id", sessionID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var turns []Turn
	for turnRows.Next() {
		var turn Turn
		if err := turnRows.Scan(&turn.ID, &turn.SessionID, &turn.SequenceNo, &turn.ProtocolTurnID, &turn.MessageChainDigest, &turn.Status, &turn.StartedAt, &turn.CompletedAt); err != nil {
			_ = turnRows.Close()
			return nil, nil, nil, nil, err
		}
		turns = append(turns, turn)
	}
	if err := turnRows.Err(); err != nil {
		_ = turnRows.Close()
		return nil, nil, nil, nil, err
	}
	_ = turnRows.Close()
	requestRows, err := r.db.QueryContext(ctx, "SELECT r.id,r.turn_id,r.correlation_request_id,r.billing_request_id,r.client_request_id,r.upstream_request_id,r.endpoint,r.model,r.status,r.error_class,r.error_code,r.client_disconnected,r.has_truncated,r.metadata,r.started_at,r.completed_at FROM session_archive_requests r JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=$1 ORDER BY t.sequence_no,r.started_at,r.id", sessionID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var requests []Request
	for requestRows.Next() {
		var request Request
		var metadata []byte
		if err := requestRows.Scan(&request.ID, &request.TurnID, &request.CorrelationRequestID, &request.BillingRequestID, &request.ClientRequestID, &request.UpstreamRequestID, &request.Endpoint, &request.Model, &request.Status, &request.ErrorClass, &request.ErrorCode, &request.ClientDisconnected, &request.HasTruncated, &metadata, &request.StartedAt, &request.CompletedAt); err != nil {
			_ = requestRows.Close()
			return nil, nil, nil, nil, err
		}
		_ = json.Unmarshal(metadata, &request.Metadata)
		requests = append(requests, request)
	}
	if err := requestRows.Err(); err != nil {
		_ = requestRows.Close()
		return nil, nil, nil, nil, err
	}
	_ = requestRows.Close()
	attemptRows, err := r.db.QueryContext(ctx, "SELECT a.id,a.request_id,a.attempt_no,COALESCE(a.account_id,0),a.transform_type,a.upstream_request_id,COALESCE(a.upstream_status,0),a.status,a.error_class,a.error_code,a.duration_ms,a.is_final,a.started_at,a.completed_at FROM session_archive_attempts a JOIN session_archive_requests r ON r.id=a.request_id JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=$1 ORDER BY a.request_id,a.attempt_no", sessionID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var attempts []Attempt
	for attemptRows.Next() {
		var attempt Attempt
		if err := attemptRows.Scan(&attempt.ID, &attempt.RequestID, &attempt.AttemptNo, &attempt.AccountID, &attempt.TransformType, &attempt.UpstreamRequestID, &attempt.UpstreamStatus, &attempt.Status, &attempt.ErrorClass, &attempt.ErrorCode, &attempt.DurationMS, &attempt.Final, &attempt.StartedAt, &attempt.CompletedAt); err != nil {
			_ = attemptRows.Close()
			return nil, nil, nil, nil, err
		}
		attempts = append(attempts, attempt)
	}
	if err := attemptRows.Err(); err != nil {
		_ = attemptRows.Close()
		return nil, nil, nil, nil, err
	}
	_ = attemptRows.Close()
	refRows, err := r.db.QueryContext(ctx, "SELECT br.id,br.owner_type,br.owner_id,br.purpose,br.direction,br.content_type,br.observed_sha256,br.observed_bytes,br.stored_bytes,br.truncated,br.dropped_reason,br.sequence_no,br.occurred_at,(b.status='ready'),COALESCE(b.storage_backend,'') FROM session_archive_blob_refs br LEFT JOIN session_archive_blobs b ON b.id=br.blob_id WHERE (br.owner_type='request' AND br.owner_id IN (SELECT r.id FROM session_archive_requests r JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=$1)) OR (br.owner_type='attempt' AND br.owner_id IN (SELECT a.id FROM session_archive_attempts a JOIN session_archive_requests r ON r.id=a.request_id JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=$1)) ORDER BY br.occurred_at,br.owner_type,br.owner_id,br.sequence_no,br.id", sessionID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var refs []BlobRef
	for refRows.Next() {
		var ref BlobRef
		var available sql.NullBool
		if err := refRows.Scan(&ref.ID, &ref.OwnerType, &ref.OwnerID, &ref.Purpose, &ref.Direction, &ref.ContentType, &ref.ObservedSHA256, &ref.ObservedBytes, &ref.StoredBytes, &ref.Truncated, &ref.DroppedReason, &ref.SequenceNo, &ref.OccurredAt, &available, &ref.StorageBackend); err != nil {
			_ = refRows.Close()
			return nil, nil, nil, nil, err
		}
		ref.Available = available.Valid && available.Bool
		refs = append(refs, ref)
	}
	if err := refRows.Err(); err != nil {
		_ = refRows.Close()
		return nil, nil, nil, nil, err
	}
	_ = refRows.Close()
	return turns, requests, attempts, refs, nil
}

type ContentRecord struct {
	Ref            BlobRef
	BlobID         *int64
	StorageBackend string
	ObjectKey      string
	Encoding       EncodingInfo
}

// sessionReadLease 把 Session 行的共享锁保持到敏感正文响应或单 Session 导出结束。
// 删除事务更新 Session 状态时会等待该锁，避免正文已经定位但删除先提交的 TOCTOU。
type sessionReadLease struct {
	tx   *sql.Tx
	once sync.Once
}

func (l *sessionReadLease) Release() {
	if l == nil || l.tx == nil {
		return
	}
	l.once.Do(func() { _ = l.tx.Rollback() })
}

func (r *Repository) AcquireSessionReadLease(ctx context.Context, sessionID int64) (*sessionReadLease, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	lease := &sessionReadLease{tx: tx}
	var lockedID int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM session_archive_sessions WHERE id=$1 AND status<>'deleting' FOR SHARE", sessionID).Scan(&lockedID); err != nil {
		lease.Release()
		return nil, err
	}
	return lease, nil
}

func (r *Repository) AcquireRequestReadLease(ctx context.Context, requestID int64) (*sessionReadLease, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	lease := &sessionReadLease{tx: tx}
	var sessionID int64
	query := "SELECT s.id FROM session_archive_sessions s JOIN session_archive_turns t ON t.session_id=s.id JOIN session_archive_requests r ON r.turn_id=t.id WHERE r.id=$1 AND s.status<>'deleting' FOR SHARE OF s"
	if err := tx.QueryRowContext(ctx, query, requestID).Scan(&sessionID); err != nil {
		lease.Release()
		return nil, err
	}
	return lease, nil
}

func (r *Repository) RequestContents(ctx context.Context, requestID int64, kind string) ([]ContentRecord, error) {
	purpose := map[string]BlobPurpose{"request": PurposeRawRequest, "response": PurposeResponse, "tool": PurposeTool, "raw": PurposeErrorBody, "upstream": PurposeUpstreamRequest, "attachment": PurposeAttachment}[kind]
	if purpose == "" {
		return nil, errors.New("invalid content kind")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID int64
	err = tx.QueryRowContext(ctx, "SELECT s.id FROM session_archive_sessions s JOIN session_archive_turns t ON t.session_id=s.id JOIN session_archive_requests r ON r.turn_id=t.id WHERE r.id=$1 AND s.status<>'deleting' FOR SHARE OF s", requestID).Scan(&sessionID)
	if err != nil {
		return nil, err
	}
	query := "SELECT br.id,br.owner_type,br.owner_id,br.purpose,br.direction,br.content_type,br.observed_sha256,br.observed_bytes,br.stored_bytes,br.truncated,br.dropped_reason,br.sequence_no,br.occurred_at,br.blob_id,b.storage_backend,b.stored_plaintext_sha256,b.stored_bytes,b.compressed_bytes,b.ciphertext_bytes,b.gzip_version,b.format_version,b.key_id,b.object_key FROM session_archive_blob_refs br LEFT JOIN session_archive_blobs b ON b.id=br.blob_id AND b.status='ready' WHERE br.owner_type='request' AND br.owner_id=$1 AND br.purpose=$2 ORDER BY br.occurred_at,br.sequence_no,br.id"
	if purpose == PurposeUpstreamRequest {
		query = "SELECT br.id,br.owner_type,br.owner_id,br.purpose,br.direction,br.content_type,br.observed_sha256,br.observed_bytes,br.stored_bytes,br.truncated,br.dropped_reason,br.sequence_no,br.occurred_at,br.blob_id,b.storage_backend,b.stored_plaintext_sha256,b.stored_bytes,b.compressed_bytes,b.ciphertext_bytes,b.gzip_version,b.format_version,b.key_id,b.object_key FROM session_archive_blob_refs br JOIN session_archive_attempts a ON a.id=br.owner_id AND br.owner_type='attempt' LEFT JOIN session_archive_blobs b ON b.id=br.blob_id AND b.status='ready' WHERE a.request_id=$1 AND br.purpose=$2 ORDER BY br.occurred_at,a.attempt_no,br.sequence_no,br.id"
	}
	rows, err := tx.QueryContext(ctx, query, requestID, purpose)
	if err != nil {
		return nil, err
	}
	records := make([]ContentRecord, 0, 4)
	for rows.Next() {
		var record ContentRecord
		var blobID sql.NullInt64
		var backend, hash, keyID, objectKey sql.NullString
		var stored, compressed, ciphertext sql.NullInt64
		var gzipVersion, formatVersion sql.NullInt64
		if err := rows.Scan(&record.Ref.ID, &record.Ref.OwnerType, &record.Ref.OwnerID, &record.Ref.Purpose, &record.Ref.Direction, &record.Ref.ContentType, &record.Ref.ObservedSHA256, &record.Ref.ObservedBytes, &record.Ref.StoredBytes, &record.Ref.Truncated, &record.Ref.DroppedReason, &record.Ref.SequenceNo, &record.Ref.OccurredAt, &blobID, &backend, &hash, &stored, &compressed, &ciphertext, &gzipVersion, &formatVersion, &keyID, &objectKey); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if blobID.Valid && objectKey.Valid {
			id := blobID.Int64
			record.BlobID, record.StorageBackend, record.ObjectKey, record.Ref.Available = &id, backend.String, objectKey.String, true
			record.Ref.StorageBackend = backend.String
			record.Encoding = EncodingInfo{StoredPlaintextSHA256: hash.String, StoredBytes: stored.Int64, CompressedBytes: compressed.Int64, CiphertextBytes: ciphertext.Int64, GZIPVersion: int(gzipVersion.Int64), FormatVersion: int(formatVersion.Int64), KeyID: keyID.String}
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return records, nil
}

func (r *Repository) RequestContent(ctx context.Context, requestID int64, kind string) (ContentRecord, error) {
	records, err := r.RequestContents(ctx, requestID, kind)
	if err != nil {
		return ContentRecord{}, err
	}
	return records[0], nil
}

func (r *Repository) ResolveSessionIDs(ctx context.Context, filter SessionFilter, max int) ([]int64, error) {
	if max < 1 || max > 100000 {
		max = 10000
	}
	where, args := buildSessionWhere(filter)
	args = append(args, max+1)
	rows, err := r.db.QueryContext(ctx, "SELECT s.id FROM session_archive_sessions s "+where+fmt.Sprintf(" ORDER BY s.id LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > max {
		return nil, fmt.Errorf("session selection exceeds maximum of %d", max)
	}
	return ids, nil
}

func (r *Repository) SessionStorageBackends(ctx context.Context, sessionIDs []int64) ([]string, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT b.storage_backend
		FROM session_archive_blob_refs br
		JOIN session_archive_blobs b ON b.id=br.blob_id
		WHERE (br.owner_type='session' AND br.owner_id=ANY($1))
		   OR (br.owner_type='turn' AND br.owner_id IN (SELECT id FROM session_archive_turns WHERE session_id=ANY($1)))
		   OR (br.owner_type='request' AND br.owner_id IN (SELECT r.id FROM session_archive_requests r JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=ANY($1)))
		   OR (br.owner_type='attempt' AND br.owner_id IN (SELECT a.id FROM session_archive_attempts a JOIN session_archive_requests r ON r.id=a.request_id JOIN session_archive_turns t ON t.id=r.turn_id WHERE t.session_id=ANY($1)))
		ORDER BY b.storage_backend`, pq.Array(sessionIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var backends []string
	for rows.Next() {
		var backend string
		if err := rows.Scan(&backend); err != nil {
			return nil, err
		}
		backends = append(backends, backend)
	}
	return backends, rows.Err()
}

func (s *Service) EnsureSessionBackendsAvailable(ctx context.Context, sessionIDs []int64) error {
	backends, err := s.repository.SessionStorageBackends(ctx, sessionIDs)
	if err != nil {
		return err
	}
	registry := s.currentRegistry()
	for _, backend := range backends {
		if _, err := registry.Resolve(backend); err != nil {
			return err
		}
	}
	return nil
}

type DecodedContent struct {
	Record  ContentRecord
	Content []byte
}

var errMultipleContentParts = errors.New("archive content has multiple parts")

func (s *Service) ReadContents(ctx context.Context, requestID int64, kind string) ([]DecodedContent, error) {
	if s == nil || !s.cfg.Enabled || s.repository == nil {
		return nil, errors.New("session archive disabled")
	}
	records, err := s.repository.RequestContents(ctx, requestID, kind)
	if err != nil {
		return nil, err
	}
	items := make([]DecodedContent, 0, len(records))
	for _, record := range records {
		item := DecodedContent{Record: record}
		if !record.Ref.Available {
			items = append(items, item)
			continue
		}
		var output bytes.Buffer
		if err := s.WriteContent(ctx, record, &output); err != nil {
			return nil, err
		}
		item.Content = output.Bytes()
		items = append(items, item)
	}
	return items, nil
}

// WriteContent 对单条 ready 引用做认证、解密、解压与 hash 校验后写入目标；
// Codec 内部使用临时文件验证完整性，不会向目标写出未经认证的部分明文。
func (s *Service) WriteContent(ctx context.Context, record ContentRecord, dst io.Writer) error {
	if s == nil || !s.cfg.Enabled || s.codec == nil {
		return errors.New("session archive disabled")
	}
	if !record.Ref.Available || record.BlobID == nil || record.ObjectKey == "" {
		return errors.New("archive content unavailable")
	}
	entry, err := s.currentRegistry().Resolve(record.StorageBackend)
	if err != nil {
		s.metrics.failure(err, true)
		return err
	}
	reader, err := entry.Store.Get(ctx, record.ObjectKey)
	if err != nil {
		s.metrics.failure(err, true)
		return err
	}
	decodeErr := s.codec.Decode(reader, dst, record.Encoding)
	closeErr := reader.Close()
	if decodeErr != nil {
		s.metrics.failure(decodeErr, true)
		return decodeErr
	}
	if closeErr != nil {
		s.metrics.failure(closeErr, true)
		return closeErr
	}
	return nil
}

// ReadSingleContent 供 SFT 等要求单一完整 JSON 正文的路径使用，避免先构造
// []DecodedContent 再聚合复制一遍大正文。多 ref 不做无分隔猜测性拼接。
func (s *Service) ReadSingleContent(ctx context.Context, requestID int64, kind string) (ContentRecord, []byte, error) {
	if s == nil || !s.cfg.Enabled || s.repository == nil {
		return ContentRecord{}, nil, errors.New("session archive disabled")
	}
	records, err := s.repository.RequestContents(ctx, requestID, kind)
	if err != nil {
		return ContentRecord{}, nil, err
	}
	if len(records) != 1 {
		return ContentRecord{}, nil, errMultipleContentParts
	}
	record := records[0]
	if !record.Ref.Available {
		return record, nil, nil
	}
	var output bytes.Buffer
	if record.Ref.StoredBytes > 0 && uint64(record.Ref.StoredBytes) <= uint64(^uint(0)>>1) {
		output.Grow(int(record.Ref.StoredBytes))
	}
	if err := s.WriteContent(ctx, record, &output); err != nil {
		return ContentRecord{}, nil, err
	}
	return record, output.Bytes(), nil
}
