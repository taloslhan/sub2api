package sessionarchive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type RequiredAuditFunc func(context.Context, string, string, map[string]any) error
type AdminIDFunc func(*gin.Context) (int64, bool)
type DownloadLimiterFunc func(*gin.Context) bool

type HandlerOptions struct {
	Service           *Service
	Tickets           TicketStore
	RequiredAudit     RequiredAuditFunc
	MarkRequiredAudit func(*gin.Context)
	AdminID           AdminIDFunc
	DownloadLimiter   DownloadLimiterFunc
	TicketTTL         time.Duration
	DownloadBasePath  string
}

type Handler struct {
	service           *Service
	tickets           TicketStore
	requiredAudit     RequiredAuditFunc
	markRequiredAudit func(*gin.Context)
	adminID           AdminIDFunc
	downloadLimiter   DownloadLimiterFunc
	ticketTTL         time.Duration
	downloadBasePath  string
}

func NewHandler(opts HandlerOptions) (*Handler, error) {
	if opts.Service == nil {
		return nil, errors.New("session archive handler requires service")
	}
	if opts.TicketTTL <= 0 {
		opts.TicketTTL = 2 * time.Minute
	}
	if opts.DownloadBasePath == "" {
		opts.DownloadBasePath = "/api/v1/session-archive/download/"
	}
	return &Handler{service: opts.Service, tickets: opts.Tickets, requiredAudit: opts.RequiredAudit, markRequiredAudit: opts.MarkRequiredAudit, adminID: opts.AdminID, downloadLimiter: opts.DownloadLimiter, ticketTTL: opts.TicketTTL, downloadBasePath: opts.DownloadBasePath}, nil
}

func (h *Handler) Runtime(c *gin.Context) {
	status := h.service.Status()
	c.JSON(http.StatusOK, gin.H{
		"enabled": status.Enabled, "process_status": status.ProcessStatus,
		"storage_status": status.StorageStatus, "database_status": status.DatabaseStatus,
		"active_key_id": status.ActiveKeyID, "bucket": status.Bucket, "prefix": status.Prefix,
		"queue_events": status.QueueEvents, "queue_event_capacity": status.QueueEventCapacity,
		"queue_bytes": status.QueueBytes, "queue_byte_capacity": status.QueueByteCapacity,
		"enqueued_total": status.EnqueuedTotal, "dropped_total": status.DroppedTotal,
		"truncated_total": status.TruncatedTotal, "stored_total": status.StoredTotal,
		"failed_total": status.FailedTotal, "storage_failures": status.StorageFailures,
		"export_failures": status.ExportFailures, "pending_backlog": status.PendingBacklog,
		"gc_backlog": status.GCBacklog, "last_error": status.LastError, "last_success_at": nullableTime(status.LastSuccessAt),
	})
}

func (h *Handler) ListSessions(c *gin.Context) {
	filter, err := sessionFilterFromRequest(c)
	if err != nil {
		writeHandlerError(c, http.StatusBadRequest, err)
		return
	}
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 20)
	result, err := h.service.repository.ListSessions(c.Request.Context(), filter, page, pageSize)
	if err != nil {
		writeHandlerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) GetSession(c *gin.Context) {
	id, err := pathID(c)
	if err != nil {
		writeHandlerError(c, http.StatusBadRequest, err)
		return
	}
	record, err := h.service.repository.GetSession(c.Request.Context(), id)
	if err != nil {
		writeRepositoryError(c, err)
		return
	}
	turns, requests, attempts, refs, err := h.service.repository.SessionTimeline(c.Request.Context(), id)
	if err != nil {
		writeRepositoryError(c, err)
		return
	}
	turnPayload := make([]gin.H, 0, len(turns))
	for _, turn := range turns {
		requestPayload := make([]gin.H, 0)
		requestSequence := 0
		for _, request := range requests {
			if request.TurnID != turn.ID {
				continue
			}
			requestSequence++
			attemptPayload := make([]gin.H, 0)
			for _, attempt := range attempts {
				if attempt.RequestID != request.ID {
					continue
				}
				attemptPayload = append(attemptPayload, gin.H{
					"id": attempt.ID, "sequence": attempt.AttemptNo, "account_id": nullableID(attempt.AccountID),
					"transform_type": attempt.TransformType, "upstream_status": attempt.Status,
					"upstream_status_code": nullableInt(attempt.UpstreamStatus), "error_category": attempt.ErrorClass,
					"latency_ms": attempt.DurationMS, "final": attempt.Final,
					"created_at": attempt.StartedAt, "completed_at": attempt.CompletedAt,
				})
			}
			available := make([]string, 0, 4)
			for _, ref := range refs {
				belongsToRequest := ref.OwnerType == "request" && ref.OwnerID == request.ID
				if ref.OwnerType == "attempt" {
					for _, attempt := range attempts {
						if attempt.ID == ref.OwnerID && attempt.RequestID == request.ID {
							belongsToRequest = true
							break
						}
					}
				}
				if belongsToRequest && ref.Available {
					if kind := contentKind(ref.Purpose); kind != "" && !containsString(available, kind) {
						available = append(available, kind)
					}
				}
			}
			requestPayload = append(requestPayload, gin.H{
				"id": request.ID, "sequence": requestSequence,
				"correlation_request_id": request.CorrelationRequestID, "billing_request_id": request.BillingRequestID,
				"upstream_request_id": request.UpstreamRequestID, "endpoint": request.Endpoint, "model": request.Model,
				"status": request.Status, "error_category": request.ErrorClass,
				"client_disconnected": request.ClientDisconnected, "has_truncated": request.HasTruncated,
				"available_content": available, "attempts": attemptPayload,
				"created_at": request.StartedAt, "completed_at": request.CompletedAt,
			})
		}
		turnPayload = append(turnPayload, gin.H{
			"id": turn.ID, "sequence": turn.SequenceNo, "protocol_turn_id": turn.ProtocolTurnID,
			"status": turn.Status, "started_at": turn.StartedAt, "completed_at": turn.CompletedAt,
			"requests": requestPayload,
		})
	}
	payload := summaryPayload(record.Summary)
	payload["stable_identifier_digest"] = record.StableIdentifierHash
	payload["policy_snapshot"] = policySnapshotPayload(record.PolicySnapshot)
	payload["turns"] = turnPayload
	c.JSON(http.StatusOK, payload)
}

func (h *Handler) GetRequestContent(c *gin.Context) {
	requestID, err := pathID(c)
	if err != nil {
		writeHandlerError(c, http.StatusBadRequest, err)
		return
	}
	kind := c.Query("kind")
	lease, err := h.service.repository.AcquireRequestReadLease(c.Request.Context(), requestID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeHandlerError(c, http.StatusNotFound, err)
		} else {
			writeHandlerError(c, http.StatusServiceUnavailable, err)
		}
		return
	}
	defer lease.Release()
	items, err := h.service.ReadContents(c.Request.Context(), requestID, kind)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeHandlerError(c, http.StatusServiceUnavailable, err)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeHandlerError(c, http.StatusNotFound, err)
		return
	}
	parts := make([]gin.H, 0, len(items))
	observedBytes, storedBytes := int64(0), int64(0)
	truncated, available := false, true
	contentType, direction := items[0].Record.Ref.ContentType, items[0].Record.Ref.Direction
	occurredAt := items[0].Record.Ref.OccurredAt
	droppedReasons := make([]string, 0, 1)
	for _, item := range items {
		parts = append(parts, contentPartPayload(item.Record, item.Content))
		observedBytes += item.Record.Ref.ObservedBytes
		storedBytes += item.Record.Ref.StoredBytes
		truncated = truncated || item.Record.Ref.Truncated
		available = available && item.Record.Ref.Available
		if contentType != item.Record.Ref.ContentType {
			contentType = "application/octet-stream"
		}
		if direction != item.Record.Ref.Direction {
			direction = "mixed"
		}
		if item.Record.Ref.DroppedReason != "" && !containsString(droppedReasons, item.Record.Ref.DroppedReason) {
			droppedReasons = append(droppedReasons, item.Record.Ref.DroppedReason)
		}
	}
	if err := h.audit(c, "session_archive.content.read", fmt.Sprintf("request:%d:%s", requestID, kind), map[string]any{"request_id": requestID, "kind": kind, "stored_bytes": storedBytes, "part_count": len(parts)}); err != nil {
		writeHandlerError(c, http.StatusServiceUnavailable, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Pragma", "no-cache")
	payload := gin.H{
		"request_id": requestID, "kind": kind, "content_type": contentType,
		"observed_bytes": observedBytes, "stored_bytes": storedBytes,
		"truncated": truncated, "dropped_reason": strings.Join(droppedReasons, ","),
		"available": available, "frame_count": len(parts), "parts": parts,
		"direction": direction, "occurred_at": occurredAt,
	}
	c.JSON(http.StatusOK, payload)
}

func contentPartPayload(record ContentRecord, content []byte) gin.H {
	payload := gin.H{
		"ref_id": record.Ref.ID, "owner_type": record.Ref.OwnerType, "owner_id": record.Ref.OwnerID,
		"sequence_no": record.Ref.SequenceNo, "direction": record.Ref.Direction,
		"occurred_at": record.Ref.OccurredAt, "content_type": record.Ref.ContentType,
		"observed_bytes": record.Ref.ObservedBytes, "stored_bytes": record.Ref.StoredBytes,
		"truncated": record.Ref.Truncated, "dropped_reason": record.Ref.DroppedReason,
		"available": record.Ref.Available,
	}
	if record.Ref.Available {
		encoded, encoding := encodeContentForTransport(record.Ref.ContentType, content)
		if encoding == "base64" {
			payload["encoding"] = encoding
			payload["base64"] = encoded
		} else if json.Valid(content) {
			var value any
			_ = json.Unmarshal(content, &value)
			payload["value"] = value
		} else {
			payload["text"] = string(content)
		}
	}
	return payload
}

func (h *Handler) ListPolicies(c *gin.Context) {
	policies, err := h.service.repository.ListPolicies(c.Request.Context())
	if err != nil {
		writeHandlerError(c, http.StatusInternalServerError, err)
		return
	}
	items := make([]gin.H, 0, len(policies))
	for _, policy := range policies {
		items = append(items, policyPayload(policy))
	}
	effective := ResolvePolicy(PolicyIdentity{}, policies, DefaultResolvedPolicy(h.service.cfg.PayloadMaxBytes, h.service.cfg.DefaultRetentionDays))
	c.JSON(http.StatusOK, gin.H{"items": items, "effective_global": resolvedPolicyPayload(effective)})
}

type policyRequest struct {
	ScopeType                 PolicyScope `json:"scope_type"`
	ScopeID                   *int64      `json:"scope_id"`
	State                     PolicyState `json:"state"`
	CaptureRequest            bool        `json:"capture_request"`
	CaptureResponse           bool        `json:"capture_response"`
	CaptureTransformedRequest bool        `json:"capture_transformed_request"`
	CaptureTools              bool        `json:"capture_tools"`
	CaptureAttachments        bool        `json:"capture_attachments"`
	BodyLimitBytes            int64       `json:"body_limit_bytes"`
	RetentionDays             int         `json:"retention_days"`
}

func (h *Handler) UpsertPolicy(c *gin.Context) {
	var request policyRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeHandlerError(c, http.StatusBadRequest, err)
		return
	}
	scopeID := int64(0)
	if request.ScopeID != nil {
		scopeID = *request.ScopeID
	}
	if request.ScopeType == ScopeGlobal {
		scopeID = 0
	}
	policy := Policy{ScopeType: request.ScopeType, ScopeID: scopeID, State: request.State, CaptureRawRequest: request.CaptureRequest, CaptureUpstreamRequest: request.CaptureTransformedRequest, CaptureResponse: request.CaptureResponse, CaptureTools: request.CaptureTools, CaptureAttachments: request.CaptureAttachments, PayloadMaxBytes: request.BodyLimitBytes, RetentionDays: request.RetentionDays}
	if err := ValidatePolicy(policy, h.service.cfg.PayloadMaxBytes); err != nil {
		writeHandlerError(c, http.StatusBadRequest, err)
		return
	}
	if policy.State == PolicyOn && (!h.service.cfg.Enabled || !h.service.started.Load()) {
		writeHandlerError(c, http.StatusConflict, errors.New("session archive storage is not enabled and self-checked"))
		return
	}
	if err := h.audit(c, "session_archive.policy.update", fmt.Sprintf("%s:%d", policy.ScopeType, policy.ScopeID), map[string]any{"scope_type": policy.ScopeType, "scope_id": policy.ScopeID, "state": policy.State}); err != nil {
		writeHandlerError(c, http.StatusServiceUnavailable, err)
		return
	}
	saved, err := h.service.repository.UpsertPolicy(c.Request.Context(), policy)
	if err != nil {
		writeHandlerError(c, http.StatusInternalServerError, err)
		return
	}
	h.service.InvalidatePolicyCache()
	c.JSON(http.StatusOK, policyPayload(saved))
}

func (h *Handler) DeletePolicy(c *gin.Context) {
	scope := PolicyScope(c.Query("scope_type"))
	scopeID, _ := strconv.ParseInt(c.Query("scope_id"), 10, 64)
	if scope == ScopeGlobal {
		scopeID = 0
	}
	if err := h.audit(c, "session_archive.policy.delete", fmt.Sprintf("%s:%d", scope, scopeID), map[string]any{"scope_type": scope, "scope_id": scopeID}); err != nil {
		writeHandlerError(c, http.StatusServiceUnavailable, err)
		return
	}
	if err := h.service.repository.DeletePolicy(c.Request.Context(), scope, scopeID); err != nil {
		writeHandlerError(c, http.StatusInternalServerError, err)
		return
	}
	h.service.InvalidatePolicyCache()
	c.Status(http.StatusNoContent)
}

type exportRequest struct {
	Format    string         `json:"format"`
	SessionID *int64         `json:"session_id"`
	Filter    *SessionFilter `json:"filter"`
}

type exportPreflightResult struct {
	MatchedSessions int            `json:"matched_sessions"`
	EligibleSamples int            `json:"eligible_samples"`
	SkippedSamples  int            `json:"skipped_samples"`
	SkippedReasons  map[string]int `json:"skipped_reasons"`
}

type requestContentReader func(context.Context, int64, string) (ContentRecord, []byte, error)

func (h *Handler) ExportPreflight(c *gin.Context) {
	request, ids, ok := h.parseExportRequest(c)
	if !ok {
		return
	}
	result, err := h.preflightExport(c.Request.Context(), request.Format, ids)
	if err != nil {
		writeHandlerError(c, http.StatusServiceUnavailable, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"format": request.Format, "matched_sessions": result.MatchedSessions, "eligible_samples": result.EligibleSamples, "skipped_samples": result.SkippedSamples, "skipped_reasons": result.SkippedReasons})
}

func (h *Handler) IssueExportTicket(c *gin.Context) {
	if h.tickets == nil {
		writeHandlerError(c, http.StatusServiceUnavailable, errors.New("export ticket store unavailable"))
		return
	}
	request, ids, ok := h.parseExportRequest(c)
	if !ok {
		return
	}
	adminID, ok := h.currentAdminID(c)
	if !ok {
		writeHandlerError(c, http.StatusUnauthorized, errors.New("human administrator identity required"))
		return
	}
	preflight, err := h.preflightExport(c.Request.Context(), request.Format, ids)
	if err != nil {
		writeHandlerError(c, http.StatusServiceUnavailable, err)
		return
	}
	filter := SessionFilter{}
	if request.Filter != nil {
		filter = *request.Filter
	}
	ticket := ExportTicket{ID: uuid.NewString(), AdminID: adminID, Format: request.Format, SessionIDs: ids, Filter: filter, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(h.ticketTTL)}
	if err := h.audit(c, "session_archive.export.issue", fmt.Sprintf("admin:%d", adminID), map[string]any{"format": ticket.Format, "matched_sessions": len(ids)}); err != nil {
		writeHandlerError(c, http.StatusServiceUnavailable, err)
		return
	}
	if err := h.tickets.Put(c.Request.Context(), ticket, h.ticketTTL); err != nil {
		writeHandlerError(c, http.StatusServiceUnavailable, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": ticket.ID, "expires_at": ticket.ExpiresAt, "download_url": h.downloadBasePath + ticket.ID, "matched_sessions": preflight.MatchedSessions, "eligible_samples": preflight.EligibleSamples, "skipped_samples": preflight.SkippedSamples, "skipped_reasons": preflight.SkippedReasons})
}

func (h *Handler) preflightExport(ctx context.Context, format string, sessionIDs []int64) (exportPreflightResult, error) {
	result := exportPreflightResult{MatchedSessions: len(sessionIDs), SkippedReasons: make(map[string]int)}
	if format == "archive" {
		result.EligibleSamples = len(sessionIDs)
		return result, nil
	}
	for _, sessionID := range sessionIDs {
		_, requests, _, _, err := h.service.repository.SessionTimeline(ctx, sessionID)
		if err != nil {
			return exportPreflightResult{}, fmt.Errorf("preflight session %d: %w", sessionID, err)
		}
		tallySFTRequests(ctx, requests, h.service.ReadSingleContent, &result)
	}
	return result, nil
}

func tallySFTRequests(ctx context.Context, requests []Request, read requestContentReader, result *exportPreflightResult) {
	for _, request := range requests {
		_, reason := readSFTSample(ctx, request.ID, read)
		if reason == "" {
			result.EligibleSamples++
			continue
		}
		result.SkippedSamples++
		result.SkippedReasons[reason]++
	}
}

type sftSample struct {
	Messages []any `json:"messages"`
	Tools    []any `json:"tools,omitempty"`
}

func readSFTSample(ctx context.Context, requestID int64, read requestContentReader) (sftSample, string) {
	requestRecord, requestBody, requestErr := read(ctx, requestID, "request")
	responseRecord, responseBody, responseErr := read(ctx, requestID, "response")
	for _, item := range []struct {
		kind   string
		record ContentRecord
		body   []byte
		err    error
	}{
		{kind: "request", record: requestRecord, body: requestBody, err: requestErr},
		{kind: "response", record: responseRecord, body: responseBody, err: responseErr},
	} {
		if item.err != nil {
			if errors.Is(item.err, sql.ErrNoRows) {
				return sftSample{}, item.kind + "_unavailable"
			}
			if errors.Is(item.err, errMultipleContentParts) {
				return sftSample{}, item.kind + "_unsupported_parts"
			}
			return sftSample{}, item.kind + "_read_failed"
		}
		if !item.record.Ref.Available {
			return sftSample{}, item.kind + "_unavailable"
		}
		if item.record.Ref.Truncated {
			return sftSample{}, item.kind + "_truncated"
		}
		trimmed := bytes.TrimSpace(item.body)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || !json.Valid(trimmed) {
			return sftSample{}, item.kind + "_invalid_json"
		}
	}
	return normalizeSFTSample(requestBody, responseBody)
}

func (h *Handler) parseExportRequest(c *gin.Context) (exportRequest, []int64, bool) {
	var request exportRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeHandlerError(c, http.StatusBadRequest, err)
		return request, nil, false
	}
	if request.Format != "archive" && request.Format != "sft" {
		writeHandlerError(c, http.StatusBadRequest, errors.New("format must be archive or sft"))
		return request, nil, false
	}
	var ids []int64
	if request.SessionID != nil && *request.SessionID > 0 {
		ids = []int64{*request.SessionID}
	} else if request.Filter != nil {
		var err error
		ids, err = h.service.repository.ResolveSessionIDs(c.Request.Context(), *request.Filter, 100000)
		if err != nil {
			writeHandlerError(c, http.StatusInternalServerError, err)
			return request, nil, false
		}
	}
	if len(ids) == 0 {
		writeHandlerError(c, http.StatusBadRequest, errors.New("export matched no active sessions"))
		return request, nil, false
	}
	return request, ids, true
}

type deletionRequest struct {
	SessionIDs []int64        `json:"session_ids"`
	Filter     *SessionFilter `json:"filter"`
}

func (h *Handler) CreateDeletionJob(c *gin.Context) {
	var request deletionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeHandlerError(c, http.StatusBadRequest, err)
		return
	}
	filter := SessionFilter{}
	if request.Filter != nil {
		filter = *request.Filter
	}
	ids := request.SessionIDs
	if len(ids) == 0 && request.Filter != nil {
		var err error
		ids, err = h.service.repository.ResolveSessionIDs(c.Request.Context(), filter, 100000)
		if err != nil {
			writeHandlerError(c, http.StatusInternalServerError, err)
			return
		}
	}
	if len(ids) == 0 {
		writeHandlerError(c, http.StatusBadRequest, errors.New("deletion matched no active sessions"))
		return
	}
	adminID, ok := h.currentAdminID(c)
	if !ok {
		writeHandlerError(c, http.StatusUnauthorized, errors.New("human administrator identity required"))
		return
	}
	if err := h.audit(c, "session_archive.deletion.create", fmt.Sprintf("sessions:%d", len(ids)), map[string]any{"matched_sessions": len(ids)}); err != nil {
		writeHandlerError(c, http.StatusServiceUnavailable, err)
		return
	}
	job, err := h.service.repository.CreateDeletionJob(c.Request.Context(), adminID, ids, filter)
	if err != nil {
		writeHandlerError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusAccepted, deletionJobPayload(job))
}

func (h *Handler) ListDeletionJobs(c *gin.Context) {
	page, pageSize := queryInt(c, "page", 1), queryInt(c, "page_size", 20)
	jobs, total, err := h.service.repository.ListDeletionJobs(c.Request.Context(), page, pageSize)
	if err != nil {
		writeHandlerError(c, http.StatusInternalServerError, err)
		return
	}
	items := make([]gin.H, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, deletionJobPayload(job))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

func (h *Handler) GetDeletionJob(c *gin.Context) {
	id, err := pathID(c)
	if err != nil {
		writeHandlerError(c, http.StatusBadRequest, err)
		return
	}
	job, err := h.service.repository.GetDeletionJob(c.Request.Context(), id)
	if err != nil {
		writeRepositoryError(c, err)
		return
	}
	c.JSON(http.StatusOK, deletionJobPayload(job))
}

// Download 位于 admin 路由组外；ticket 是短时、单次 bearer capability。
func (h *Handler) Download(c *gin.Context) {
	if h.tickets == nil {
		writeHandlerError(c, http.StatusServiceUnavailable, errors.New("export ticket store unavailable"))
		return
	}
	if h.downloadLimiter == nil || !h.downloadLimiter(c) {
		writeHandlerError(c, http.StatusTooManyRequests, errors.New("archive download rate limit exceeded"))
		return
	}
	ticketID := c.Param("ticket")
	ticket, err := h.tickets.Consume(c.Request.Context(), ticketID)
	if err != nil {
		writeHandlerError(c, http.StatusUnauthorized, err)
		return
	}
	if h.requiredAudit == nil {
		writeHandlerError(c, http.StatusServiceUnavailable, errors.New("required audit unavailable"))
		return
	}
	if err := h.requiredAudit(c.Request.Context(), "session_archive.export.consume", fmt.Sprintf("admin:%d", ticket.AdminID), map[string]any{"format": ticket.Format, "matched_sessions": len(ticket.SessionIDs)}); err != nil {
		writeHandlerError(c, http.StatusServiceUnavailable, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"session-archive-%s.jsonl\"", ticket.Format))
	encoder := json.NewEncoder(c.Writer)
	if err := encoder.Encode(gin.H{"type": "manifest", "format": ticket.Format, "created_at": time.Now().UTC(), "session_count": len(ticket.SessionIDs)}); err != nil {
		return
	}
	for _, sessionID := range ticket.SessionIDs {
		lease, err := h.service.repository.AcquireSessionReadLease(c.Request.Context(), sessionID)
		if err != nil {
			if err := encoder.Encode(gin.H{"type": "skip", "session_id": sessionID, "reason": "session_unavailable"}); err != nil {
				return
			}
			continue
		}
		ok := h.writeExportSession(c.Request.Context(), c.Writer, encoder, ticket.Format, sessionID)
		lease.Release()
		if !ok {
			exportErr := errors.New("archive export stream failed")
			h.service.metrics.exportFailures.Add(1)
			h.service.metrics.failure(exportErr, true)
			_ = h.requiredAudit(c.Request.Context(), "session_archive.export.failed", fmt.Sprintf("admin:%d", ticket.AdminID), map[string]any{"format": ticket.Format, "matched_sessions": len(ticket.SessionIDs)})
			return
		}
	}
	_ = h.requiredAudit(c.Request.Context(), "session_archive.export.completed", fmt.Sprintf("admin:%d", ticket.AdminID), map[string]any{"format": ticket.Format, "matched_sessions": len(ticket.SessionIDs)})
}

func (h *Handler) writeExportSession(ctx context.Context, dst io.Writer, encoder *json.Encoder, format string, sessionID int64) bool {
	record, err := h.service.repository.GetSession(ctx, sessionID)
	if err != nil {
		return encoder.Encode(gin.H{"type": "skip", "session_id": sessionID, "reason": "session_unavailable"}) == nil
	}
	turns, requests, attempts, refs, err := h.service.repository.SessionTimeline(ctx, sessionID)
	if err != nil {
		return encoder.Encode(gin.H{"type": "skip", "session_id": sessionID, "reason": "timeline_unavailable"}) == nil
	}
	if format == "sft" {
		for _, request := range requests {
			sample, reason := readSFTSample(ctx, request.ID, h.service.ReadSingleContent)
			if reason != "" {
				continue
			}
			payload := gin.H{"session_id": sessionID, "request_id": request.ID, "messages": sample.Messages}
			if len(sample.Tools) > 0 {
				payload["tools"] = sample.Tools
			}
			if err := encoder.Encode(payload); err != nil {
				return false
			}
		}
		return true
	}
	if err := encoder.Encode(gin.H{"type": "session", "session": record.Summary, "turns": turns, "requests": requests, "attempts": attempts, "blob_refs": refs}); err != nil {
		return false
	}
	for _, request := range requests {
		for _, kind := range []string{"request", "upstream", "response", "tool", "attachment", "raw"} {
			records, readErr := h.service.repository.RequestContents(ctx, request.ID, kind)
			if errors.Is(readErr, sql.ErrNoRows) {
				continue
			}
			if readErr != nil {
				return false
			}
			for _, contentRecord := range records {
				if !h.writeArchiveContentLine(ctx, dst, encoder, request.ID, kind, contentRecord) {
					return false
				}
			}
		}
	}
	return true
}

func (h *Handler) writeArchiveContentLine(ctx context.Context, dst io.Writer, encoder *json.Encoder, requestID int64, kind string, record ContentRecord) bool {
	payload := gin.H{
		"type": "content", "request_id": requestID, "kind": kind,
		"owner_type": record.Ref.OwnerType, "owner_id": record.Ref.OwnerID,
		"sequence_no": record.Ref.SequenceNo, "direction": record.Ref.Direction,
		"occurred_at": record.Ref.OccurredAt, "content_type": record.Ref.ContentType,
		"observed_bytes": record.Ref.ObservedBytes, "stored_bytes": record.Ref.StoredBytes,
		"truncated": record.Ref.Truncated, "dropped_reason": record.Ref.DroppedReason,
		"available": record.Ref.Available,
	}
	if !record.Ref.Available {
		return encoder.Encode(payload) == nil
	}
	if h.service == nil || h.service.codec == nil {
		return false
	}

	tmp, err := os.CreateTemp(h.service.codec.tempDir, "session-archive-export-*")
	if err != nil {
		return false
	}
	name := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(name) }()
	if err := h.service.WriteContent(ctx, record, tmp); err != nil {
		return false
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return false
	}
	payload["content_encoding"] = "base64"
	header, err := json.Marshal(payload)
	if err != nil || len(header) == 0 || header[len(header)-1] != '}' {
		return false
	}
	if _, err := dst.Write(header[:len(header)-1]); err != nil {
		return false
	}
	if _, err := io.WriteString(dst, `,"content":"`); err != nil {
		return false
	}
	base64Writer := base64.NewEncoder(base64.StdEncoding, dst)
	_, copyErr := io.Copy(base64Writer, tmp)
	closeErr := base64Writer.Close()
	if copyErr != nil || closeErr != nil {
		return false
	}
	_, err = io.WriteString(dst, "\"}\n")
	return err == nil
}

func (h *Handler) audit(c *gin.Context, action, target string, extra map[string]any) error {
	if h.requiredAudit == nil {
		return errors.New("required audit unavailable")
	}
	if err := h.requiredAudit(c.Request.Context(), action, target, extra); err != nil {
		return err
	}
	if h.markRequiredAudit != nil {
		h.markRequiredAudit(c)
	}
	return nil
}

func (h *Handler) currentAdminID(c *gin.Context) (int64, bool) {
	if h.adminID == nil {
		return 0, false
	}
	return h.adminID(c)
}

func sessionFilterFromRequest(c *gin.Context) (SessionFilter, error) {
	filter := SessionFilter{CorrelationRequestID: strings.TrimSpace(c.Query("correlation_request_id")), Model: strings.TrimSpace(c.Query("model")), Client: strings.TrimSpace(c.Query("client")), Status: strings.TrimSpace(c.Query("status")), Protocol: strings.TrimSpace(c.Query("protocol"))}
	var err error
	for key, target := range map[string]*int64{"user_id": &filter.UserID, "api_key_id": &filter.APIKeyID, "group_id": &filter.GroupID} {
		if raw := strings.TrimSpace(c.Query(key)); raw != "" {
			*target, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || *target < 1 {
				return SessionFilter{}, fmt.Errorf("invalid %s", key)
			}
		}
	}
	for _, item := range []struct {
		keys   []string
		target *time.Time
	}{{[]string{"from", "start_at"}, &filter.From}, {[]string{"to", "end_at"}, &filter.To}} {
		for _, key := range item.keys {
			if raw := strings.TrimSpace(c.Query(key)); raw != "" {
				value, parseErr := time.Parse(time.RFC3339, raw)
				if parseErr != nil {
					return SessionFilter{}, fmt.Errorf("invalid %s", key)
				}
				*item.target = value
				break
			}
		}
	}
	return filter, nil
}

func pathID(c *gin.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func queryInt(c *gin.Context, key string, fallback int) int {
	value, err := strconv.Atoi(c.Query(key))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

func contentKind(purpose BlobPurpose) string {
	switch purpose {
	case PurposeRawRequest:
		return "request"
	case PurposeResponse:
		return "response"
	case PurposeTool:
		return "tool"
	case PurposeErrorBody:
		return "raw"
	case PurposeUpstreamRequest:
		return "upstream"
	case PurposeAttachment:
		return "attachment"
	default:
		return ""
	}
}

func isBinaryContent(contentType string, content []byte) bool {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return !utf8.Valid(content)
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return true
	}
	mediaType = strings.ToLower(mediaType)
	textual := strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") ||
		mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml") ||
		mediaType == "application/javascript" || mediaType == "application/x-javascript" ||
		mediaType == "application/x-ndjson" || mediaType == "application/json-seq" ||
		mediaType == "application/x-www-form-urlencoded" || mediaType == "application/graphql" ||
		mediaType == "application/sdp"
	return !textual || !utf8.Valid(content)
}

func encodeContentForTransport(contentType string, content []byte) (string, string) {
	if isBinaryContent(contentType, content) {
		return base64.StdEncoding.EncodeToString(content), "base64"
	}
	return string(content), ""
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func summaryPayload(item SessionSummary) gin.H {
	return gin.H{
		"id": item.ID, "user_id": item.UserID, "username": item.Username, "user_email": item.UserEmail,
		"api_key_id": item.APIKeyID, "api_key_name": item.APIKeyName, "group_id": item.GroupID,
		"group_name": item.GroupName, "protocol": item.Protocol, "client": item.Client,
		"first_model": item.FirstModel, "last_model": item.LastModel, "status": item.Status,
		"capture_coverage": item.CaptureCoverage, "merge_method": item.MergeMethod,
		"turn_count": item.TurnCount, "request_count": item.RequestCount, "has_truncated": item.HasTruncated,
		"created_at": item.CreatedAt, "last_activity_at": item.LastActivityAt, "expires_at": item.ExpiresAt,
	}
}

func policySnapshotPayload(raw json.RawMessage) gin.H {
	var policy ResolvedPolicy
	_ = json.Unmarshal(raw, &policy)
	return resolvedPolicyPayload(policy)
}

func resolvedPolicyPayload(policy ResolvedPolicy) gin.H {
	state := "off"
	if policy.Enabled {
		state = "on"
	}
	return gin.H{"state": state, "capture_request": policy.CaptureRawRequest, "capture_response": policy.CaptureResponse, "capture_transformed_request": policy.CaptureUpstreamRequest, "capture_tools": policy.CaptureTools, "capture_attachments": policy.CaptureAttachments, "body_limit_bytes": policy.PayloadMaxBytes, "retention_days": policy.RetentionDays, "matched_scope_type": policy.MatchedScope, "matched_scope_id": policy.MatchedScopeID}
}

func policyPayload(policy Policy) gin.H {
	return gin.H{"id": policy.ID, "scope_type": policy.ScopeType, "scope_id": policy.ScopeID, "state": policy.State, "capture_request": policy.CaptureRawRequest, "capture_response": policy.CaptureResponse, "capture_transformed_request": policy.CaptureUpstreamRequest, "capture_tools": policy.CaptureTools, "capture_attachments": policy.CaptureAttachments, "body_limit_bytes": policy.PayloadMaxBytes, "retention_days": policy.RetentionDays, "updated_at": policy.UpdatedAt}
}

func deletionJobPayload(job DeletionJob) gin.H {
	status := job.Status
	if status == "cancelled" {
		status = "canceled"
	}
	return gin.H{"id": job.ID, "status": status, "matched_sessions": job.MatchedSessions, "processed_sessions": job.ProcessedSessions, "deleted_sessions": job.DeletedSessions, "failed_sessions": job.FailedSessions, "released_blobs": job.ReleasedBlobs, "last_error": job.LastError, "created_at": job.CreatedAt, "started_at": job.StartedAt, "finished_at": job.FinishedAt}
}

func nullableID(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func writeRepositoryError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeHandlerError(c, http.StatusNotFound, err)
		return
	}
	writeHandlerError(c, http.StatusInternalServerError, err)
}

func writeHandlerError(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"error": err.Error()})
}
