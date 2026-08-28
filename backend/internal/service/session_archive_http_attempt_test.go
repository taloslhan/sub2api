package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPUpstreamAttemptObserverNumbersRetriesWithoutReadingPayloadByDefault(t *testing.T) {
	var events []HTTPUpstreamAttemptEvent
	ctx := WithHTTPUpstreamAttemptObserver(context.Background(), false, 1024, "messages", func(event HTTPUpstreamAttemptEvent) {
		events = append(events, event)
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/messages", bytes.NewReader([]byte(`{"model":"claude"}`)))
	require.NoError(t, err)

	RecordHTTPUpstreamAttempt(request, 41, "tls_fingerprint", time.Now(), &http.Response{StatusCode: 503, Header: http.Header{"X-Request-Id": []string{"req-1"}}}, nil)
	RecordHTTPUpstreamAttempt(request, 42, "tls_fingerprint", time.Now(), &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"req-2"}}}, nil)

	require.Len(t, events, 2)
	require.Equal(t, []int{1, 2}, []int{events[0].AttemptNo, events[1].AttemptNo})
	require.Equal(t, []int64{41, 42}, []int64{events[0].AccountID, events[1].AccountID})
	require.Equal(t, "failed", events[0].Status)
	require.Equal(t, "completed", events[1].Status)
	require.Empty(t, events[0].Payload)
	require.Empty(t, events[1].Payload)
}

func TestHTTPUpstreamAttemptObserverReadsReplayablePayloadOnlyWhenEnabled(t *testing.T) {
	var event HTTPUpstreamAttemptEvent
	ctx := WithHTTPUpstreamAttemptObserver(context.Background(), true, 1024, "responses", func(captured HTTPUpstreamAttemptEvent) {
		event = captured
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/responses", bytes.NewReader([]byte(`{"input":"hello"}`)))
	require.NoError(t, err)

	RecordHTTPUpstreamAttempt(request, 7, "http", time.Now(), nil, errors.New("connection refused"))

	require.Equal(t, []byte(`{"input":"hello"}`), event.Payload)
	require.Equal(t, "failed", event.Status)
	require.Equal(t, "transport_error", event.ErrorClass)
}

func TestHTTPUpstreamAttemptObserverCorrectsHTTP200SemanticFailureBeforeNextAccount(t *testing.T) {
	var events []HTTPUpstreamAttemptEvent
	ctx := WithHTTPUpstreamAttemptObserver(context.Background(), false, 1024, "responses", func(event HTTPUpstreamAttemptEvent) {
		events = append(events, event)
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/responses", bytes.NewReader([]byte(`{"input":"hello"}`)))
	require.NoError(t, err)

	RecordHTTPUpstreamAttempt(request, 7, "http", time.Now(), &http.Response{StatusCode: 200, Header: make(http.Header)}, nil)
	FinalizeLatestHTTPUpstreamAttempt(ctx, errors.New("SSE terminated before response.completed"))
	RecordHTTPUpstreamAttempt(request, 8, "http", time.Now(), &http.Response{StatusCode: 200, Header: make(http.Header)}, nil)
	FinalizeLatestHTTPUpstreamAttempt(ctx, nil)

	require.Len(t, events, 4)
	require.Equal(t, []int{1, 1, 2, 2}, []int{events[0].AttemptNo, events[1].AttemptNo, events[2].AttemptNo, events[3].AttemptNo})
	require.Equal(t, "completed", events[0].Status)
	require.True(t, events[1].UpdateOnly)
	require.Equal(t, "failed", events[1].Status)
	require.Equal(t, "protocol_error", events[1].ErrorClass)
	require.Equal(t, "completed", events[3].Status)
	require.True(t, events[3].UpdateOnly)
}

func TestHTTPUpstreamAttemptObserverUsesContentLengthForTruncatedObservation(t *testing.T) {
	var event HTTPUpstreamAttemptEvent
	ctx := WithHTTPUpstreamAttemptObserver(context.Background(), true, 3, "messages", func(captured HTTPUpstreamAttemptEvent) {
		event = captured
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/v1/messages", bytes.NewReader([]byte("abcdefghij")))
	require.NoError(t, err)
	request.ContentLength = 10

	RecordHTTPUpstreamAttempt(request, 9, "http", time.Now(), &http.Response{StatusCode: 200, Header: make(http.Header)}, nil)

	require.Equal(t, []byte("abcd"), event.Payload)
	require.Equal(t, int64(10), event.ObservedBytes)
}
