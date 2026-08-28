package handler

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/custom/sessionarchive"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSessionArchiveHTTPResponseWriterCapturesActualContentTypeLazily(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		contentType string
		payload     string
		writeBytes  bool
	}{
		{name: "json", contentType: "application/json; charset=utf-8", payload: `{"ok":true}`, writeBytes: true},
		{name: "sse", contentType: "text/event-stream", payload: "data: done\n\n"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			processed := make(chan sessionarchive.CaptureEvent, 1)
			collector, err := sessionarchive.NewCollector(sessionarchive.CollectorOptions{
				WorkerCount: 1, QueueSize: 2, QueueMaxBytes: 1024, PayloadMaxBytes: 1024,
				MaxRetries: 0, RetryBackoff: time.Millisecond,
				Processor: sessionarchive.ProcessorFunc(func(_ context.Context, event sessionarchive.CaptureEvent) error {
					processed <- event
					return nil
				}),
			})
			require.NoError(t, err)
			require.NoError(t, collector.Start(context.Background()))

			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			writer := &sessionArchiveHTTPResponseWriter{
				ResponseWriter: ginContext.Writer,
				meta:           sessionarchive.CaptureMeta{Policy: sessionarchive.ResolvedPolicy{Enabled: true, PayloadMaxBytes: 1024}},
				newSink:        collector.NewSink,
			}
			require.Nil(t, writer.sink)
			writer.Header().Set("Content-Type", testCase.contentType)
			var written int
			if testCase.writeBytes {
				written, err = writer.Write([]byte(testCase.payload))
			} else {
				written, err = writer.WriteString(testCase.payload)
			}
			require.NoError(t, err)
			require.Equal(t, len(testCase.payload), written)
			writer.Finish()

			select {
			case event := <-processed:
				require.Equal(t, testCase.contentType, event.Meta.ContentType)
				require.Equal(t, testCase.payload, string(event.Observation.StoredPayload))
			case <-time.After(time.Second):
				t.Fatal("归档响应事件未被处理")
			}
			require.NoError(t, collector.Shutdown(context.Background()))
		})
	}
}

func TestAppendArchiveFrameSharesOneBoundedSink(t *testing.T) {
	processed := make(chan sessionarchive.CaptureEvent, 1)
	collector, err := sessionarchive.NewCollector(sessionarchive.CollectorOptions{
		WorkerCount: 1, QueueSize: 1, QueueMaxBytes: 8, PayloadMaxBytes: 8,
		MaxRetries: 0, RetryBackoff: time.Millisecond,
		Processor: sessionarchive.ProcessorFunc(func(_ context.Context, event sessionarchive.CaptureEvent) error {
			processed <- event
			return nil
		}),
	})
	require.NoError(t, err)
	require.NoError(t, collector.Start(context.Background()))

	sink := collector.NewSink(sessionarchive.CaptureMeta{Policy: sessionarchive.ResolvedPolicy{Enabled: true, PayloadMaxBytes: 8}})
	var frames int64
	appendArchiveFrame(sink, &frames, []byte("12345"))
	appendArchiveFrame(sink, &frames, []byte("67890"))
	result := sink.Finish()
	require.True(t, result.Accepted)
	require.True(t, result.Truncated)
	require.Equal(t, int64(11), result.ObservedBytes)
	require.Equal(t, int64(8), result.StoredBytes)

	select {
	case event := <-processed:
		require.Equal(t, "12345\n67", string(event.Observation.StoredPayload))
	case <-time.After(time.Second):
		t.Fatal("归档事件未被处理")
	}
	require.NoError(t, collector.Shutdown(context.Background()))
}

func TestOpenAIWSArchiveEventTrackerFinishesOnlyOpenTurns(t *testing.T) {
	var events []service.OpenAIWSArchiveEvent
	tracker := newOpenAIWSArchiveEventTracker(func(event service.OpenAIWSArchiveEvent) {
		events = append(events, event)
	})

	tracker.Capture(service.OpenAIWSArchiveEvent{Kind: service.OpenAIWSArchiveTurnAccepted, Turn: 1})
	tracker.Capture(service.OpenAIWSArchiveEvent{Kind: service.OpenAIWSArchiveRawFrame, Turn: 1})
	tracker.Capture(service.OpenAIWSArchiveEvent{Kind: service.OpenAIWSArchiveRawFrame, Turn: 1})
	tracker.Capture(service.OpenAIWSArchiveEvent{Kind: service.OpenAIWSArchiveAttempt, Turn: 1})
	tracker.Capture(service.OpenAIWSArchiveEvent{Kind: service.OpenAIWSArchiveAttempt, Turn: 1, Status: "failed"})
	tracker.Capture(service.OpenAIWSArchiveEvent{Kind: service.OpenAIWSArchiveTurnAccepted, Turn: 2})
	tracker.Capture(service.OpenAIWSArchiveEvent{Kind: service.OpenAIWSArchiveTerminal, Turn: 2, Status: "completed"})
	tracker.Finish(false)
	tracker.Finish(false)

	var terminals []service.OpenAIWSArchiveEvent
	for _, event := range events {
		if event.Kind == service.OpenAIWSArchiveTerminal {
			terminals = append(terminals, event)
		}
	}
	require.Len(t, terminals, 2)
	require.Equal(t, 2, terminals[0].Turn)
	require.Equal(t, "completed", terminals[0].Status)
	require.Equal(t, 1, terminals[1].Turn)
	require.Equal(t, "failed", terminals[1].Status)
	require.Equal(t, 1, terminals[1].AttemptNo)
	require.Equal(t, int64(1), terminals[1].SequenceNo)
	var attempts []service.OpenAIWSArchiveEvent
	for _, event := range events {
		if event.Kind == service.OpenAIWSArchiveAttempt && event.Turn == 1 {
			attempts = append(attempts, event)
		}
	}
	require.Len(t, attempts, 2)
	require.Equal(t, 1, attempts[0].AttemptNo)
	require.Equal(t, 1, attempts[1].AttemptNo)
	require.Equal(t, "failed", attempts[1].Status)
}

type recordingSessionArchiveAttemptCapture struct {
	events        []sessionarchive.CaptureEvent
	metas         []sessionarchive.CaptureMeta
	payloads      [][]byte
	observedBytes []int64
}

func (r *recordingSessionArchiveAttemptCapture) TryCapture(event sessionarchive.CaptureEvent) sessionarchive.CaptureResult {
	r.events = append(r.events, event)
	return sessionarchive.CaptureResult{Accepted: true}
}

func (r *recordingSessionArchiveAttemptCapture) TryCaptureBytes(meta sessionarchive.CaptureMeta, payload []byte) sessionarchive.CaptureResult {
	return r.TryCaptureBytesObserved(meta, payload, int64(len(payload)))
}

func (r *recordingSessionArchiveAttemptCapture) TryCaptureBytesObserved(meta sessionarchive.CaptureMeta, payload []byte, observedBytes int64) sessionarchive.CaptureResult {
	r.metas = append(r.metas, meta)
	r.payloads = append(r.payloads, append([]byte(nil), payload...))
	r.observedBytes = append(r.observedBytes, observedBytes)
	return sessionarchive.CaptureResult{Accepted: true}
}

func TestCaptureSessionArchiveHTTPAttemptOmitsTransformedBodyByDefault(t *testing.T) {
	capture := &recordingSessionArchiveAttemptCapture{}
	base := sessionarchive.CaptureMeta{Policy: sessionarchive.ResolvedPolicy{Enabled: true, CaptureUpstreamRequest: false}}
	captureSessionArchiveHTTPAttempt(capture, base, service.HTTPUpstreamAttemptEvent{
		AttemptNo: 1, AccountID: 10, TransformType: "messages", Transport: "tls_fingerprint",
		Status: "completed", UpstreamStatus: 200, Payload: []byte(`{"secret":"must-not-be-saved"}`),
	})

	require.Len(t, capture.events, 1)
	require.Empty(t, capture.metas)
	require.Empty(t, capture.events[0].Observation.StoredPayload)
	require.Equal(t, "messages:tls_fingerprint", capture.events[0].Meta.TransformType)
}

func TestCaptureSessionArchiveHTTPAttemptStoresTransformedBodyOnlyWhenEnabled(t *testing.T) {
	capture := &recordingSessionArchiveAttemptCapture{}
	base := sessionarchive.CaptureMeta{Policy: sessionarchive.ResolvedPolicy{Enabled: true, CaptureUpstreamRequest: true}}
	payload := []byte(`{"input":"hello"}`)
	captureSessionArchiveHTTPAttempt(capture, base, service.HTTPUpstreamAttemptEvent{
		AttemptNo: 2, AccountID: 11, TransformType: "responses", Transport: "http",
		Status: "failed", UpstreamStatus: 429, ErrorClass: "upstream_http", ErrorCode: "429", Payload: payload, ObservedBytes: int64(len(payload)),
	})

	require.Empty(t, capture.events)
	require.Len(t, capture.metas, 1)
	require.Equal(t, payload, capture.payloads[0])
	require.Equal(t, sessionarchive.PurposeUpstreamRequest, capture.metas[0].Purpose)
	require.Equal(t, 429, capture.metas[0].UpstreamStatus)
	require.Equal(t, int64(len(payload)), capture.observedBytes[0])
}

func TestCaptureSessionArchiveHTTPAttemptUpdateDoesNotDuplicatePayload(t *testing.T) {
	capture := &recordingSessionArchiveAttemptCapture{}
	base := sessionarchive.CaptureMeta{Policy: sessionarchive.ResolvedPolicy{Enabled: true, CaptureUpstreamRequest: true}}
	captureSessionArchiveHTTPAttempt(capture, base, service.HTTPUpstreamAttemptEvent{
		AttemptNo: 1, UpdateOnly: true, Status: "failed", ErrorClass: "protocol_error",
		Payload: []byte(`{"must":"not-be-stored-again"}`), ObservedBytes: 4096,
	})

	require.Len(t, capture.events, 1)
	require.Empty(t, capture.metas)
	require.Equal(t, "failed", capture.events[0].Meta.Status)
}
