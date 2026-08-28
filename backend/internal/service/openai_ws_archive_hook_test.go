package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmitOpenAIWSArchiveEventIsFailOpenAndSynchronous(t *testing.T) {
	payload := []byte(`{"type":"response.create"}`)
	hooks := &OpenAIWSIngressHooks{
		TurnCorrelationRequestID: func(turn int) string { return "corr-turn-1" },
		ArchiveEvent: func(event OpenAIWSArchiveEvent) {
			require.Equal(t, "corr-turn-1", event.CorrelationRequestID)
			require.Equal(t, payload, event.Payload)
			panic("collector adapter failure")
		},
	}

	require.NotPanics(t, func() {
		emitOpenAIWSArchiveEvent(hooks, OpenAIWSArchiveEvent{
			Kind: OpenAIWSArchiveRawFrame, Turn: 1, Payload: payload,
		})
	})
	require.Equal(t, byte('{'), payload[0])
}
