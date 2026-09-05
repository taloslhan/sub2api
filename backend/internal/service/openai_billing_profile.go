package service

// CAPYBARA-PATCH: GPT-5.6 与 Astra 按上游账号模式区分订阅 credits 与 API token pricing。

// OpenAIBillingProfile 标识上游账号的计费口径：ChatGPT/Codex 订阅按 credits，
// API Key 按 API token pricing。零值表示非 OpenAI 或无法判定，行为与改动前一致。
type OpenAIBillingProfile string

const (
	OpenAIBillingProfileUnknown             OpenAIBillingProfile = ""
	OpenAIBillingProfileChatGPTSubscription OpenAIBillingProfile = "chatgpt_subscription"
	OpenAIBillingProfileAPI                 OpenAIBillingProfile = "api"
)

// GPT-5.6 ChatGPT/Codex 订阅的 Fast 按 Standard credits 的 2.5 倍消耗。
const openAIChatGPTFastCreditRatio = 2.5

func openAIBillingProfileForAccount(account *Account) OpenAIBillingProfile {
	if account == nil {
		return OpenAIBillingProfileUnknown
	}
	if account.IsOpenAIOAuthLike() {
		return OpenAIBillingProfileChatGPTSubscription
	}
	if account.IsOpenAIApiKey() {
		return OpenAIBillingProfileAPI
	}
	return OpenAIBillingProfileUnknown
}

func applyOpenAIBillingProfilePolicy(profile OpenAIBillingProfile, model string, pricing *ModelPricing) *ModelPricing {
	if profile != OpenAIBillingProfileChatGPTSubscription || pricing == nil {
		return pricing
	}
	isAstra := isOpenAIGPT6AstraModel(model)
	if !isAstra && !isOpenAIGPT56Model(model) {
		return pricing
	}

	cloned := *pricing
	cloned.LongContextInputThreshold = 0
	cloned.LongContextInputMultiplier = 1
	cloned.LongContextOutputMultiplier = 1
	if isAstra {
		// CAPYBARA-PATCH: Astra 订阅不收缓存创建费及长上下文附加费；API 与其他模型保持原口径。
		cloned.CacheCreationPricePerToken = 0
		cloned.CacheCreationPricePerTokenPriority = 0
		cloned.CacheCreation5mPrice = 0
		cloned.CacheCreation1hPrice = 0
		cloned.CacheCreationPriceExplicit = true
		return &cloned
	}
	enforceOpenAIFastPricingRatio(&cloned, openAIChatGPTFastCreditRatio)
	return &cloned
}
