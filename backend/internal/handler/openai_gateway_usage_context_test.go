package handler

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "request-456")
	parent = context.WithValue(parent, ctxkey.CorrelationRequestID, "correlation-789")

	var gotClientRequestID string
	var gotRequestID string
	var gotCorrelationRequestID string
	h := &GatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
		gotCorrelationRequestID, _ = ctx.Value(ctxkey.CorrelationRequestID).(string)
	})

	require.Equal(t, "client-request-123", gotClientRequestID)
	require.Equal(t, "request-456", gotRequestID)
	require.Equal(t, "correlation-789", gotCorrelationRequestID)
}

func TestOpenAISubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "openai-client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "openai-request-456")
	parent = context.WithValue(parent, ctxkey.CorrelationRequestID, "openai-correlation-789")

	var gotClientRequestID string
	var gotRequestID string
	var gotCorrelationRequestID string
	h := &OpenAIGatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
		gotCorrelationRequestID, _ = ctx.Value(ctxkey.CorrelationRequestID).(string)
	})

	require.Equal(t, "openai-client-request-123", gotClientRequestID)
	require.Equal(t, "openai-request-456", gotRequestID)
	require.Equal(t, "openai-correlation-789", gotCorrelationRequestID)
}

func TestOpenAIWSTurnCorrelationsAreStablePerTurnAndDistinctAcrossTurns(t *testing.T) {
	var correlations openAIWSTurnCorrelations
	first := correlations.id(1)
	require.NotEmpty(t, first)
	require.Equal(t, first, correlations.id(1), "同一 turn 的账号重试必须复用关联 ID")
	second := correlations.id(2)
	require.NotEmpty(t, second)
	require.NotEqual(t, first, second, "不同逻辑 turn 必须使用独立关联 ID")
	require.Equal(t, second, service.CorrelationRequestIDFromContext(correlations.context(context.Background(), 2)))
}
