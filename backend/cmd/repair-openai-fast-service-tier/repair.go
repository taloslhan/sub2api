package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// targetServiceTier 是修正后的目标档位。写死 priority：这是 fast 归一化后的内部值。
const targetServiceTier = "priority"

// selectSQL 选出需要修正的行。所有条件都写死，不做成可配置项——
// 任何一条被放宽都会把不该改的行卷进来。
//
//   - inbound_endpoint 而不是 upstream_endpoint：后者会把入站 /v1/messages 与
//     /v1/chat/completions 派生出的 /v1/responses 混进来（过度包含），同时 compact
//     的深层子路径在 upstream_endpoint 上会保留成 /v1/responses/compact/detail
//     这样的三段值（漏检）。
//   - users.role = 'admin'：本次缺陷影响范围仅限管理员自用流量。
//   - accounts.platform = 'openai' AND accounts.type = 'oauth'：收窄到
//     ChatGPT/Codex 后端账号，只有它们走 service_tier 档位定价。
//   - 档位条件排除已经是 priority 的行（无需修正，同时构成重跑幂等的收敛条件），
//     也排除 flex（方向相反，改成 priority 是多收费）。
//   - billing_mode 条件排除图片、web_search、audio 等按次计费行：它们的成本列
//     不是按 token 公式算出来的，重算会写出错误金额。
const selectSQL = `
	SELECT ul.id,
	       ul.user_id,
	       ul.account_id,
	       ul.group_id,
	       ul.model,
	       COALESCE(ul.upstream_model, ''),
	       ul.service_tier,
	       COALESCE(ul.input_tokens, 0),
	       COALESCE(ul.image_input_tokens, 0),
	       COALESCE(ul.output_tokens, 0),
	       COALESCE(ul.image_output_tokens, 0),
	       COALESCE(ul.cache_creation_tokens, 0),
	       COALESCE(ul.cache_creation_5m_tokens, 0),
	       COALESCE(ul.cache_creation_1h_tokens, 0),
	       COALESCE(ul.cache_read_tokens, 0),
	       COALESCE(ul.image_count, 0),
	       COALESCE(ul.rate_multiplier, 1),
	       COALESCE(ul.long_context_billing_applied, false),
	       ul.created_at,
	       COALESCE(ul.total_cost, 0),
	       COALESCE(ul.actual_cost, 0),
	       ul.account_stats_cost
	FROM usage_logs ul
	JOIN users u    ON u.id = ul.user_id    AND u.role = 'admin'
	JOIN accounts a ON a.id = ul.account_id AND a.platform = 'openai' AND a.type = 'oauth'
	WHERE ul.id > $1
	  AND ul.inbound_endpoint IN ('/v1/responses', '/v1/responses/compact')
	  AND ul.created_at >= $2 AND ul.created_at < $3
	  AND (ul.service_tier IS NULL OR ul.service_tier IN ('default', 'auto', 'scale'))
	  AND COALESCE(ul.billing_mode, 'token') = 'token'
	ORDER BY ul.id ASC
	LIMIT $4`

// updateSQL 写回档位与全部 8 个成本列。
// 只写其中一部分会让明细列之和与 total_cost 对不上，前端按列展示时直接穿帮。
// WHERE 里重复一遍档位条件：作为并发/重跑下的幂等护栏，已经改成 priority 的行不会被再改一次。
const updateSQL = `
	UPDATE usage_logs
	SET service_tier        = $2,
	    input_cost          = $3,
	    image_input_cost    = $4,
	    output_cost         = $5,
	    image_output_cost   = $6,
	    cache_creation_cost = $7,
	    cache_read_cost     = $8,
	    total_cost          = $9,
	    actual_cost         = $10,
	    account_stats_cost  = $11
	WHERE id = $1
	  AND (service_tier IS NULL OR service_tier IN ('default', 'auto', 'scale'))`

// logRow 是一条待修正记录的全部重算输入，全部取自该行自身。
type logRow struct {
	id            int64
	userID        int64
	accountID     int64
	groupID       *int64
	model         string
	upstreamModel string
	serviceTier   *string

	tokens     service.UsageTokens
	imageCount int

	// rateMultiplier 取 usage_logs.rate_multiplier。
	// 不是 account_rate_multiplier——那是独立列，只参与账号配额与 dashboard 的
	// account_cost 口径；也绝不能用 actual_cost / total_cost 反推（total_cost = 0 时除零）。
	rateMultiplier float64

	// longCtxApplied 取 usage_logs.long_context_billing_applied。
	// 长上下文开关是账号级配置且没有历史快照，只能用这个落库的结果标记回填，
	// 不可以读账号当前配置。
	longCtxApplied bool

	createdAt        time.Time
	totalCost        float64
	actualCost       float64
	accountStatsCost *float64
}

// billingModel 是参与计费的模型名。
// 注意：生产路径上真正的 billingModel 还可能被 result.BillingModel /
// input.ChannelMappedModel / input.OriginalModel 覆盖，而这些都没有落库，
// 所以 usage_logs.model 是可用的最佳近似。summary 中的 recompute_drift
// 计数就是用来暴露这种不可还原情况的。
func (r *logRow) billingModel() string {
	if trimmed := strings.TrimSpace(r.model); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(r.upstreamModel)
}

// statsModel 是账号统计定价使用的模型名，复刻 service.applyAccountStatsCost：
// 上游模型优先，为空时回落到请求模型。
func (r *logRow) statsModel() string {
	if trimmed := strings.TrimSpace(r.upstreamModel); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(r.model)
}

func (r *logRow) normalizedOldTier() string {
	if r.serviceTier == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*r.serviceTier))
}

type repairer struct {
	db             *sql.DB
	groupRepo      service.GroupRepository
	channelService *service.ChannelService
	billingService *service.BillingService
	resolver       *service.ModelPricingResolver

	groupCache map[int64]*service.Group
}

func (r *repairer) run(ctx context.Context, from, to time.Time, batchSize int, execute bool) (*summary, error) {
	sum := newSummary()
	var cursor int64
	for {
		batch, err := r.scan(ctx, cursor, from, to, batchSize)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		cursor = batch[len(batch)-1].id

		plans := make([]updatePlan, 0, len(batch))
		for i := range batch {
			plan, err := r.plan(ctx, &batch[i])
			if err != nil {
				return nil, err
			}
			sum.add(&batch[i], plan)
			plans = append(plans, plan)
		}
		if execute {
			n, err := r.apply(ctx, plans)
			if err != nil {
				return nil, err
			}
			sum.updated += n
		}
	}
	return sum, nil
}

func (r *repairer) scan(ctx context.Context, cursor int64, from, to time.Time, batchSize int) ([]logRow, error) {
	rows, err := r.db.QueryContext(ctx, selectSQL, cursor, from, to, batchSize)
	if err != nil {
		return nil, fmt.Errorf("scan usage_logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	batch := make([]logRow, 0, batchSize)
	for rows.Next() {
		var (
			item        logRow
			groupID     sql.NullInt64
			serviceTier sql.NullString
			statsCost   sql.NullFloat64
		)
		if err := rows.Scan(
			&item.id,
			&item.userID,
			&item.accountID,
			&groupID,
			&item.model,
			&item.upstreamModel,
			&serviceTier,
			&item.tokens.InputTokens,
			&item.tokens.ImageInputTokens,
			&item.tokens.OutputTokens,
			&item.tokens.ImageOutputTokens,
			&item.tokens.CacheCreationTokens,
			&item.tokens.CacheCreation5mTokens,
			&item.tokens.CacheCreation1hTokens,
			&item.tokens.CacheReadTokens,
			&item.imageCount,
			&item.rateMultiplier,
			&item.longCtxApplied,
			&item.createdAt,
			&item.totalCost,
			&item.actualCost,
			&statsCost,
		); err != nil {
			return nil, fmt.Errorf("scan usage_logs row: %w", err)
		}
		if groupID.Valid {
			id := groupID.Int64
			item.groupID = &id
		}
		if serviceTier.Valid {
			tier := serviceTier.String
			item.serviceTier = &tier
		}
		if statsCost.Valid {
			cost := statsCost.Float64
			item.accountStatsCost = &cost
		}
		batch = append(batch, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage_logs rows: %w", err)
	}
	return batch, nil
}

// updatePlan 是单行的重算结果与写回决定。
type updatePlan struct {
	id int64

	cost *service.CostBreakdown

	// oldRecomputed 是用同一批输入、按落库时的旧档位重算出来的总额。
	// 它与落库 total_cost 的差异反映「定价文件/渠道配置在落库后发生过变动」，
	// 只用于 dry-run 报告，不参与写回。
	oldRecomputed float64

	statsBranch statsBranch
	// statsCost 是写回 account_stats_cost 的值。nil 表示写 NULL。
	statsCost *float64
	// statsValueDrift 表示按当前配置、用旧档位重算出的账号统计费用与落库值不一致，
	// 说明「该行落库后渠道配置未变更」这个前提可能已经不成立。仅用于报告。
	statsValueDrift bool
}

func (r *repairer) plan(ctx context.Context, row *logRow) (updatePlan, error) {
	group, err := r.group(ctx, row.groupID)
	if err != nil {
		return updatePlan{}, err
	}

	cost, err := r.calculate(ctx, row, group, targetServiceTier)
	if err != nil {
		return updatePlan{}, fmt.Errorf("recompute cost for usage_log id=%d: %w", row.id, err)
	}
	// 渠道配置若已改成按次/图片计费，8 个成本列的语义与 token 公式不同，
	// 这时重算结果不能写回。直接中止而不是猜测。
	if cost.BillingMode != "" && cost.BillingMode != string(service.BillingModeToken) {
		return updatePlan{}, fmt.Errorf(
			"usage_log id=%d: current pricing resolves to billing_mode=%q but the row was recorded as token billing; refusing to rewrite cost columns",
			row.id, cost.BillingMode)
	}

	plan := updatePlan{id: row.id, cost: cost}

	if oldCost, err := r.calculate(ctx, row, group, row.normalizedOldTier()); err == nil && oldCost != nil {
		plan.oldRecomputed = oldCost.TotalCost
	}

	plan.statsBranch, plan.statsCost, plan.statsValueDrift, err = r.resolveStatsCost(ctx, row, cost.TotalCost)
	if err != nil {
		return updatePlan{}, err
	}
	return plan, nil
}

func (r *repairer) calculate(ctx context.Context, row *logRow, group *service.Group, tier string) (*service.CostBreakdown, error) {
	longCtx := row.longCtxApplied
	return r.billingService.CalculateCostUnified(service.CostInput{
		Ctx:                       ctx,
		Model:                     row.billingModel(),
		GroupID:                   row.groupID,
		Group:                     group,
		Tokens:                    row.tokens,
		RequestCount:              1,
		RateMultiplier:            row.rateMultiplier,
		PricingAt:                 row.createdAt,
		ServiceTier:               tier,
		Resolver:                  r.resolver,
		LongContextBillingEnabled: &longCtx,
	})
}

func (r *repairer) group(ctx context.Context, groupID *int64) (*service.Group, error) {
	if groupID == nil {
		return nil, nil
	}
	if cached, ok := r.groupCache[*groupID]; ok {
		return cached, nil
	}
	group, err := r.groupRepo.GetByIDLite(ctx, *groupID)
	if err != nil {
		// group_id 是 ON DELETE SET NULL 的外键，非空就应该查得到。
		// 查不到说明数据状态超出预期，宁可中止也不要按 Group=nil 静默降级——
		// 那会让长上下文与分组定价都按默认值算出错误金额。
		return nil, fmt.Errorf("load group id=%d: %w", *groupID, err)
	}
	r.groupCache[*groupID] = group
	return group, nil
}

func (r *repairer) apply(ctx context.Context, plans []updatePlan) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, updateSQL)
	if err != nil {
		return 0, fmt.Errorf("prepare update: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	var updated int64
	for i := range plans {
		p := &plans[i]
		var statsCost any
		if p.statsCost != nil {
			statsCost = *p.statsCost
		}
		res, err := stmt.ExecContext(ctx,
			p.id,
			targetServiceTier,
			p.cost.InputCost,
			p.cost.ImageInputCost,
			p.cost.OutputCost,
			p.cost.ImageOutputCost,
			p.cost.CacheCreationCost,
			p.cost.CacheReadCost,
			p.cost.TotalCost,
			p.cost.ActualCost,
			statsCost,
		)
		if err != nil {
			return 0, fmt.Errorf("update usage_log id=%d: %w", p.id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rows affected for usage_log id=%d: %w", p.id, err)
		}
		updated += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit batch: %w", err)
	}
	return updated, nil
}
