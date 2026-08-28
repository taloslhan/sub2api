package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultHTTPUpstreamCaptureLimit = int64(64 << 20)

type httpUpstreamAttemptObserverContextKey struct{}

// HTTPUpstreamAttemptEvent 描述一次已经离开本地校验并进入真实 HTTP 传输的请求。
// Payload 仅在归档策略显式开启转换请求正文时读取，并受 PayloadMaxBytes 限制。
type HTTPUpstreamAttemptEvent struct {
	AttemptNo         int
	UpdateOnly        bool
	OccurredAt        time.Time
	Duration          time.Duration
	AccountID         int64
	TransformType     string
	Transport         string
	Status            string
	UpstreamStatus    int
	UpstreamRequestID string
	ErrorClass        string
	ErrorCode         string
	Payload           []byte
	ObservedBytes     int64
}

type httpUpstreamAttemptObserverState struct {
	mu               sync.Mutex
	nextAttempt      int
	finalizedAttempt int
	capturePayload   bool
	payloadMaxBytes  int64
	transformType    string
	observe          func(HTTPUpstreamAttemptEvent)
	latest           HTTPUpstreamAttemptEvent
}

// WithHTTPUpstreamAttemptObserver 为当前入站请求安装 fail-open 的真实出站观察器。
// 观察器状态由派生出的所有重试上下文共享，确保账号切换和内部重试连续编号。
func WithHTTPUpstreamAttemptObserver(
	ctx context.Context,
	capturePayload bool,
	payloadMaxBytes int64,
	transformType string,
	observe func(HTTPUpstreamAttemptEvent),
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observe == nil {
		return ctx
	}
	if payloadMaxBytes <= 0 || payloadMaxBytes > defaultHTTPUpstreamCaptureLimit {
		payloadMaxBytes = defaultHTTPUpstreamCaptureLimit
	}
	return context.WithValue(ctx, httpUpstreamAttemptObserverContextKey{}, &httpUpstreamAttemptObserverState{
		capturePayload: capturePayload, payloadMaxBytes: payloadMaxBytes,
		transformType: strings.TrimSpace(transformType), observe: observe,
	})
}

// RecordHTTPUpstreamAttempt 在真实传输返回响应头或错误后记录一次 Attempt。
// 归档读取与回调的任何异常都会被吞掉，不得改变原请求的响应、重试或计费语义。
func RecordHTTPUpstreamAttempt(request *http.Request, accountID int64, transport string, startedAt time.Time, response *http.Response, requestErr error) {
	defer func() { _ = recover() }()
	if request == nil {
		return
	}
	state, _ := request.Context().Value(httpUpstreamAttemptObserverContextKey{}).(*httpUpstreamAttemptObserverState)
	if state == nil || state.observe == nil {
		return
	}

	state.mu.Lock()
	state.nextAttempt++
	event := HTTPUpstreamAttemptEvent{
		AttemptNo: state.nextAttempt, OccurredAt: startedAt.UTC(), Duration: time.Since(startedAt),
		AccountID: accountID, TransformType: state.transformType, Transport: strings.TrimSpace(transport),
		Status: "completed",
	}
	capturePayload, payloadMaxBytes, observe := state.capturePayload, state.payloadMaxBytes, state.observe
	state.mu.Unlock()

	if requestErr != nil {
		event.Status = "failed"
		event.ErrorClass = "transport_error"
		event.ErrorCode = sanitizeUpstreamErrorMessage(requestErr.Error())
	}
	if response != nil {
		event.UpstreamStatus = response.StatusCode
		event.UpstreamRequestID = firstNonEmpty(
			response.Header.Get("x-request-id"),
			response.Header.Get("x-goog-request-id"),
			response.Header.Get("x-amzn-requestid"),
			response.Header.Get("openai-request-id"),
		)
		if response.StatusCode >= http.StatusBadRequest {
			event.Status = "failed"
			event.ErrorClass = "upstream_http"
			event.ErrorCode = strconv.Itoa(response.StatusCode)
		}
	}
	if capturePayload && request.GetBody != nil {
		body, err := request.GetBody()
		if err == nil {
			defer func() { _ = body.Close() }()
			// 多读一个字节，让 Collector 在正文超过策略上限时保留 truncated 事实。
			event.Payload, _ = io.ReadAll(io.LimitReader(body, payloadMaxBytes+1))
		}
	}
	event.ObservedBytes = int64(len(event.Payload))
	if request.ContentLength > event.ObservedBytes {
		event.ObservedBytes = request.ContentLength
	}
	state.mu.Lock()
	state.latest = event
	state.latest.Payload = nil
	state.mu.Unlock()
	observe(event)
}

// FinalizeLatestHTTPUpstreamAttempt 在账号级 Forward 返回时修正最近一次传输的协议终态。
// 例如上游先返回 200，随后 SSE 解析失败并触发 failover 时，同一 AttemptNo 会从
// completed 更新为 failed；更新事件不携带正文，避免重复创建 Blob 引用。
func FinalizeLatestHTTPUpstreamAttempt(ctx context.Context, forwardErr error) {
	defer func() { _ = recover() }()
	if ctx == nil {
		return
	}
	state, _ := ctx.Value(httpUpstreamAttemptObserverContextKey{}).(*httpUpstreamAttemptObserverState)
	if state == nil || state.observe == nil {
		return
	}
	state.mu.Lock()
	if state.nextAttempt <= 0 || state.finalizedAttempt >= state.nextAttempt || state.latest.AttemptNo != state.nextAttempt {
		state.mu.Unlock()
		return
	}
	event := state.latest
	state.finalizedAttempt = event.AttemptNo
	observe := state.observe
	state.mu.Unlock()

	event.UpdateOnly = true
	event.Payload = nil
	event.ObservedBytes = 0
	event.Duration = time.Since(event.OccurredAt)
	if forwardErr != nil && event.Status == "completed" {
		event.Status = "failed"
		event.ErrorClass = "protocol_error"
		event.ErrorCode = sanitizeUpstreamErrorMessage(forwardErr.Error())
		if errors.Is(forwardErr, context.Canceled) || errors.Is(forwardErr, context.DeadlineExceeded) || ctx.Err() != nil {
			event.Status = "cancelled"
			event.ErrorClass = "request_cancelled"
		}
	}
	observe(event)
}
