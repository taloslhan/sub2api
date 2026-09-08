// Package openaiprice 保存业务采用的非促销 API 基准与订阅 credits 价卡。
package openaiprice

const (
	Version                     = "2026-09-08.v1"
	SourceDate                  = "2026-09-07"
	CreditsPerDollar            = 25.0 // 内部金额归一化，不代表账号实际采购成本。
	APIFastMultiplier           = 2.0
	SubscriptionFastMultiplier  = 2.5
	LongContextThreshold        = 272000
	LongContextInputMultiplier  = 2.0
	LongContextOutputMultiplier = 1.5
	CacheWriteMultiplier        = 1.25
	APISource                   = "https://developers.openai.com/api/docs/pricing"
	CreditsSource               = "https://help.openai.com/en/articles/11481834-chatgpt-rate-card-business-enterpriseedu-credit-based-pricing#chatgpt-work-and-codex"
	FastSource                  = "https://learn.chatgpt.com/docs/agent-configuration/speed"
)

// Components 的单位为每百万 tokens；分项倍率不提前舍入。
type Components struct {
	Input     float64 `json:"input"`
	CacheRead float64 `json:"cache_read"`
	Output    float64 `json:"output"`
}

type Card struct {
	API     Components `json:"api"`
	Credits Components `json:"credits"`
}

// Lookup 按值返回，调用方无法污染共享价卡。Sol 的 API 基准刻意不跟随促销。
func Lookup(model string) (Card, bool) {
	switch model {
	case "gpt-6-astra":
		// CAPYBARA-PATCH: Astra 缓存读取恢复原 credits 价，撤销额外 2 倍业务倍率。
		return Card{Components{10, 1, 50}, Components{250, 25, 1250}}, true
	case "gpt-5.6-sol", "gpt-daybreak-blue-latest":
		return Card{Components{5, .5, 30}, Components{100, 10, 500}}, true
	case "gpt-5.6-terra":
		return Card{Components{2, .2, 12}, Components{50, 5, 300}}, true
	case "gpt-5.6-luna":
		return Card{Components{.2, .02, 1.2}, Components{5, .5, 30}}, true
	default:
		return Card{}, false
	}
}

func (c Card) SubscriptionMultipliers() Components {
	return Components{c.Credits.Input / CreditsPerDollar / c.API.Input,
		c.Credits.CacheRead / CreditsPerDollar / c.API.CacheRead,
		c.Credits.Output / CreditsPerDollar / c.API.Output}
}
