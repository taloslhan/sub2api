package main

// CAPYBARA-PATCH: 默认 dry-run，并以旧值反解计费模型后收敛写回 9 个成本/标记列。

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const selectSQL = `
	SELECT ul.id,
	       ul.user_id,
	       ul.account_id,
	       ul.group_id,
	       COALESCE(ul.model, ''),
	       COALESCE(ul.upstream_model, ''),
	       COALESCE(ul.requested_model, ''),
	       COALESCE(ul.upstream_response_model, ''),
	       ul.service_tier,
	       COALESCE(ul.input_tokens, 0),
	       COALESCE(ul.image_input_tokens, 0),
	       COALESCE(ul.output_tokens, 0),
	       COALESCE(ul.image_output_tokens, 0),
	       COALESCE(ul.cache_creation_tokens, 0),
	       COALESCE(ul.cache_creation_5m_tokens, 0),
	       COALESCE(ul.cache_creation_1h_tokens, 0),
	       COALESCE(ul.cache_read_tokens, 0),
	       COALESCE(ul.rate_multiplier, 1),
	       ul.created_at,
	       COALESCE(ul.total_cost, 0),
	       COALESCE(ul.actual_cost, 0),
	       ul.account_stats_cost
	FROM usage_logs ul
	JOIN accounts a ON a.id = ul.account_id
	WHERE ul.id > $1
	  AND ul.created_at >= $2 AND ul.created_at < $3
	  AND a.platform = 'openai'
	  AND a.type IN ('oauth', 'setup-token')
	  AND (
	        lower(translate(COALESCE(ul.model, ''), '_', '-'))                   LIKE '%gpt-5.6%'
	     OR lower(translate(COALESCE(ul.upstream_model, ''), '_', '-'))          LIKE '%gpt-5.6%'
	     OR lower(translate(COALESCE(ul.requested_model, ''), '_', '-'))         LIKE '%gpt-5.6%'
	     OR lower(translate(COALESCE(ul.upstream_response_model, ''), '_', '-')) LIKE '%gpt-5.6%'
	     OR lower(translate(COALESCE(ul.model, ''), '_', '-'))                   LIKE '%gpt5.6%'
	     OR lower(translate(COALESCE(ul.upstream_model, ''), '_', '-'))          LIKE '%gpt5.6%'
	     OR lower(translate(COALESCE(ul.requested_model, ''), '_', '-'))         LIKE '%gpt5.6%'
	     OR lower(translate(COALESCE(ul.upstream_response_model, ''), '_', '-')) LIKE '%gpt5.6%'
	  )
	  AND COALESCE(NULLIF(BTRIM(ul.billing_mode), ''), 'token') = 'token'
	  AND COALESCE(ul.image_count, 0) = 0
	  AND COALESCE(ul.video_count, 0) = 0
	ORDER BY ul.id ASC
	LIMIT $4`

const updateSQL = `
	UPDATE usage_logs
	SET input_cost                  = $2,
	    image_input_cost            = $3,
	    output_cost                 = $4,
	    image_output_cost           = $5,
	    cache_creation_cost         = $6,
	    cache_read_cost             = $7,
	    total_cost                  = $8,
	    actual_cost                 = $9,
	    account_stats_cost          = $10,
	    long_context_billing_applied = $11
	WHERE id = $1
	  AND total_cost IS DISTINCT FROM $8`

type logRow struct {
	id        int64
	userID    int64
	accountID int64
	groupID   *int64

	model                 string
	upstreamModel         string
	requestedModel        string
	upstreamResponseModel string
	serviceTier           *string
	tokens                service.UsageTokens
	rateMultiplier        float64
	createdAt             time.Time
	totalCost             float64
	actualCost            float64
	accountStatsCost      *float64
}

func (r *logRow) billingModelCandidates() []string {
	values := []string{r.model, r.upstreamModel, r.requestedModel, r.upstreamResponseModel}
	seen := make(map[string]struct{}, len(values))
	candidates := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, value)
	}
	return candidates
}

func (r *logRow) normalizedServiceTier() string {
	if r.serviceTier == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(*r.serviceTier))
}

func (r *logRow) statsModel() string {
	if model := strings.TrimSpace(r.upstreamModel); model != "" {
		return model
	}
	return strings.TrimSpace(r.model)
}

type repairer struct {
	db             *sql.DB
	groupRepo      service.GroupRepository
	channelService *service.ChannelService
	billingService *service.BillingService
	resolver       *service.ModelPricingResolver
	groupCache     map[int64]*service.Group
}

type candidateCost struct {
	model  string
	source string
	old    *service.CostBreakdown
	new    *service.CostBreakdown
	err    error
}

type updatePlan struct {
	id            int64
	billingModel  string
	pricingSource string
	newCost       *service.CostBreakdown
	needsChange   bool
	alreadyNew    bool
	drift         bool
	unrepairable  bool
	candidates    []candidateCost
	statsBranch   statsBranch
	statsCost     *float64
	statsDrift    bool
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
			plan, planErr := r.plan(ctx, &batch[i])
			if planErr != nil {
				return nil, planErr
			}
			sum.add(&batch[i], plan)
			plans = append(plans, plan)
		}
		if execute {
			updated, applyErr := r.apply(ctx, plans)
			if applyErr != nil {
				return nil, applyErr
			}
			sum.updated += updated
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
		var item logRow
		var groupID sql.NullInt64
		var serviceTier sql.NullString
		var accountStatsCost sql.NullFloat64
		if err := rows.Scan(
			&item.id, &item.userID, &item.accountID, &groupID,
			&item.model, &item.upstreamModel, &item.requestedModel, &item.upstreamResponseModel,
			&serviceTier,
			&item.tokens.InputTokens, &item.tokens.ImageInputTokens,
			&item.tokens.OutputTokens, &item.tokens.ImageOutputTokens,
			&item.tokens.CacheCreationTokens, &item.tokens.CacheCreation5mTokens,
			&item.tokens.CacheCreation1hTokens, &item.tokens.CacheReadTokens,
			&item.rateMultiplier, &item.createdAt, &item.totalCost, &item.actualCost,
			&accountStatsCost,
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
		if accountStatsCost.Valid {
			cost := accountStatsCost.Float64
			item.accountStatsCost = &cost
		}
		batch = append(batch, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage_logs rows: %w", err)
	}
	return batch, nil
}

func (r *repairer) plan(ctx context.Context, row *logRow) (updatePlan, error) {
	group, err := r.group(ctx, row.groupID)
	if err != nil {
		return updatePlan{}, err
	}

	plan := updatePlan{id: row.id}
	var selected *candidateCost
	var best *candidateCost
	bestDiff := math.Inf(1)
	for _, model := range row.billingModelCandidates() {
		candidate := r.calculateCandidate(ctx, row, group, model)
		plan.candidates = append(plan.candidates, candidate)
		if candidate.err != nil || candidate.old == nil || candidate.new == nil {
			continue
		}
		diff := math.Abs(candidate.old.TotalCost - row.totalCost)
		if diff < bestDiff {
			bestDiff = diff
			best = &plan.candidates[len(plan.candidates)-1]
		}
		if nearlyEqual(candidate.old.TotalCost, row.totalCost) {
			selected = &plan.candidates[len(plan.candidates)-1]
			plan.alreadyNew = false
			break
		}
		if selected == nil && nearlyEqual(candidate.new.TotalCost, row.totalCost) {
			selected = &plan.candidates[len(plan.candidates)-1]
			plan.alreadyNew = true
		}
	}
	if selected == nil {
		selected = best
		plan.drift = selected != nil
	}
	if selected == nil {
		plan.unrepairable = true
		return plan, nil
	}

	plan.billingModel = selected.model
	plan.pricingSource = selected.source
	plan.newCost = selected.new
	plan.needsChange = !nearlyEqual(row.totalCost, selected.new.TotalCost)
	if !nearlyEqual(row.totalCost, selected.old.TotalCost) && !nearlyEqual(row.totalCost, selected.new.TotalCost) {
		plan.drift = true
	}
	if nearlyEqual(selected.old.TotalCost, selected.new.TotalCost) {
		return plan, nil
	}

	plan.statsBranch, plan.statsCost, plan.statsDrift, err = r.resolveStatsCost(ctx, row, selected.new.TotalCost)
	if err != nil {
		return updatePlan{}, err
	}
	return plan, nil
}

func (r *repairer) calculateCandidate(ctx context.Context, row *logRow, group *service.Group, model string) candidateCost {
	resolved := r.resolver.Resolve(ctx, service.PricingInput{Model: model, GroupID: row.groupID, Group: group})
	candidate := candidateCost{model: model, source: resolved.Source}
	calculate := func(profile service.OpenAIBillingProfile) (*service.CostBreakdown, error) {
		cost, err := r.billingService.CalculateCostUnified(service.CostInput{
			Ctx:                  ctx,
			Model:                model,
			GroupID:              row.groupID,
			Group:                group,
			Tokens:               row.tokens,
			RequestCount:         1,
			RateMultiplier:       row.rateMultiplier,
			PricingAt:            row.createdAt,
			ServiceTier:          row.normalizedServiceTier(),
			Resolver:             r.resolver,
			Resolved:             resolved,
			OpenAIBillingProfile: profile,
		})
		if err == nil && cost == nil {
			return nil, fmt.Errorf("billing returned nil cost")
		}
		if err == nil && cost != nil && cost.BillingMode != "" && cost.BillingMode != string(service.BillingModeToken) {
			return nil, fmt.Errorf("resolved billing_mode=%q", cost.BillingMode)
		}
		return cost, err
	}

	var err error
	candidate.old, err = calculate(service.OpenAIBillingProfileUnknown)
	if err != nil {
		candidate.err = fmt.Errorf("old profile: %w", err)
		return candidate
	}
	candidate.new, err = calculate(service.OpenAIBillingProfileChatGPTSubscription)
	if err != nil {
		candidate.err = fmt.Errorf("subscription profile: %w", err)
	}
	return candidate
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
		plan := &plans[i]
		if !plan.needsChange || plan.newCost == nil {
			continue
		}
		var statsCost any
		if plan.statsCost != nil {
			statsCost = *plan.statsCost
		}
		result, err := stmt.ExecContext(ctx,
			plan.id,
			plan.newCost.InputCost,
			plan.newCost.ImageInputCost,
			plan.newCost.OutputCost,
			plan.newCost.ImageOutputCost,
			plan.newCost.CacheCreationCost,
			plan.newCost.CacheReadCost,
			plan.newCost.TotalCost,
			plan.newCost.ActualCost,
			statsCost,
			plan.newCost.LongContextBillingApplied,
		)
		if err != nil {
			return 0, fmt.Errorf("update usage_log id=%d: %w", plan.id, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rows affected for usage_log id=%d: %w", plan.id, err)
		}
		updated += rows
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit batch: %w", err)
	}
	return updated, nil
}

func nearlyEqual(a, b float64) bool {
	diff := math.Abs(a - b)
	if diff <= 1e-9 {
		return true
	}
	return diff <= 1e-6*math.Max(math.Abs(a), math.Abs(b))
}
