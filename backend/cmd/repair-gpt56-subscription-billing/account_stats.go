package main

// CAPYBARA-PATCH: 非 NULL account_stats_cost 按既有四级来源链安全重算，不猜测来源。

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type statsBranch int

const (
	branchNull statsBranch = iota
	branchCustomRule
	branchApplyPricing
	branchModelFile
)

func (b statsBranch) String() string {
	switch b {
	case branchNull:
		return "null"
	case branchCustomRule:
		return "custom_rule"
	case branchApplyPricing:
		return "apply_pricing"
	case branchModelFile:
		return "model_file"
	default:
		return "unknown"
	}
}

// resolveStatsCost 复刻账号统计的四级来源链，并把 model-file 分支切到订阅 profile。
// 无法归类时中止，避免用 total_cost 猜测原始来源。
func (r *repairer) resolveStatsCost(
	ctx context.Context,
	row *logRow,
	newTotalCost float64,
) (statsBranch, *float64, bool, error) {
	if row.accountStatsCost == nil {
		return branchNull, nil, false, nil
	}
	if row.groupID == nil {
		return 0, nil, false, fmt.Errorf("usage_log id=%d: account_stats_cost is set but group_id is NULL", row.id)
	}

	groupID := *row.groupID
	channel, err := r.channelService.GetChannelForGroup(ctx, groupID)
	if err != nil {
		return 0, nil, false, fmt.Errorf("usage_log id=%d: load channel for group %d: %w", row.id, groupID, err)
	}
	if channel == nil {
		return 0, nil, false, fmt.Errorf("usage_log id=%d: account_stats_cost is set but group %d has no active channel", row.id, groupID)
	}
	model := row.statsModel()
	if model == "" {
		return 0, nil, false, fmt.Errorf("usage_log id=%d: account_stats_cost is set but stats model is empty", row.id)
	}

	platform := r.channelService.GetGroupPlatform(ctx, groupID)
	if cost := tryCustomRules(channel, row.accountID, groupID, platform, model, row.tokens, 1); cost != nil {
		return branchCustomRule, row.accountStatsCost, !nearlyEqual(*cost, *row.accountStatsCost), nil
	}
	if channel.ApplyPricingToAccountStats {
		drift := !nearlyEqual(row.totalCost, *row.accountStatsCost) && !nearlyEqual(newTotalCost, *row.accountStatsCost)
		return branchApplyPricing, &newTotalCost, drift, nil
	}

	oldCandidates := r.oldModelFileStatsCandidates(model, row.tokens, row.normalizedServiceTier())
	newCost := r.newModelFileStatsCost(model, row.tokens, row.normalizedServiceTier())
	if len(oldCandidates) == 0 || newCost == nil {
		return 0, nil, false, fmt.Errorf(
			"usage_log id=%d: account_stats_cost=%v cannot be reproduced by any current source", row.id, *row.accountStatsCost)
	}
	drift := !nearlyEqual(*newCost, *row.accountStatsCost)
	for _, candidate := range oldCandidates {
		if nearlyEqual(candidate, *row.accountStatsCost) {
			drift = false
			break
		}
	}
	return branchModelFile, newCost, drift, nil
}

func (r *repairer) oldModelFileStatsCandidates(model string, tokens service.UsageTokens, tier string) []float64 {
	pricing, err := r.billingService.GetModelPricing(model)
	if err != nil || pricing == nil {
		return nil
	}
	candidates := make([]float64, 0, 2)
	if cost, err := r.billingService.CalculateCostWithServiceTier(model, tokens, 1, tier); err == nil &&
		cost != nil && cost.TotalCost > 0 {
		candidates = append(candidates, cost.TotalCost)
	}
	if tier == "priority" || tier == "fast" || tier == "flex" {
		return candidates
	}
	flat := flatModelFileStatsCost(pricing, tokens)
	if flat > 0 {
		candidates = append(candidates, flat)
	}
	return candidates
}

func (r *repairer) newModelFileStatsCost(model string, tokens service.UsageTokens, tier string) *float64 {
	cost, err := r.billingService.CalculateCostUnified(service.CostInput{
		Model:                model,
		Tokens:               tokens,
		RateMultiplier:       1,
		ServiceTier:          tier,
		OpenAIBillingProfile: service.OpenAIBillingProfileChatGPTSubscription,
	})
	if err != nil || cost == nil || cost.TotalCost <= 0 {
		return nil
	}
	return &cost.TotalCost
}

func flatModelFileStatsCost(pricing *service.ModelPricing, tokens service.UsageTokens) float64 {
	return float64(tokens.InputTokens)*pricing.InputPricePerToken +
		float64(tokens.OutputTokens)*pricing.OutputPricePerToken +
		float64(tokens.CacheCreationTokens)*pricing.CacheCreationPricePerToken +
		float64(tokens.CacheReadTokens)*pricing.CacheReadPricePerToken +
		float64(tokens.ImageOutputTokens)*pricing.ImageOutputPricePerToken
}

// 以下逻辑与 internal/service/account_stats_pricing.go 的自定义规则分支保持一致。
func tryCustomRules(
	channel *service.Channel,
	accountID, groupID int64,
	platform, model string,
	tokens service.UsageTokens,
	requestCount int,
) *float64 {
	modelLower := strings.ToLower(model)
	for i := range channel.AccountStatsPricingRules {
		rule := &channel.AccountStatsPricingRules[i]
		if !matchAccountStatsRule(rule, accountID, groupID) {
			continue
		}
		pricing := findPricingForModel(rule.Pricing, platform, modelLower)
		if pricing != nil {
			return calculateStatsCost(pricing, tokens, requestCount)
		}
	}
	return nil
}

func matchAccountStatsRule(rule *service.AccountStatsPricingRule, accountID, groupID int64) bool {
	if len(rule.AccountIDs) == 0 && len(rule.GroupIDs) == 0 {
		return false
	}
	for _, id := range rule.AccountIDs {
		if id == accountID {
			return true
		}
	}
	for _, id := range rule.GroupIDs {
		if id == groupID {
			return true
		}
	}
	return false
}

func findPricingForModel(
	pricingList []service.ChannelModelPricing,
	platform, modelLower string,
) *service.ChannelModelPricing {
	for i := range pricingList {
		pricing := &pricingList[i]
		if !isPlatformMatch(platform, pricing.Platform) {
			continue
		}
		for _, model := range pricing.Models {
			if strings.ToLower(model) == modelLower {
				return pricing
			}
		}
	}
	for i := range pricingList {
		pricing := &pricingList[i]
		if !isPlatformMatch(platform, pricing.Platform) {
			continue
		}
		for _, model := range pricing.Models {
			model = strings.ToLower(model)
			if strings.HasSuffix(model, "*") && strings.HasPrefix(modelLower, strings.TrimSuffix(model, "*")) {
				return pricing
			}
		}
	}
	return nil
}

func isPlatformMatch(queryPlatform, pricingPlatform string) bool {
	return queryPlatform == "" || pricingPlatform == "" || queryPlatform == pricingPlatform
}

func calculateStatsCost(
	pricing *service.ChannelModelPricing,
	tokens service.UsageTokens,
	requestCount int,
) *float64 {
	if pricing == nil {
		return nil
	}
	if pricing.BillingMode == service.BillingModePerRequest || pricing.BillingMode == service.BillingModeImage {
		if pricing.PerRequestPrice == nil || *pricing.PerRequestPrice <= 0 {
			return nil
		}
		cost := *pricing.PerRequestPrice * float64(requestCount)
		return &cost
	}
	return calculateTokenStatsCost(pricing, tokens)
}

func calculateTokenStatsCost(pricing *service.ChannelModelPricing, tokens service.UsageTokens) *float64 {
	selected := pricing
	if len(pricing.Intervals) > 0 {
		totalTokens := tokens.InputTokens + tokens.OutputTokens + tokens.CacheCreationTokens + tokens.CacheReadTokens
		if interval := service.FindMatchingInterval(pricing.Intervals, totalTokens); interval != nil {
			selected = &service.ChannelModelPricing{
				InputPrice:      interval.InputPrice,
				OutputPrice:     interval.OutputPrice,
				CacheWritePrice: interval.CacheWritePrice,
				CacheReadPrice:  interval.CacheReadPrice,
				PerRequestPrice: interval.PerRequestPrice,
			}
		}
	}
	deref := func(value *float64) float64 {
		if value == nil {
			return 0
		}
		return *value
	}
	cost := float64(tokens.InputTokens)*deref(selected.InputPrice) +
		float64(tokens.OutputTokens)*deref(selected.OutputPrice) +
		float64(tokens.CacheCreationTokens)*deref(selected.CacheWritePrice) +
		float64(tokens.CacheReadTokens)*deref(selected.CacheReadPrice) +
		float64(tokens.ImageOutputTokens)*deref(selected.ImageOutputPrice)
	if cost <= 0 {
		return nil
	}
	return &cost
}
