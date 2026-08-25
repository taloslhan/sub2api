//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLog_UpstreamModelMismatchFilterAndPartialIndex(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)

	user := mustCreateUser(t, client, &service.User{Email: "model-audit@test.com"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-model-audit", Name: "model-audit"})
	account := mustCreateAccount(t, client, &service.Account{Name: "model-audit-account"})
	now := time.Now().UTC()
	responseModel := "gpt-5.4"
	for _, mismatch := range []bool{true, false} {
		mismatchValue := mismatch
		_, err := repo.Create(ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID,
			Model: "gpt-5.5", InputTokens: 1, OutputTokens: 1,
			UpstreamResponseModel: &responseModel, UpstreamModelMismatch: &mismatchValue,
			CreatedAt: now,
		})
		require.NoError(t, err)
	}

	start := now.Add(-time.Hour)
	end := now.Add(time.Hour)
	trueValue := true
	stats, err := repo.GetStatsWithFilters(ctx, usagestats.UsageLogFilters{
		UserID: user.ID, StartTime: &start, EndTime: &end, UpstreamModelMismatch: &trueValue,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.TotalRequests)
	require.Equal(t, []usagestats.EndpointStat{{
		Endpoint: "unknown", Requests: 1, TotalTokens: 2,
	}}, stats.Endpoints)
	require.Equal(t, []usagestats.EndpointStat{{
		Endpoint: "unknown", Requests: 1, TotalTokens: 2,
	}}, stats.UpstreamEndpoints)
	require.Equal(t, []usagestats.EndpointStat{{
		Endpoint: "unknown -> unknown", Requests: 1, TotalTokens: 2,
	}}, stats.EndpointPaths)

	trend, err := repo.GetUsageTrendWithUsageFilters(ctx, start, end, "hour", usagestats.UsageLogFilters{
		UserID: user.ID, UpstreamModelMismatch: &trueValue,
	})
	require.NoError(t, err)
	require.Len(t, trend, 1)
	require.Equal(t, int64(1), trend[0].Requests)

	_, err = tx.ExecContext(ctx, "SET LOCAL enable_seqscan = off")
	require.NoError(t, err)
	assertPlanUsesIndex := func(query, indexName string, args ...any) {
		rows, queryErr := tx.QueryContext(ctx, query, args...)
		require.NoError(t, queryErr)
		var planLines []string
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			planLines = append(planLines, line)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		require.Contains(t, strings.Join(planLines, "\n"), indexName)
	}
	assertPlanUsesIndex(`
EXPLAIN (COSTS OFF)
SELECT id
FROM usage_logs
WHERE upstream_model_mismatch IS TRUE
ORDER BY created_at DESC, id DESC
LIMIT 100
`, usageLogsUpstreamModelMismatchIndex)
	assertPlanUsesIndex(`
EXPLAIN (COSTS OFF)
SELECT id
FROM usage_logs
WHERE COALESCE(NULLIF(TRIM(requested_model), ''), model) = $1
  AND created_at >= $2 AND created_at < $3
ORDER BY created_at DESC, id DESC
LIMIT 100
`, usageLogsEffectiveRequestedModelIndex, "gpt-5.5", start, end)
	assertPlanUsesIndex(`
EXPLAIN (COSTS OFF)
SELECT id
FROM usage_logs
WHERE COALESCE(NULLIF(TRIM(upstream_model), ''), model) = $1
  AND created_at >= $2 AND created_at < $3
ORDER BY created_at DESC, id DESC
LIMIT 100
`, usageLogsEffectiveUpstreamModelIndex, "gpt-5.5", start, end)
}

func TestUsageLog_GetStatsWithFilters_AggregatesAndEndpoints(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)

	user := mustCreateUser(t, client, &service.User{Email: "stats@test.com"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-stats-1", Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: "acc-stats"})

	now := time.Now().UTC()
	inboundEndpoint := "/v1/messages"
	upstreamEndpoint := "/v1/responses"
	for i := 0; i < 3; i++ {
		_, err := repo.Create(ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID,
			Model: "claude-3", InputTokens: 2, OutputTokens: 3,
			CacheCreationTokens: 4, CacheReadTokens: 5,
			TotalCost: 0.5, ActualCost: 0.4, CreatedAt: now,
			InboundEndpoint: &inboundEndpoint, UpstreamEndpoint: &upstreamEndpoint,
		})
		require.NoError(t, err)
	}

	start := now.Add(-1 * time.Hour)
	end := now.Add(1 * time.Hour)
	// 按本测试创建的 user 维度过滤:集成库为共享实例,其它用 testEntClient 的兄弟测试会留下
	// 已提交的 usage_log 行(含零 token 的失败请求),不限定 user 会把它们计入 TotalRequests。
	stats, err := repo.GetStatsWithFilters(ctx, usagestats.UsageLogFilters{UserID: user.ID, StartTime: &start, EndTime: &end})
	require.NoError(t, err)
	require.Equal(t, int64(3), stats.TotalRequests)
	require.Equal(t, int64(6), stats.TotalInputTokens)
	require.Equal(t, int64(9), stats.TotalOutputTokens)
	require.Equal(t, int64(27), stats.TotalCacheTokens)
	require.Equal(t, int64(12), stats.TotalCacheCreationTokens)
	require.Equal(t, int64(15), stats.TotalCacheReadTokens)
	require.InDelta(t, 1.2, stats.TotalActualCost, 1e-9)
	require.NotEmpty(t, stats.Endpoints)
	require.NotEmpty(t, stats.UpstreamEndpoints)
	require.NotEmpty(t, stats.EndpointPaths)
}

// CAPYBARA-PATCH: 用量筛选区间输出吞吐与首 Token 平均
func TestUsageLog_GetStatsWithFilters_ThroughputAndFirstToken(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)

	user := mustCreateUser(t, client, &service.User{Email: "throughput@test.com"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-throughput", Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: "acc-throughput"})

	now := time.Now().UTC()
	intPtr := func(v int) *int { return &v }
	create := func(model string, outputTokens int, durationMs, firstTokenMs *int, createdAt time.Time) {
		t.Helper()
		_, err := repo.Create(ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID,
			Model: model, InputTokens: 1, OutputTokens: outputTokens,
			DurationMs: durationMs, FirstTokenMs: firstTokenMs,
			CreatedAt: createdAt,
		})
		require.NoError(t, err)
	}

	statsFor := func(filters usagestats.UsageLogFilters) *usagestats.UsageStats {
		t.Helper()
		filters.UserID = user.ID
		stats, err := repo.GetStatsWithFilters(ctx, filters)
		require.NoError(t, err)
		return stats
	}

	start := now.Add(-1 * time.Hour)
	end := now.Add(1 * time.Hour)

	t.Run("单条记录锁定 tok/s 口径", func(t *testing.T) {
		// 100 output tokens / 2000ms = 50 tok/s
		create("speed-single", 100, intPtr(2000), intPtr(300), now)

		stats := statsFor(usagestats.UsageLogFilters{
			Model: "speed-single", StartTime: &start, EndTime: &end,
		})
		require.Equal(t, int64(1), stats.TotalRequests)
		require.NotNil(t, stats.AverageOutputTokensPerSecond)
		require.InDelta(t, 50.0, *stats.AverageOutputTokensPerSecond, 1e-9)
		require.Equal(t, int64(1), stats.OutputTokensPerSecondSamples)
		require.NotNil(t, stats.AverageFirstTokenMs)
		require.InDelta(t, 300.0, *stats.AverageFirstTokenMs, 1e-9)
		require.Equal(t, int64(1), stats.FirstTokenMsSamples)
	})

	t.Run("逐请求算术平均而非总量加权", func(t *testing.T) {
		// 10 tok/s(10 tokens / 1000ms) 与 30 tok/s(900 tokens / 30000ms)。
		// 算术平均 = 20；按总量加权 = 910000/31000 ≈ 29.35，两者刻意不等。
		create("speed-avg", 10, intPtr(1000), nil, now)
		create("speed-avg", 900, intPtr(30000), nil, now)

		stats := statsFor(usagestats.UsageLogFilters{
			Model: "speed-avg", StartTime: &start, EndTime: &end,
		})
		require.Equal(t, int64(2), stats.TotalRequests)
		require.NotNil(t, stats.AverageOutputTokensPerSecond)
		require.InDelta(t, 20.0, *stats.AverageOutputTokensPerSecond, 1e-9)
		require.Equal(t, int64(2), stats.OutputTokensPerSecondSamples)
		require.Nil(t, stats.AverageFirstTokenMs)
		require.Equal(t, int64(0), stats.FirstTokenMsSamples)
	})

	t.Run("无效时长与零输出不进入速度样本", func(t *testing.T) {
		create("speed-invalid", 100, intPtr(2000), intPtr(100), now) // 唯一有效样本:50 tok/s
		create("speed-invalid", 100, nil, nil, now)                  // duration_ms NULL
		create("speed-invalid", 100, intPtr(0), nil, now)            // duration_ms = 0
		create("speed-invalid", 100, intPtr(-500), nil, now)         // duration_ms 为负
		create("speed-invalid", 0, intPtr(1000), nil, now)           // output_tokens = 0

		stats := statsFor(usagestats.UsageLogFilters{
			Model: "speed-invalid", StartTime: &start, EndTime: &end,
		})
		require.Equal(t, int64(5), stats.TotalRequests)
		require.NotNil(t, stats.AverageOutputTokensPerSecond)
		require.InDelta(t, 50.0, *stats.AverageOutputTokensPerSecond, 1e-9)
		require.Equal(t, int64(1), stats.OutputTokensPerSecondSamples)
		require.NotNil(t, stats.AverageFirstTokenMs)
		require.InDelta(t, 100.0, *stats.AverageFirstTokenMs, 1e-9)
		require.Equal(t, int64(1), stats.FirstTokenMsSamples)
	})

	t.Run("首 Token 为 NULL 的记录不参与平均", func(t *testing.T) {
		create("first-token-mixed", 10, intPtr(1000), intPtr(200), now)
		create("first-token-mixed", 10, intPtr(1000), intPtr(400), now)
		create("first-token-mixed", 10, intPtr(1000), nil, now)

		stats := statsFor(usagestats.UsageLogFilters{
			Model: "first-token-mixed", StartTime: &start, EndTime: &end,
		})
		require.Equal(t, int64(3), stats.TotalRequests)
		require.NotNil(t, stats.AverageFirstTokenMs)
		require.InDelta(t, 300.0, *stats.AverageFirstTokenMs, 1e-9)
		require.Equal(t, int64(2), stats.FirstTokenMsSamples)
		require.Equal(t, int64(3), stats.OutputTokensPerSecondSamples)
	})

	t.Run("全部样本无效时平均值为 nil", func(t *testing.T) {
		create("all-invalid", 0, nil, nil, now)
		create("all-invalid", 0, intPtr(0), nil, now)

		stats := statsFor(usagestats.UsageLogFilters{
			Model: "all-invalid", StartTime: &start, EndTime: &end,
		})
		require.Equal(t, int64(2), stats.TotalRequests)
		require.Nil(t, stats.AverageOutputTokensPerSecond)
		require.Equal(t, int64(0), stats.OutputTokensPerSecondSamples)
		require.Nil(t, stats.AverageFirstTokenMs)
		require.Equal(t, int64(0), stats.FirstTokenMsSamples)
	})

	t.Run("时间范围过滤改变新指标", func(t *testing.T) {
		older := now.Add(-45 * time.Minute)
		create("time-scoped", 100, intPtr(1000), intPtr(500), older) // 100 tok/s
		create("time-scoped", 100, intPtr(5000), intPtr(700), now)   // 20 tok/s

		full := statsFor(usagestats.UsageLogFilters{
			Model: "time-scoped", StartTime: &start, EndTime: &end,
		})
		require.Equal(t, int64(2), full.TotalRequests)
		require.InDelta(t, 60.0, *full.AverageOutputTokensPerSecond, 1e-9)
		require.Equal(t, int64(2), full.OutputTokensPerSecondSamples)
		require.InDelta(t, 600.0, *full.AverageFirstTokenMs, 1e-9)
		require.Equal(t, int64(2), full.FirstTokenMsSamples)

		recentStart := now.Add(-5 * time.Minute)
		recent := statsFor(usagestats.UsageLogFilters{
			Model: "time-scoped", StartTime: &recentStart, EndTime: &end,
		})
		require.Equal(t, int64(1), recent.TotalRequests)
		require.InDelta(t, 20.0, *recent.AverageOutputTokensPerSecond, 1e-9)
		require.Equal(t, int64(1), recent.OutputTokensPerSecondSamples)
		require.InDelta(t, 700.0, *recent.AverageFirstTokenMs, 1e-9)
		require.Equal(t, int64(1), recent.FirstTokenMsSamples)
	})
}
