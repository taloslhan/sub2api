package service

// CAPYBARA-PATCH: 账号主动开启 credits 时，Standard/Fast 使用独立统一价卡。
import (
	"github.com/Wei-Shaw/sub2api/internal/custom/openaiprice"
	"math"
)

// UnifiedOpenAIModel 返回价卡支持的规范名称；未知型号不猜测订阅价格。
func UnifiedOpenAIModel(model string) string {
	if isOpenAIDaybreakBlueModel(model) {
		return "gpt-daybreak-blue-latest"
	}
	if isOpenAIGPT6AstraModel(model) {
		return "gpt-6-astra"
	}
	if isOpenAIGPT56Model(model) {
		return normalizeKnownOpenAICodexModel(model)
	}
	return ""
}

func unifiedOpenAITier(tier string) bool {
	switch normalizeBillingServiceTier(tier) {
	case "", "auto", "default", "standard", "priority", "fast":
		return true
	default:
		return false
	}
}

func unifiedOpenAIModelPricing(model string, profile OpenAIBillingProfile) *ModelPricing {
	card, ok := openaiprice.Lookup(UnifiedOpenAIModel(model))
	if !ok {
		return nil
	}
	p := &ModelPricing{
		InputPricePerToken:          card.API.Input / 1e6,
		CacheReadPricePerToken:      card.API.CacheRead / 1e6,
		OutputPricePerToken:         card.API.Output / 1e6,
		CacheCreationPriceExplicit:  true,
		LongContextInputThreshold:   openaiprice.LongContextThreshold,
		LongContextInputMultiplier:  openaiprice.LongContextInputMultiplier,
		LongContextOutputMultiplier: openaiprice.LongContextOutputMultiplier,
	}
	p.CacheCreationPricePerToken = p.InputPricePerToken * openaiprice.CacheWriteMultiplier
	fast := openaiprice.APIFastMultiplier
	if profile == OpenAIBillingProfileChatGPTCredits {
		m := card.SubscriptionMultipliers()
		p.InputPricePerToken *= m.Input
		p.CacheReadPricePerToken *= m.CacheRead
		p.OutputPricePerToken *= m.Output
		p.CacheCreationPricePerToken = 0
		p.LongContextInputThreshold = 0
		p.LongContextInputMultiplier = 1
		p.LongContextOutputMultiplier = 1
		fast = openaiprice.SubscriptionFastMultiplier
	}
	enforceOpenAIFastPricingRatio(p, fast)
	return p
}

// calculateUnifiedOpenAICost 保持 total_cost 与分项为业务倍率前金额。
// 即使 Resolver 提供按次或自定义 token 卡，也只保留其分时业务倍率。
func (s *BillingService) calculateUnifiedOpenAICost(input CostInput) *CostBreakdown {
	if !unifiedOpenAITier(input.ServiceTier) || input.OpenAIBillingProfile != OpenAIBillingProfileChatGPTCredits {
		return nil
	}
	p := unifiedOpenAIModelPricing(input.Model, input.OpenAIBillingProfile)
	if p == nil {
		return nil
	}
	resolved := input.Resolved
	if resolved == nil && input.Resolver != nil {
		resolved = input.Resolver.Resolve(input.Ctx, PricingInput{Model: input.Model, GroupID: input.GroupID, Group: input.Group})
	}
	bd := s.computeTokenBreakdown(p, input.Tokens, input.RateMultiplier, input.ServiceTier, true)
	multiplier := math.Max(input.RateMultiplier, 0) * resolvedChannelTimeMultiplier(resolved, input.PricingAt)
	bd.ActualCost = bd.TotalCost * multiplier
	bd.BusinessRateMultiplier = &multiplier
	bd.BillingMode = string(BillingModeToken)
	// 优惠/零倍率不影响长上下文实际采用的价格档。
	bd.LongContextBillingApplied = s.shouldApplySessionLongContextPricing(input.Tokens, p)
	return bd
}
