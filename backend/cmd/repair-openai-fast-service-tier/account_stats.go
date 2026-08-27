package main

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// statsBranch 是 account_stats_cost 的来源分支。
//
// usage_logs 上没有来源标记列，渠道配置也没有历史版本化，所以只能用「当前渠道配置」
// 反推 service.resolveAccountStatsCost 当初走的是哪条分支。这条推断的前提是
// 「该行落库之后渠道配置没有变更」——statsValueDrift 就是用来量化这个前提有多可信的。
type statsBranch int

const (
	// branchNull 原值为 NULL：保持 NULL。
	// 下游的 COALESCE(account_stats_cost, total_cost) 会自动联动到新的 total_cost。
	branchNull statsBranch = iota
	// branchCustomRule 自定义规则来源：规则定价与 service_tier 无关，保持不变。
	branchCustomRule
	// branchApplyPricing 渠道开启了 ApplyPricingToAccountStats：直接跟随新的 total_cost。
	branchApplyPricing
	// branchModelFile 模型定价文件来源：按 priority 重新计算。
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

// resolveStatsCost 推断 account_stats_cost 的来源分支并给出写回值。
// 无法归类时返回 error，由调用方中止整个流程——不要猜测。
func (r *repairer) resolveStatsCost(ctx context.Context, row *logRow, newTotalCost float64) (statsBranch, *float64, bool, error) {
	if row.accountStatsCost == nil {
		return branchNull, nil, false, nil
	}
	if row.groupID == nil {
		return 0, nil, false, fmt.Errorf(
			"usage_log id=%d: account_stats_cost is set but group_id is NULL; cannot infer its pricing source", row.id)
	}

	groupID := *row.groupID
	channel, err := r.channelService.GetChannelForGroup(ctx, groupID)
	if err != nil {
		return 0, nil, false, fmt.Errorf("usage_log id=%d: load channel for group %d: %w", row.id, groupID, err)
	}
	if channel == nil {
		return 0, nil, false, fmt.Errorf(
			"usage_log id=%d: account_stats_cost is set but group %d has no active channel now; cannot infer its pricing source",
			row.id, groupID)
	}

	platform := r.channelService.GetGroupPlatform(ctx, groupID)
	// 复刻 resolveAccountStatsCost 的入口守卫：upstreamModel 为空时上游直接返回 nil。
	// 此处 account_stats_cost 非 NULL 却拿不到模型名，属于自相矛盾的数据状态，中止。
	model := row.statsModel()
	if model == "" {
		return 0, nil, false, fmt.Errorf(
			"usage_log id=%d: account_stats_cost is set but the row carries no model name; cannot infer its pricing source", row.id)
	}
	requestCount := 1
	if row.imageCount > 0 {
		requestCount = row.imageCount
	}

	// 优先级 1：自定义规则（始终尝试，不依赖 ApplyPricingToAccountStats 开关）。
	// 规则定价按 token/次单价直接算，与 service_tier 无关，所以保持原值不变。
	if cost := tryCustomRules(channel, row.accountID, groupID, platform, model, row.tokens, requestCount); cost != nil {
		return branchCustomRule, row.accountStatsCost, !nearlyEqual(*cost, *row.accountStatsCost), nil
	}

	// 优先级 2：渠道开启「应用模型定价到账号统计」→ 直接等于本次请求的客户计费（倍率前）。
	// 客户计费本次被改了，账号统计必须同步跟随。
	if channel.ApplyPricingToAccountStats {
		drift := !nearlyEqual(row.totalCost, *row.accountStatsCost)
		newCost := newTotalCost
		return branchApplyPricing, &newCost, drift, nil
	}

	// 优先级 3：模型定价文件（LiteLLM/fallback）默认价格。
	oldCandidates := r.modelFileStatsCandidates(model, row.tokens, row.normalizedOldTier())
	if len(oldCandidates) == 0 {
		return 0, nil, false, fmt.Errorf(
			"usage_log id=%d: account_stats_cost=%v is set but none of the custom-rule / apply-pricing / model-file sources can produce a value under the current channel config; refusing to guess",
			row.id, *row.accountStatsCost)
	}
	newCandidates := r.modelFileStatsCandidates(model, row.tokens, targetServiceTier)
	if len(newCandidates) == 0 {
		return 0, nil, false, fmt.Errorf(
			"usage_log id=%d: model-file account stats pricing for model %q cannot be recomputed at service_tier=%s",
			row.id, model, targetServiceTier)
	}
	// 目标档位 priority 必定走「带档位重算」这一条明确分支，候选唯一，写回值是精确的。
	newCost := newCandidates[0]

	// 旧档位下上游走哪条分支无法在此还原，所以落库值命中任一候选就认为
	// 「渠道配置未变更」的前提仍成立，避免把长上下文行误报成 drift。
	drift := true
	for _, candidate := range oldCandidates {
		if nearlyEqual(candidate, *row.accountStatsCost) {
			drift = false
			break
		}
	}
	return branchModelFile, &newCost, drift, nil
}

// modelFileStatsCandidates 复刻 service.tryModelFilePricing 在给定档位下可能产出的值。
//
// 上游实现的分支条件是
// `tier ∈ {priority, fast, flex} || shouldApplySessionLongContextPricing(tokens, pricing)`，
// 而 shouldApplySessionLongContextPricing 未导出，main 包里拿不到。因此：
//   - priority/fast/flex：必定走「带档位重算」，候选唯一，结果精确；
//   - 其余档位：两条分支都可能，把「带档位重算」与「平铺单价」都作为候选返回，
//     由调用方判断落库值是否命中其一。
func (r *repairer) modelFileStatsCandidates(model string, tokens service.UsageTokens, tier string) []float64 {
	pricing, err := r.billingService.GetModelPricing(model)
	if err != nil || pricing == nil {
		return nil
	}

	candidates := make([]float64, 0, 2)
	if breakdown, err := r.billingService.CalculateCostWithServiceTier(model, tokens, 1, tier); err == nil &&
		breakdown != nil && breakdown.TotalCost > 0 {
		candidates = append(candidates, breakdown.TotalCost)
	}
	if tier == "priority" || tier == "fast" || tier == "flex" {
		return candidates
	}

	flat := float64(tokens.InputTokens)*pricing.InputPricePerToken +
		float64(tokens.OutputTokens)*pricing.OutputPricePerToken +
		float64(tokens.CacheCreationTokens)*pricing.CacheCreationPricePerToken +
		float64(tokens.CacheReadTokens)*pricing.CacheReadPricePerToken +
		float64(tokens.ImageOutputTokens)*pricing.ImageOutputPricePerToken
	if flat > 0 {
		candidates = append(candidates, flat)
	}
	return candidates
}

// 以下是 service 包内未导出的账号统计定价匹配逻辑的等价实现，
// 逐行对照 internal/service/account_stats_pricing.go，改动那边时这里要同步。

// tryCustomRules 遍历自定义规则，按数组顺序先命中为准。
func tryCustomRules(
	channel *service.Channel, accountID, groupID int64,
	platform, model string, tokens service.UsageTokens, requestCount int,
) *float64 {
	modelLower := strings.ToLower(model)
	for _, rule := range channel.AccountStatsPricingRules {
		if !matchAccountStatsRule(&rule, accountID, groupID) {
			continue
		}
		pricing := findPricingForModel(rule.Pricing, platform, modelLower)
		if pricing == nil {
			continue // 规则匹配但模型不在规则定价中，继续下一条
		}
		return calculateStatsCost(pricing, tokens, requestCount)
	}
	return nil
}

// matchAccountStatsRule 检查规则是否匹配指定的 accountID 和 groupID。
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

// findPricingForModel 先精确匹配，再通配符匹配（按配置顺序，先匹配先使用）。
func findPricingForModel(pricingList []service.ChannelModelPricing, platform, modelLower string) *service.ChannelModelPricing {
	for i := range pricingList {
		p := &pricingList[i]
		if !isPlatformMatch(platform, p.Platform) {
			continue
		}
		for _, m := range p.Models {
			if strings.ToLower(m) == modelLower {
				return p
			}
		}
	}
	for i := range pricingList {
		p := &pricingList[i]
		if !isPlatformMatch(platform, p.Platform) {
			continue
		}
		for _, m := range p.Models {
			ml := strings.ToLower(m)
			if !strings.HasSuffix(ml, "*") {
				continue
			}
			if strings.HasPrefix(modelLower, strings.TrimSuffix(ml, "*")) {
				return p
			}
		}
	}
	return nil
}

// isPlatformMatch 判断平台是否匹配（空平台视为不限平台）。
func isPlatformMatch(queryPlatform, pricingPlatform string) bool {
	if queryPlatform == "" || pricingPlatform == "" {
		return true
	}
	return queryPlatform == pricingPlatform
}

// calculateStatsCost 使用给定的定价计算费用（不含任何倍率，原始费用）。
func calculateStatsCost(pricing *service.ChannelModelPricing, tokens service.UsageTokens, requestCount int) *float64 {
	if pricing == nil {
		return nil
	}
	switch pricing.BillingMode {
	case service.BillingModePerRequest, service.BillingModeImage:
		if pricing.PerRequestPrice == nil || *pricing.PerRequestPrice <= 0 {
			return nil
		}
		cost := *pricing.PerRequestPrice * float64(requestCount)
		return &cost
	default:
		return calculateTokenStatsCost(pricing, tokens)
	}
}

// calculateTokenStatsCost Token 计费；有区间定价时按总 token 数命中区间。
func calculateTokenStatsCost(pricing *service.ChannelModelPricing, tokens service.UsageTokens) *float64 {
	p := pricing
	if len(pricing.Intervals) > 0 {
		totalTokens := tokens.InputTokens + tokens.OutputTokens + tokens.CacheCreationTokens + tokens.CacheReadTokens
		if iv := service.FindMatchingInterval(pricing.Intervals, totalTokens); iv != nil {
			p = &service.ChannelModelPricing{
				InputPrice:      iv.InputPrice,
				OutputPrice:     iv.OutputPrice,
				CacheWritePrice: iv.CacheWritePrice,
				CacheReadPrice:  iv.CacheReadPrice,
				PerRequestPrice: iv.PerRequestPrice,
			}
		}
	}
	deref := func(ptr *float64) float64 {
		if ptr == nil {
			return 0
		}
		return *ptr
	}
	cost := float64(tokens.InputTokens)*deref(p.InputPrice) +
		float64(tokens.OutputTokens)*deref(p.OutputPrice) +
		float64(tokens.CacheCreationTokens)*deref(p.CacheWritePrice) +
		float64(tokens.CacheReadTokens)*deref(p.CacheReadPrice) +
		float64(tokens.ImageOutputTokens)*deref(p.ImageOutputPrice)
	if cost <= 0 {
		return nil
	}
	return &cost
}

// nearlyEqual 比较两个金额。account_stats_cost 是 NUMERIC(20,10)，落库时已经四舍五入，
// 所以绝对容差取 1e-9（远大于 5e-11 的量化误差），同时给大额留 1e-6 的相对容差。
func nearlyEqual(a, b float64) bool {
	diff := math.Abs(a - b)
	if diff <= 1e-9 {
		return true
	}
	return diff <= 1e-6*math.Max(math.Abs(a), math.Abs(b))
}
