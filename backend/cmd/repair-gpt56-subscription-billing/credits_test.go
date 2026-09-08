package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func creditRowForTest(t *testing.T, r *creditsRepair, model, tier string) map[string]any {
	t.Helper()
	cost, err := r.billingService.CalculateCostUnified(service.CostInput{Model: model, ServiceTier: tier, RateMultiplier: 1,
		Tokens: service.UsageTokens{InputTokens: 100000, CacheReadTokens: 50000, OutputTokens: 10000}, OpenAIBillingProfile: r.profile})
	require.NoError(t, err)
	row := creditsCostFields(cost, 0)
	row["account_stats_cost"] = nil
	row["id"], row["user_id"], row["account_id"] = float64(1), float64(2), float64(4)
	row["model"], row["service_tier"], row["created_at"] = model, tier, creditsFrom
	row["rate_multiplier"] = float64(1)
	row["input_tokens"], row["cache_read_tokens"], row["output_tokens"] = float64(100000), float64(50000), float64(10000)
	return row
}

func TestCreditsRepairConvergesAndComparesAllFields(t *testing.T) {
	r := &creditsRepair{repairer: newRepairerForTest(t), profile: service.OpenAIBillingProfileChatGPTSubscription, accountID: 4}
	for _, tt := range []struct{ name, model, tier string }{
		{"fast Astra increase", "gpt-6-astra", "priority"},
		{"Sol decrease", "gpt-5.6-sol", ""},
		{"Daybreak alias", "gpt-daybreak-blue-latest", "priority"},
		{"stats only", "gpt-6-astra", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := creditRowForTest(t, r, tt.model, tt.tier)
			raw, err := json.Marshal(row)
			require.NoError(t, err)
			plan, selected, err := r.planCredits(context.Background(), raw)
			require.NoError(t, err)
			require.True(t, selected)
			require.NotNil(t, plan)
			for key, value := range plan.After {
				row[key] = value
			}
			raw, err = json.Marshal(row)
			require.NoError(t, err)
			plan, selected, err = r.planCredits(context.Background(), raw)
			require.NoError(t, err)
			require.True(t, selected)
			require.Nil(t, plan)
			// 总价与统计没变，但成本分项坏掉，仍须命中修正。
			row["input_cost"] = float64(999)
			raw, err = json.Marshal(row)
			require.NoError(t, err)
			plan, _, err = r.planCredits(context.Background(), raw)
			require.NoError(t, err)
			require.NotNil(t, plan)
		})
	}
}

func TestCreditsRepairVerifiesDiscountAndRejectsDrift(t *testing.T) {
	r := &creditsRepair{repairer: newRepairerForTest(t), profile: service.OpenAIBillingProfileChatGPTSubscription, accountID: 4}
	row := creditRowForTest(t, r, "gpt-6-astra", "priority")
	actualCost, ok := row["actual_cost"].(float64)
	require.True(t, ok)
	row["actual_cost"] = actualCost / 2
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	plan, _, err := r.planCredits(context.Background(), raw)
	require.NoError(t, err)
	require.True(t, plan.FreeFast)
	require.InDelta(t, 1.55, plan.After["actual_cost"], 1e-10)
	row["actual_cost"] = float64(123)
	raw, err = json.Marshal(row)
	require.NoError(t, err)
	_, _, err = r.planCredits(context.Background(), raw)
	require.ErrorContains(t, err, "cannot be reproduced")
}

func TestCreditsSnapshotComparisonKeepsTokensAndNestedMetadata(t *testing.T) {
	before := map[string]any{"actual_cost": 1.0, "input_tokens": 10.0, "image_size_breakdown": map[string]any{"size": 1.0}}
	current := map[string]any{"actual_cost": 1.0, "input_tokens": 10.0, "image_size_breakdown": map[string]any{"size": 1.0}}
	require.True(t, sameCreditsFields(current, before))
	current["input_tokens"] = 11.0
	require.False(t, sameCreditsFields(current, before))
	require.True(t, creditsAmountEqual(1.12345678901, 1.123456789))
	require.False(t, creditsAmountEqual(1.1234567891, 1.123456789))
}

func TestCreditsSnapshotTransactionResumeAndDrift(t *testing.T) {
	r := &creditsRepair{repairer: newRepairerForTest(t), profile: service.OpenAIBillingProfileChatGPTSubscription, accountID: 4}
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	r.db = db
	row := creditRowForTest(t, r, "gpt-6-astra", "priority")
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	plan, _, err := r.planCredits(context.Background(), raw)
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT to_jsonb").WithArgs(plan.ID, int64(4), creditsFrom, creditsTo).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(string(raw)))
	mock.ExpectExec("UPDATE usage_logs SET input_cost").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, r.applyCreditsBatch(context.Background(), []creditsChange{*plan}))
	// 上批提交后进程中断：同一快照再次执行，已写入记录不重复更新。
	for key, value := range plan.After {
		row[key] = value
	}
	after, err := json.Marshal(row)
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT to_jsonb").WithArgs(plan.ID, int64(4), creditsFrom, creditsTo).WillReturnRows(sqlmock.NewRows([]string{"row"}).AddRow(string(after)))
	mock.ExpectCommit()
	require.NoError(t, r.applyCreditsBatch(context.Background(), []creditsChange{*plan}))
	// 配置漂移在任何 UPDATE 之前阻断。
	r.accountGate = !r.accountGate
	mock.ExpectBegin()
	mock.ExpectRollback()
	require.ErrorContains(t, r.applyCreditsBatch(context.Background(), []creditsChange{*plan}), "configuration drift")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAstraCacheDoubleRepairScopeDiscountAndConvergence(t *testing.T) {
	r := &creditsRepair{repairer: newRepairerForTest(t), profile: service.OpenAIBillingProfileChatGPTCredits, accountID: 4, astraCacheDouble: true}
	for _, tier := range []string{"", "priority"} {
		for _, free := range []bool{false, true} {
			if free && tier == "" {
				continue
			}
			row := creditRowForTest(t, r, "gpt-6-astra", tier)
			cacheCost, ok := row["cache_read_cost"].(float64)
			require.True(t, ok)
			oldCache := cacheCost / 2
			row["cache_read_cost"] = oldCache
			totalCost, ok := row["total_cost"].(float64)
			require.True(t, ok)
			row["total_cost"] = totalCost - oldCache
			row["account_stats_cost"] = row["total_cost"]
			row["actual_cost"] = row["total_cost"]
			if free {
				row["actual_cost"] = (totalCost - oldCache) / 2.5
			}
			raw, err := json.Marshal(row)
			require.NoError(t, err)
			plan, selected, err := r.planCredits(context.Background(), raw)
			require.NoError(t, err)
			require.True(t, selected)
			require.NotNil(t, plan)
			require.Equal(t, free, plan.FreeFast)
			require.InDelta(t, oldCache*2, plan.After["cache_read_cost"], 1e-10)
			delta := oldCache
			if free {
				delta /= 2.5
			}
			require.InDelta(t, delta, plan.ActualDelta, 1e-10)
			require.Equal(t, row["input_cost"], plan.After["input_cost"])
			require.Equal(t, row["output_cost"], plan.After["output_cost"])
			for k, v := range plan.After {
				row[k] = v
			}
			raw, err = json.Marshal(row)
			require.NoError(t, err)
			plan, selected, err = r.planCredits(context.Background(), raw)
			require.NoError(t, err)
			require.True(t, selected)
			require.Nil(t, plan)
			row["total_cost"] = 99.0
			raw, err = json.Marshal(row)
			require.NoError(t, err)
			_, _, err = r.planCredits(context.Background(), raw)
			require.ErrorContains(t, err, "cannot be reproduced")
		}
	}
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-daybreak-blue-latest"} {
		raw, err := json.Marshal(creditRowForTest(t, r, model, "priority"))
		require.NoError(t, err)
		plan, selected, err := r.planCredits(context.Background(), raw)
		require.NoError(t, err)
		require.False(t, selected)
		require.Nil(t, plan)
	}
}

func TestCreditsRepairRejectsChangedHistoricalRateCard(t *testing.T) {
	for _, execute := range []bool{false, true} {
		for _, double := range []bool{false, true} {
			err := runCreditsRepair(4, "unused-snapshot.json", "unused-pricing.json", 100, execute, double, false)
			require.ErrorContains(t, err, "requires frozen rate card 2026-09-07.v2")
		}
	}
}

func TestAstraCacheRestorePricesScopeDiscountAndIdempotency(t *testing.T) {
	r := &creditsRepair{repairer: newRepairerForTest(t), profile: service.OpenAIBillingProfileChatGPTCredits, accountID: 4, astraCacheRestore: true}
	from, to := r.window()
	require.Equal(t, "2026-09-08T00:00:00+08:00", from)
	require.Equal(t, "2026-09-09T00:00:00+08:00", to)
	for _, tier := range []string{"", "priority", "fast"} {
		for _, free := range []bool{false, true} {
			if free && tier == "" {
				continue
			}
			for _, multiplier := range []float64{1, .7} {
				row := creditRowForTest(t, r, "gpt-6-astra", tier)
				row["created_at"], row["rate_multiplier"] = from, multiplier
				oldTotal, newTotal, newCache := 1.6, 1.55, .05
				if tier != "" {
					oldTotal *= 2.5
					newTotal *= 2.5
					newCache *= 2.5
				}
				row["cache_read_cost"], row["total_cost"], row["account_stats_cost"] = newCache*2, oldTotal, oldTotal
				oldActual, newActual := oldTotal*multiplier, newTotal*multiplier
				if free {
					oldActual /= 2.5
					newActual /= 2.5
				}
				row["actual_cost"] = oldActual
				raw, err := json.Marshal(row)
				require.NoError(t, err)
				plan, selected, err := r.planCredits(context.Background(), raw)
				require.NoError(t, err)
				require.True(t, selected)
				require.NotNil(t, plan)
				require.Equal(t, free, plan.FreeFast)
				require.InDelta(t, newCache, plan.After["cache_read_cost"], 1e-10)
				require.InDelta(t, newTotal, plan.After["total_cost"], 1e-10)
				require.InDelta(t, newActual, plan.After["actual_cost"], 1e-10)
				require.InDelta(t, newTotal, plan.After["account_stats_cost"], 1e-10)
				require.InDelta(t, newActual-oldActual, plan.ActualDelta, 1e-10)
				require.Equal(t, row["input_cost"], plan.After["input_cost"])
				require.Equal(t, row["output_cost"], plan.After["output_cost"])
				for k, v := range plan.After {
					row[k] = v
				}
				raw, err = json.Marshal(row)
				require.NoError(t, err)
				plan, selected, err = r.planCredits(context.Background(), raw)
				require.NoError(t, err)
				require.True(t, selected)
				require.Nil(t, plan)
				row["total_cost"] = 123.0
				raw, err = json.Marshal(row)
				require.NoError(t, err)
				_, _, err = r.planCredits(context.Background(), raw)
				require.ErrorContains(t, err, "cannot be reproduced")
			}
		}
	}
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-daybreak-blue-latest"} {
		raw, err := json.Marshal(creditRowForTest(t, r, model, ""))
		require.NoError(t, err)
		plan, selected, err := r.planCredits(context.Background(), raw)
		require.NoError(t, err)
		require.False(t, selected)
		require.Nil(t, plan)
	}
	require.ErrorContains(t, runCreditsRepair(4, "unused", "unused", 100, false, true, true), "mutually exclusive")
}
