package main

// CAPYBARA-PATCH: 锁定修数 CLI 的旧值反解、双向修正与值收敛幂等性。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newRepairerForTest(t *testing.T) *repairer {
	t.Helper()
	dataDir := t.TempDir()
	fallbackFile := filepath.Join(dataDir, "fallback.json")
	require.NoError(t, os.WriteFile(fallbackFile, []byte(`{
		"gpt-5.6-sol": {
			"input_cost_per_token": 5e-6,
			"input_cost_per_token_priority": 1e-5,
			"output_cost_per_token": 3e-5,
			"output_cost_per_token_priority": 6e-5,
			"cache_creation_input_token_cost": 6.25e-6,
			"cache_creation_input_token_cost_priority": 1.25e-5,
			"cache_read_input_token_cost": 5e-7,
			"cache_read_input_token_cost_priority": 1e-6,
			"long_context_input_token_threshold": 272000,
			"long_context_input_cost_multiplier": 2,
			"long_context_output_cost_multiplier": 1.5,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`), 0o600))
	cfg := &config.Config{Pricing: config.PricingConfig{
		DataDir:      dataDir,
		FallbackFile: fallbackFile,
	}}
	pricing := service.NewPricingService(cfg, nil)
	require.NoError(t, pricing.Initialize())
	t.Cleanup(pricing.Stop)
	billing := service.NewBillingService(cfg, pricing)
	return &repairer{
		billingService: billing,
		resolver:       service.NewModelPricingResolver(nil, billing),
		groupCache:     make(map[int64]*service.Group),
	}
}

func TestRepairPlanConvergesFastSubscriptionCost(t *testing.T) {
	r := newRepairerForTest(t)
	tier := "priority"
	row := logRow{
		id:             1,
		model:          "gpt-5.6-sol",
		serviceTier:    &tier,
		tokens:         service.UsageTokens{InputTokens: 100, OutputTokens: 20},
		rateMultiplier: 1,
	}
	candidate := r.calculateCandidate(context.Background(), &row, nil, row.model)
	require.NoError(t, candidate.err)
	require.Greater(t, candidate.new.TotalCost, candidate.old.TotalCost)
	row.totalCost = candidate.old.TotalCost
	row.actualCost = candidate.old.ActualCost

	plan, err := r.plan(context.Background(), &row)
	require.NoError(t, err)
	require.False(t, plan.drift)
	require.True(t, plan.needsChange)
	require.False(t, plan.alreadyNew)
	require.InDelta(t, candidate.new.TotalCost, plan.newCost.TotalCost, 1e-12)

	row.totalCost = plan.newCost.TotalCost
	row.actualCost = plan.newCost.ActualCost
	converged, err := r.plan(context.Background(), &row)
	require.NoError(t, err)
	require.False(t, converged.drift)
	require.False(t, converged.needsChange)
	require.True(t, converged.alreadyNew)
}

func TestRepairPlanHandlesLongContextDecreaseAndUnchangedRows(t *testing.T) {
	r := newRepairerForTest(t)

	t.Run("standard over threshold decreases", func(t *testing.T) {
		row := logRow{
			id:             2,
			model:          "gpt-5.6-sol",
			tokens:         service.UsageTokens{InputTokens: 272_001, OutputTokens: 1_000},
			rateMultiplier: 1,
		}
		candidate := r.calculateCandidate(context.Background(), &row, nil, row.model)
		require.NoError(t, candidate.err)
		require.Less(t, candidate.new.TotalCost, candidate.old.TotalCost)
		row.totalCost = candidate.old.TotalCost
		row.actualCost = candidate.old.ActualCost

		plan, err := r.plan(context.Background(), &row)
		require.NoError(t, err)
		require.True(t, plan.needsChange)
		require.False(t, plan.drift)
	})

	t.Run("standard below threshold is skipped", func(t *testing.T) {
		row := logRow{
			id:             3,
			model:          "gpt-5.6-sol",
			tokens:         service.UsageTokens{InputTokens: 100, OutputTokens: 20},
			rateMultiplier: 1,
		}
		candidate := r.calculateCandidate(context.Background(), &row, nil, row.model)
		require.NoError(t, candidate.err)
		require.InDelta(t, candidate.old.TotalCost, candidate.new.TotalCost, 1e-12)
		row.totalCost = candidate.old.TotalCost
		row.actualCost = candidate.old.ActualCost

		plan, err := r.plan(context.Background(), &row)
		require.NoError(t, err)
		require.False(t, plan.needsChange)
		require.False(t, plan.drift)
	})
}
