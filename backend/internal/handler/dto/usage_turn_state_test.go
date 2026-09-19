package dto

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestUsageTurnStateAdminOnlyAndByteLengths(t *testing.T) {
	request, response, transport := "中文", strings.Repeat("r", 312), "http"
	log := &service.UsageLog{UpstreamRequestTurnState: &request, UpstreamResponseTurnState: &response, TurnStateTransport: &transport}
	admin := UsageLogFromServiceAdmin(log)
	require.Equal(t, 6, *admin.UpstreamRequestTurnStateLength)
	require.Equal(t, 312, *admin.UpstreamResponseTurnStateLength)
	require.Equal(t, request, *admin.UpstreamRequestTurnState)
	raw, err := json.Marshal(UsageLogFromService(log))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "turn_state")
	require.NotContains(t, string(raw), request)
	empty := UsageLogFromServiceAdmin(&service.UsageLog{})
	require.Nil(t, empty.TurnStateTransport)
	require.Nil(t, empty.UpstreamRequestTurnStateLength)
	require.Nil(t, empty.UpstreamResponseTurnStateLength)
}
