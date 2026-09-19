package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageTurnStateHTTPFinalAttemptAndAccountGuard(t *testing.T) {
	c, _ := newTurnStateTestContext(t, 7, "usage-state")
	svc := &OpenAIGatewayService{}
	first, second := &Account{ID: 1}, &Account{ID: 2}
	svc.noteOpenAICodexTurnStateProvenance(c, first)
	req := httptest.NewRequest("POST", "/responses", nil)
	req.Header.Set(openAICodexTurnStateHeader, strings.Repeat("a", 292))
	initial := openAIHTTPUsageTurnState(&http.Response{Request: req, Header: http.Header{"X-Codex-Turn-State": {strings.Repeat("b", 312)}}})
	require.Len(t, initial.Request, 292)
	require.Len(t, initial.Response, 312)

	// 换号后的实际出站头已经剥离；响应缺失不能沿用上次签发。
	svc.guardOpenAICodexTurnStateEcho(c, second, req.Header)
	final := openAIHTTPUsageTurnState(&http.Response{Request: req, Header: http.Header{}})
	log := &UsageLog{}
	applyUsageTurnState(log, final)
	require.Equal(t, "http", *log.TurnStateTransport)
	require.Nil(t, log.UpstreamRequestTurnState)
	require.Nil(t, log.UpstreamResponseTurnState)
	require.Nil(t, log.TurnStateConnectionReused)
	require.Len(t, initial.Request, 292, "earlier snapshot must not mutate")
	require.Len(t, initial.Response, 312)
}

func TestUsageTurnStateWSConnectionSnapshot(t *testing.T) {
	conn := &openAIWSConn{
		handshakeRequestTurnState: strings.Repeat("a", 292),
		handshakeHeaders:          http.Header{"X-Codex-Turn-State": {strings.Repeat("b", 312)}},
	}
	lease := &openAIWSConnLease{conn: conn}
	first := lease.usageTurnState()
	require.False(t, *first.ConnectionReused)
	second := lease.usageTurnState()
	require.True(t, *second.ConnectionReused)
	require.False(t, *first.ConnectionReused, "queued first result must remain immutable")
	require.Equal(t, first.Request, second.Request)
	require.Equal(t, first.Response, second.Response)
	require.Equal(t, "ws", first.Transport)

	pooled := (&openAIWSConnLease{conn: conn, reused: true}).usageTurnState()
	require.True(t, *pooled.ConnectionReused)
	// 重连/换号不能继承上一连接的快照。
	fresh := (&openAIWSConnLease{conn: &openAIWSConn{}}).usageTurnState()
	require.False(t, *fresh.ConnectionReused)
	require.Empty(t, fresh.Request)
	require.Empty(t, fresh.Response)
}

func TestUsageTurnStateHTTPMissingAndResponseOnly(t *testing.T) {
	require.Nil(t, openAIHTTPUsageTurnState(nil))
	log := &UsageLog{}
	applyUsageTurnState(log, nil)
	require.Nil(t, log.TurnStateTransport)
	req := httptest.NewRequest("POST", "/responses", nil)
	state := openAIHTTPUsageTurnState(&http.Response{Request: req, Header: http.Header{"X-Codex-Turn-State": {"response-only"}}})
	applyUsageTurnState(log, state)
	require.Nil(t, log.UpstreamRequestTurnState)
	require.Equal(t, "response-only", *log.UpstreamResponseTurnState)
}
