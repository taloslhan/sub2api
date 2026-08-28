package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/custom/sessionarchive"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func configureSessionArchiveCaptures(handler *OpenAIGatewayHandler, archive *sessionarchive.Service) {
	if handler == nil || archive == nil {
		return
	}
	adapter := &sessionArchiveProtocolAdapter{
		archive: archive,
		states:  make(map[string]*protocolCaptureState),
	}
	handler.SetSessionArchive(archive)
	handler.SetOpenAIWSArchiveCapture(func(ctx context.Context, scope OpenAIWSArchiveScope, event service.OpenAIWSArchiveEvent) {
		adapter.captureOpenAIWS(ctx, scope, event)
	})
	handler.SetGrokRealtimeArchiveCapture(func(ctx context.Context, scope OpenAIWSArchiveScope, event service.GrokRealtimeCaptureEvent) {
		adapter.captureRealtime(ctx, scope, protocolStreamEvent{
			OccurredAt: event.OccurredAt, CorrelationRequestID: event.CorrelationRequestID,
			Direction: event.Direction, SequenceNo: event.SequenceNo, EventType: event.EventType,
			Status: event.Status, Error: event.Error, Payload: event.Payload,
		})
	})
	handler.SetLiveSidebandArchiveCapture(func(ctx context.Context, scope OpenAIWSArchiveScope, event service.LiveSidebandCaptureEvent) {
		adapter.captureRealtime(ctx, scope, protocolStreamEvent{
			OccurredAt: event.OccurredAt, CorrelationRequestID: event.CorrelationRequestID,
			Direction: event.Direction, SequenceNo: event.SequenceNo, EventType: event.EventType,
			Status: event.Status, Error: event.Error, Payload: event.Payload,
		})
	})
}

type sessionArchiveProtocolAdapter struct {
	archive *sessionarchive.Service
	mu      sync.Mutex
	states  map[string]*protocolCaptureState
}

type protocolCaptureState struct {
	mu             sync.Mutex
	closed         bool
	base           sessionarchive.CaptureMeta
	rawSink        *sessionarchive.CaptureSink
	responseSink   *sessionarchive.CaptureSink
	rawFrameCount  int64
	responseFrames int64
}

type protocolStreamEvent struct {
	OccurredAt           time.Time
	CorrelationRequestID string
	Direction            string
	SequenceNo           int64
	EventType            string
	Status               string
	Error                string
	Payload              []byte
}

// openAIWSArchiveEventTracker 统一补全 Responses WS 的归档顺序和终态。
// service 在可重试错误上会把终态留给外层 failover；若外层最终无法继续，
// Finish 会为所有仍开放的 Turn 补发终态，确保有界 sink 与字节许可被释放。
type openAIWSArchiveEventTracker struct {
	mu            sync.Mutex
	emit          func(service.OpenAIWSArchiveEvent)
	accepted      map[int]bool
	raw           map[int]bool
	terminal      map[int]bool
	acceptedOrder []int
	attempts      map[int]int
	sequences     map[int]map[service.OpenAIWSArchiveEventKind]int64
}

func newOpenAIWSArchiveEventTracker(emit func(service.OpenAIWSArchiveEvent)) *openAIWSArchiveEventTracker {
	return &openAIWSArchiveEventTracker{
		emit: emit, accepted: make(map[int]bool, 4), raw: make(map[int]bool, 4),
		terminal: make(map[int]bool, 4), acceptedOrder: make([]int, 0, 4),
		attempts: make(map[int]int, 4), sequences: make(map[int]map[service.OpenAIWSArchiveEventKind]int64, 4),
	}
}

func (t *openAIWSArchiveEventTracker) Capture(event service.OpenAIWSArchiveEvent) {
	if t == nil || t.emit == nil || event.Turn <= 0 {
		return
	}
	t.mu.Lock()
	switch event.Kind {
	case service.OpenAIWSArchiveTurnAccepted:
		if t.accepted[event.Turn] {
			t.mu.Unlock()
			return
		}
		t.accepted[event.Turn] = true
		t.acceptedOrder = append(t.acceptedOrder, event.Turn)
	case service.OpenAIWSArchiveRawFrame:
		if t.raw[event.Turn] {
			t.mu.Unlock()
			return
		}
		t.raw[event.Turn] = true
	case service.OpenAIWSArchiveAttempt:
		// 空状态表示真实出站开始；带状态的同类事件用于结束当前 attempt，
		// 不得再次递增编号，否则 retryable 失败会留下永久 active 的旧 attempt。
		if strings.TrimSpace(event.Status) == "" {
			t.attempts[event.Turn]++
		} else if t.attempts[event.Turn] == 0 {
			t.attempts[event.Turn] = 1
		}
		event.AttemptNo = t.attempts[event.Turn]
	case service.OpenAIWSArchiveTerminal:
		if t.terminal[event.Turn] {
			t.mu.Unlock()
			return
		}
		t.terminal[event.Turn] = true
		if event.AttemptNo == 0 {
			event.AttemptNo = t.attempts[event.Turn]
		}
	}
	event.TurnSequenceNo = event.Turn
	if t.sequences[event.Turn] == nil {
		t.sequences[event.Turn] = make(map[service.OpenAIWSArchiveEventKind]int64, 5)
	}
	t.sequences[event.Turn][event.Kind]++
	event.SequenceNo = t.sequences[event.Turn][event.Kind]
	t.mu.Unlock()
	t.emit(event)
}

func (t *openAIWSArchiveEventTracker) Finish(cancelled bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	openTurns := make([]int, 0, len(t.acceptedOrder))
	for _, turn := range t.acceptedOrder {
		if !t.terminal[turn] {
			openTurns = append(openTurns, turn)
		}
	}
	t.mu.Unlock()
	status := "failed"
	if cancelled {
		status = "cancelled"
	}
	for _, turn := range openTurns {
		t.Capture(service.OpenAIWSArchiveEvent{
			Kind: service.OpenAIWSArchiveTerminal, Turn: turn, Status: status,
			Error: "websocket connection closed before archive terminal",
		})
	}
}

func (a *sessionArchiveProtocolAdapter) captureOpenAIWS(ctx context.Context, scope OpenAIWSArchiveScope, event service.OpenAIWSArchiveEvent) {
	defer func() { _ = recover() }()
	correlationID := strings.TrimSpace(event.CorrelationRequestID)
	if correlationID == "" {
		correlationID = service.CorrelationRequestIDFromContext(ctx)
	}
	if correlationID == "" {
		return
	}

	if event.Kind == service.OpenAIWSArchiveTurnAccepted {
		policy := a.archive.ResolvePolicy(ctx, sessionarchive.PolicyIdentity{UserID: scope.UserID, APIKeyID: scope.APIKeyID, GroupID: scope.GroupID})
		if !policy.Enabled {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.states[correlationID] != nil {
			return
		}
		state := &protocolCaptureState{base: sessionarchive.CaptureMeta{
			OccurredAt: event.OccurredAt, UserID: scope.UserID, APIKeyID: scope.APIKeyID, GroupID: scope.GroupID,
			Protocol: truncateArchiveField(scope.Protocol, 64), Client: truncateArchiveField(scope.Client, 128),
			Endpoint: truncateArchiveField(scope.Endpoint, 255), Model: truncateArchiveField(scope.Model, 255),
			StableSessionID: scope.StableSessionID, ProtocolTurnID: event.RequestID,
			CorrelationRequestID: correlationID, TurnSequenceNo: event.TurnSequenceNo,
			CaptureCoverage: "full", Policy: policy,
		}}
		a.states[correlationID] = state
		meta := state.base
		meta.Kind, meta.Status = sessionarchive.EventTurnAccepted, event.Status
		a.archive.TryCapture(sessionarchive.CaptureEvent{Meta: meta})
		return
	}
	a.mu.Lock()
	state := a.states[correlationID]
	a.mu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return
	}
	meta := state.base
	meta.OccurredAt, meta.SequenceNo = event.OccurredAt, event.SequenceNo
	meta.AttemptNo, meta.AccountID = event.AttemptNo, event.AccountID
	meta.TransformType = firstNonEmptyString(event.Mode, event.Transport)
	meta.Direction, meta.Status = event.Direction, event.Status
	meta.ErrorCode = truncateArchiveField(event.EventType, 128)
	if event.Error != "" {
		meta.ErrorClass = "websocket_error"
	}
	switch event.Kind {
	case service.OpenAIWSArchiveRawFrame:
		if state.base.Policy.CaptureRawRequest {
			meta.Kind, meta.Purpose = sessionarchive.EventRawRequest, sessionarchive.PurposeRawRequest
			meta.ContentType = "application/x-ndjson"
			if state.rawSink == nil {
				state.rawSink = a.archive.NewSink(meta)
			}
			appendArchiveFrame(state.rawSink, &state.rawFrameCount, event.Payload)
		}
	case service.OpenAIWSArchiveAttempt:
		meta.Kind = sessionarchive.EventAttempt
		meta.ContentType = "application/json"
		if state.base.Policy.CaptureUpstreamRequest {
			meta.Purpose = sessionarchive.PurposeUpstreamRequest
			a.archive.TryCaptureBytes(meta, event.Payload)
		} else {
			a.archive.TryCapture(sessionarchive.CaptureEvent{Meta: meta})
		}
	case service.OpenAIWSArchiveDownstream:
		if state.base.Policy.CaptureResponse {
			meta.Kind, meta.Purpose = sessionarchive.EventResponse, sessionarchive.PurposeResponse
			meta.ContentType = "application/x-ndjson"
			if state.responseSink == nil {
				state.responseSink = a.archive.NewSink(meta)
			}
			appendArchiveFrame(state.responseSink, &state.responseFrames, event.Payload)
		}
	case service.OpenAIWSArchiveTerminal:
		state.closed = true
		finishProtocolCapture(state)
		a.mu.Lock()
		if a.states[correlationID] == state {
			delete(a.states, correlationID)
		}
		a.mu.Unlock()
		meta.Kind = sessionarchive.EventTerminal
		meta.UpstreamRequestID = strings.TrimSpace(event.RequestID)
		if meta.Status == "error" {
			meta.Status = "failed"
		}
		a.archive.TryCapture(sessionarchive.CaptureEvent{Meta: meta})
	}
}

func (a *sessionArchiveProtocolAdapter) captureRealtime(ctx context.Context, scope OpenAIWSArchiveScope, event protocolStreamEvent) {
	defer func() { _ = recover() }()
	correlationID := firstNonEmptyString(event.CorrelationRequestID, service.CorrelationRequestIDFromContext(ctx))
	if correlationID == "" {
		return
	}
	if event.EventType == "turn.accepted" {
		policy := a.archive.ResolvePolicy(ctx, sessionarchive.PolicyIdentity{UserID: scope.UserID, APIKeyID: scope.APIKeyID, GroupID: scope.GroupID})
		if !policy.Enabled {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.states[correlationID] != nil {
			return
		}
		coverage := "full"
		if scope.Protocol == "openai_live" {
			coverage = "control_plane_only"
		}
		state := &protocolCaptureState{base: sessionarchive.CaptureMeta{
			OccurredAt: event.OccurredAt, UserID: scope.UserID, APIKeyID: scope.APIKeyID, GroupID: scope.GroupID,
			Protocol: truncateArchiveField(scope.Protocol, 64), Client: truncateArchiveField(scope.Client, 128),
			Endpoint: truncateArchiveField(scope.Endpoint, 255), Model: truncateArchiveField(scope.Model, 255),
			StableSessionID: scope.StableSessionID, CorrelationRequestID: correlationID,
			TurnSequenceNo: 1, CaptureCoverage: coverage, Policy: policy,
		}}
		a.states[correlationID] = state
		meta := state.base
		meta.Kind, meta.Status = sessionarchive.EventTurnAccepted, event.Status
		a.archive.TryCapture(sessionarchive.CaptureEvent{Meta: meta})
		return
	}
	a.mu.Lock()
	state := a.states[correlationID]
	a.mu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return
	}
	meta := state.base
	meta.OccurredAt, meta.SequenceNo = event.OccurredAt, event.SequenceNo
	meta.Direction, meta.Status = event.Direction, event.Status
	meta.ErrorCode = truncateArchiveField(event.EventType, 128)
	if event.Error != "" {
		meta.ErrorClass = "realtime_proxy_error"
	}
	if event.EventType == "terminal" {
		state.closed = true
		finishProtocolCapture(state)
		a.mu.Lock()
		if a.states[correlationID] == state {
			delete(a.states, correlationID)
		}
		a.mu.Unlock()
		meta.Kind = sessionarchive.EventTerminal
		a.archive.TryCapture(sessionarchive.CaptureEvent{Meta: meta})
		return
	}
	if event.Direction == "client_to_upstream" && state.base.Policy.CaptureRawRequest {
		meta.Kind, meta.Purpose, meta.ContentType = sessionarchive.EventRawRequest, sessionarchive.PurposeRawRequest, "application/x-ndjson"
		if state.rawSink == nil {
			state.rawSink = a.archive.NewSink(meta)
		}
		appendArchiveFrame(state.rawSink, &state.rawFrameCount, event.Payload)
	}
	if event.Direction == "upstream_to_client" && state.base.Policy.CaptureResponse {
		meta.Kind, meta.Purpose, meta.ContentType = sessionarchive.EventResponse, sessionarchive.PurposeResponse, "application/x-ndjson"
		if state.responseSink == nil {
			state.responseSink = a.archive.NewSink(meta)
		}
		appendArchiveFrame(state.responseSink, &state.responseFrames, event.Payload)
	}
}

func appendArchiveFrame(sink *sessionarchive.CaptureSink, frameCount *int64, payload []byte) {
	if sink == nil {
		return
	}
	if *frameCount > 0 {
		_, _ = sink.Append([]byte{'\n'})
	}
	_, _ = sink.Append(payload)
	(*frameCount)++
}

func finishProtocolCapture(state *protocolCaptureState) {
	if state.rawSink != nil {
		state.rawSink.Finish()
	}
	if state.responseSink != nil {
		state.responseSink.Finish()
	}
}

func captureLiveCreate(
	archive *sessionarchive.Service,
	c *gin.Context,
	apiKey *service.APIKey,
	userID int64,
	model string,
	originalRequest *service.LiveCallRequest,
	upstreamRequest *service.LiveCallRequest,
	created *service.LiveCallCreated,
) {
	defer func() { _ = recover() }()
	if archive == nil || c == nil || c.Request == nil || apiKey == nil || originalRequest == nil || upstreamRequest == nil || created == nil {
		return
	}
	identity := sessionarchive.PolicyIdentity{UserID: userID, APIKeyID: apiKey.ID}
	if apiKey.GroupID != nil {
		identity.GroupID = *apiKey.GroupID
	}
	policy := archive.ResolvePolicy(c.Request.Context(), identity)
	correlationID := service.CorrelationRequestIDFromContext(c.Request.Context())
	if !policy.Enabled || correlationID == "" {
		return
	}
	base := sessionarchive.CaptureMeta{
		OccurredAt: time.Now().UTC(), UserID: userID, APIKeyID: apiKey.ID, GroupID: identity.GroupID,
		Protocol: "openai_live", Client: truncateArchiveField(c.Request.UserAgent(), 128),
		Endpoint: truncateArchiveField(firstNonEmptyString(GetInboundEndpoint(c), c.FullPath(), c.Request.URL.Path), 255),
		Model:    truncateArchiveField(model, 255), StableSessionID: created.CallID,
		CorrelationRequestID: correlationID, TurnSequenceNo: 1,
		CaptureCoverage: "control_plane_only", Policy: policy,
		Metadata: sessionarchive.AllowMetadata(c.Request.Header),
	}
	archive.TryCapture(sessionarchive.CaptureEvent{Meta: withArchiveKind(base, sessionarchive.EventTurnAccepted, "", 0)})
	if payload, err := json.Marshal(originalRequest); err == nil && policy.CaptureRawRequest {
		meta := withArchiveKind(base, sessionarchive.EventRawRequest, sessionarchive.PurposeRawRequest, 1)
		meta.ContentType, meta.Direction = "application/json", "client_to_upstream"
		archive.TryCaptureBytes(meta, payload)
	}
	if payload, err := json.Marshal(upstreamRequest); err == nil {
		meta := withArchiveKind(base, sessionarchive.EventAttempt, "", 1)
		meta.AttemptNo, meta.Status, meta.TransformType = 1, "completed", "live_create"
		if created.Account != nil {
			meta.AccountID = created.Account.ID
		}
		if policy.CaptureUpstreamRequest {
			meta.Purpose, meta.ContentType, meta.Direction = sessionarchive.PurposeUpstreamRequest, "application/json", "client_to_upstream"
			archive.TryCaptureBytes(meta, payload)
		} else {
			archive.TryCapture(sessionarchive.CaptureEvent{Meta: meta})
		}
	}
	if policy.CaptureResponse {
		meta := withArchiveKind(base, sessionarchive.EventResponse, sessionarchive.PurposeResponse, 1)
		meta.ContentType, meta.Direction = "application/sdp", "upstream_to_client"
		archive.TryCaptureBytes(meta, created.SDP)
	}
	terminal := withArchiveKind(base, sessionarchive.EventTerminal, "", 0)
	terminal.OccurredAt, terminal.Status, terminal.AttemptNo = time.Now().UTC(), "completed", 1
	archive.TryCapture(sessionarchive.CaptureEvent{Meta: terminal})
}

type sessionArchiveHTTPResponseWriter struct {
	gin.ResponseWriter
	meta    sessionarchive.CaptureMeta
	newSink func(sessionarchive.CaptureMeta) *sessionarchive.CaptureSink
	sink    *sessionarchive.CaptureSink
}

func (w *sessionArchiveHTTPResponseWriter) Write(payload []byte) (int, error) {
	written, err := w.ResponseWriter.Write(payload)
	if written > 0 {
		_, _ = w.ensureSink().Append(payload[:written])
	}
	return written, err
}

func (w *sessionArchiveHTTPResponseWriter) WriteString(payload string) (int, error) {
	written, err := w.ResponseWriter.WriteString(payload)
	if written > 0 {
		_, _ = w.ensureSink().Append([]byte(payload[:written]))
	}
	return written, err
}

func (w *sessionArchiveHTTPResponseWriter) ensureSink() *sessionarchive.CaptureSink {
	if w.sink == nil {
		w.meta.ContentType = truncateArchiveField(firstNonEmptyString(w.Header().Get("Content-Type"), "application/octet-stream"), 128)
		w.sink = w.newSink(w.meta)
	}
	return w.sink
}

func (w *sessionArchiveHTTPResponseWriter) Finish() sessionarchive.CaptureResult {
	return w.ensureSink().Finish()
}

func (w *sessionArchiveHTTPResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// beginSessionArchiveHTTP 在协议 Handler 已读取原始 body 后接管有界响应采集。
// 所有存储工作仍由 Collector 异步完成；本函数只做策略解析、字节许可申请与入队。
func beginSessionArchiveHTTP(
	archive *sessionarchive.Service,
	c *gin.Context,
	apiKey *service.APIKey,
	userID int64,
	protocol string,
	model string,
	body []byte,
) func() {
	if archive == nil || c == nil || c.Request == nil || c.Writer == nil || apiKey == nil {
		return func() {}
	}
	identity := sessionarchive.PolicyIdentity{UserID: userID, APIKeyID: apiKey.ID}
	if apiKey.GroupID != nil {
		identity.GroupID = *apiKey.GroupID
	}
	policy := archive.ResolvePolicy(c.Request.Context(), identity)
	if !policy.Enabled {
		return func() {}
	}

	correlationID := service.CorrelationRequestIDFromContext(c.Request.Context())
	if correlationID == "" {
		return func() {}
	}
	base := sessionarchive.CaptureMeta{
		OccurredAt:           time.Now().UTC(),
		UserID:               userID,
		APIKeyID:             apiKey.ID,
		GroupID:              identity.GroupID,
		Protocol:             truncateArchiveField(protocol, 64),
		Client:               truncateArchiveField(c.Request.UserAgent(), 128),
		Endpoint:             truncateArchiveField(firstNonEmptyString(GetInboundEndpoint(c), c.FullPath(), c.Request.URL.Path), 255),
		Model:                truncateArchiveField(model, 255),
		StableSessionID:      archiveStableSessionID(c, body),
		CorrelationRequestID: correlationID,
		// HTTP 的稳定会话可跨多次请求复用；0 让 repository 在同一 Session 内原子分配下一 Turn。
		TurnSequenceNo:         0,
		CaptureCoverage:        "full",
		NormalizedMessageChain: archiveMessageChain(body),
		Policy:                 policy,
		Metadata:               sessionarchive.AllowMetadata(c.Request.Header),
	}
	archive.TryCapture(sessionarchive.CaptureEvent{Meta: withArchiveKind(base, sessionarchive.EventTurnAccepted, "", 0)})
	if policy.CaptureRawRequest {
		archive.TryCaptureBytes(withArchiveKind(base, sessionarchive.EventRawRequest, sessionarchive.PurposeRawRequest, 1), body)
	}
	captureSessionArchiveRequestParts(archive, base, protocol, body)
	var lastAttemptNo atomic.Int64
	requestContext := service.WithHTTPUpstreamAttemptObserver(
		c.Request.Context(),
		policy.CaptureUpstreamRequest,
		policy.PayloadMaxBytes,
		protocol,
		func(event service.HTTPUpstreamAttemptEvent) {
			lastAttemptNo.Store(int64(event.AttemptNo))
			captureSessionArchiveHTTPAttempt(archive, base, event)
		},
	)
	c.Request = c.Request.WithContext(requestContext)

	originalWriter := c.Writer
	var responseWriter *sessionArchiveHTTPResponseWriter
	if policy.CaptureResponse {
		responseMeta := withArchiveKind(base, sessionarchive.EventResponse, sessionarchive.PurposeResponse, 1)
		responseMeta.Direction = "gateway_to_client"
		responseWriter = &sessionArchiveHTTPResponseWriter{ResponseWriter: originalWriter, meta: responseMeta, newSink: archive.NewSink}
		c.Writer = responseWriter
	}

	return func() {
		if responseWriter != nil {
			responseWriter.Finish()
			c.Writer = originalWriter
		}
		terminal := withArchiveKind(base, sessionarchive.EventTerminal, "", 0)
		terminal.OccurredAt = time.Now().UTC()
		terminal.AttemptNo = int(lastAttemptNo.Load())
		terminal.Status = "completed"
		if originalWriter.Status() >= http.StatusBadRequest {
			terminal.Status = "failed"
			terminal.ErrorCode = http.StatusText(originalWriter.Status())
		}
		if c.Request.Context().Err() != nil {
			terminal.Status = "cancelled"
			terminal.ClientDisconnected = true
		}
		archive.TryCapture(sessionarchive.CaptureEvent{Meta: terminal})
	}
}

type sessionArchiveAttemptCapture interface {
	TryCapture(sessionarchive.CaptureEvent) sessionarchive.CaptureResult
	TryCaptureBytes(sessionarchive.CaptureMeta, []byte) sessionarchive.CaptureResult
	TryCaptureBytesObserved(sessionarchive.CaptureMeta, []byte, int64) sessionarchive.CaptureResult
}

func captureSessionArchiveHTTPAttempt(archive sessionArchiveAttemptCapture, base sessionarchive.CaptureMeta, event service.HTTPUpstreamAttemptEvent) {
	defer func() { _ = recover() }()
	if archive == nil || event.AttemptNo <= 0 {
		return
	}
	meta := withArchiveKind(base, sessionarchive.EventAttempt, "", int64(event.AttemptNo))
	meta.OccurredAt = event.OccurredAt
	meta.AttemptNo = event.AttemptNo
	meta.AccountID = event.AccountID
	meta.TransformType = truncateArchiveField(strings.Trim(strings.Join([]string{event.TransformType, event.Transport}, ":"), ":"), 64)
	meta.Status = event.Status
	meta.UpstreamStatus = event.UpstreamStatus
	meta.UpstreamRequestID = truncateArchiveField(event.UpstreamRequestID, 255)
	meta.ErrorClass = truncateArchiveField(event.ErrorClass, 64)
	meta.ErrorCode = truncateArchiveField(event.ErrorCode, 128)
	meta.Duration = event.Duration
	if event.UpdateOnly {
		archive.TryCapture(sessionarchive.CaptureEvent{Meta: meta})
		return
	}
	if base.Policy.CaptureUpstreamRequest {
		meta.Purpose = sessionarchive.PurposeUpstreamRequest
		meta.ContentType = "application/json"
		meta.Direction = "client_to_upstream"
		archive.TryCaptureBytesObserved(meta, event.Payload, event.ObservedBytes)
		return
	}
	archive.TryCapture(sessionarchive.CaptureEvent{Meta: meta})
}

func withArchiveKind(base sessionarchive.CaptureMeta, kind sessionarchive.EventKind, purpose sessionarchive.BlobPurpose, sequence int64) sessionarchive.CaptureMeta {
	base.Kind = kind
	base.Purpose = purpose
	base.SequenceNo = sequence
	return base
}

func archiveStableSessionID(c *gin.Context, body []byte) string {
	if c != nil {
		if stable := strings.TrimSpace(service.ExtractClientSessionID(c)); stable != "" {
			return stable
		}
	}
	for _, path := range []string{
		"session_id", "conversation_id", "thread_id", "previous_response_id",
		"metadata.session_id", "metadata.user_id", "generationConfig.session_id",
	} {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
			return value
		}
	}
	return ""
}

// archiveMessageChain 只把逐项摘要交给 core；原始 prompt 不进入 PostgreSQL 元数据。
func archiveMessageChain(body []byte) []string {
	for _, path := range []string{"messages", "input", "contents"} {
		value := gjson.GetBytes(body, path)
		if !value.IsArray() {
			continue
		}
		items := value.Array()
		if len(items) > 256 {
			items = items[len(items)-256:]
		}
		chain := make([]string, 0, len(items))
		for _, item := range items {
			sum := sha256.Sum256([]byte(item.Raw))
			chain = append(chain, hex.EncodeToString(sum[:]))
		}
		return chain
	}
	return nil
}

func truncateArchiveField(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
