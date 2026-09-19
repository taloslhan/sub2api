//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageLog_TurnStatePersistence(t *testing.T) {
	ctx := context.Background()
	migration, err := migrations.FS.ReadFile("239_usage_log_turn_state.sql")
	require.NoError(t, err)
	// 完整迁移已由 harness 应用，再执行验证幂等。
	_, err = integrationDB.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	client := testEntClient(t)
	repo := newUsageLogRepositoryWithSQL(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: "state-" + uuid.NewString() + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-state-" + uuid.NewString(), Name: "state"})
	account := mustCreateAccount(t, client, &service.Account{Name: "state-" + uuid.NewString()})
	request, response, transport, reused := strings.Repeat("a", 292), strings.Repeat("b", 312), "ws", true
	for _, hasState := range []bool{true, false} {
		log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: uuid.NewString(), Model: "gpt-6-astra", CreatedAt: time.Now().UTC()}
		if hasState {
			log.UpstreamRequestTurnState = &request
			log.UpstreamResponseTurnState = &response
			log.TurnStateTransport = &transport
			log.TurnStateConnectionReused = &reused
		}
		_, err := repo.Create(ctx, log)
		require.NoError(t, err)
		got, err := repo.GetByID(ctx, log.ID)
		require.NoError(t, err)
		require.Equal(t, log.UpstreamRequestTurnState, got.UpstreamRequestTurnState)
		require.Equal(t, log.UpstreamResponseTurnState, got.UpstreamResponseTurnState)
		require.Equal(t, log.TurnStateTransport, got.TurnStateTransport)
		require.Equal(t, log.TurnStateConnectionReused, got.TurnStateConnectionReused)
	}
	t.Run("batch_and_best_effort", func(t *testing.T) {
		log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID,
			RequestID: uuid.NewString(), Model: "gpt-6-astra", CreatedAt: time.Now().UTC(),
			UpstreamRequestTurnState: &request, UpstreamResponseTurnState: &response,
			TurnStateTransport: &transport, TurnStateConnectionReused: &reused}
		key := usageLogBatchKey(log.RequestID, log.APIKeyID)
		prepared := prepareUsageLogInsert(log)
		_, states, _, err := repo.batchInsertUsageLogs(integrationDB, []string{key}, map[string]usageLogInsertPrepared{key: prepared})
		require.NoError(t, err)
		got, err := repo.GetByID(ctx, states[key].ID)
		require.NoError(t, err)
		require.Equal(t, log.UpstreamRequestTurnState, got.UpstreamRequestTurnState)
		require.Equal(t, log.UpstreamResponseTurnState, got.UpstreamResponseTurnState)
		require.Equal(t, log.TurnStateConnectionReused, got.TurnStateConnectionReused)

		log.RequestID = uuid.NewString()
		query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepareUsageLogInsert(log)})
		_, err = integrationDB.ExecContext(ctx, query, args...)
		require.NoError(t, err)
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT id FROM usage_logs WHERE request_id=$1", log.RequestID).Scan(&id))
		got, err = repo.GetByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, log.UpstreamRequestTurnState, got.UpstreamRequestTurnState)
		require.Equal(t, log.UpstreamResponseTurnState, got.UpstreamResponseTurnState)
	})

}
