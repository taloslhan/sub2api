package service

// CAPYBARA-PATCH: credits 计费按账号显式启用，缺省关闭，shadow 由调用方解析父账号。
const OpenAICreditsBillingEnabledKey = "openai_credits_billing_enabled"

func (a *Account) IsOpenAICreditsBillingEnabled() bool {
	if !a.IsOpenAIOAuthLike() || a.IsShadow() {
		return false
	}
	enabled, _ := a.Extra[OpenAICreditsBillingEnabledKey].(bool)
	return enabled
}
