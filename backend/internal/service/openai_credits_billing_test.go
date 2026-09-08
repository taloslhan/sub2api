//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCreditsBillingRequiresExplicitSubscriptionAccountOptIn(t *testing.T) {
	for _, typ := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		for _, value := range []any{nil, false, "true", true} {
			a := &Account{Platform: PlatformOpenAI, Type: typ, Extra: map[string]any{OpenAICreditsBillingEnabledKey: value}}
			want := value == true && typ != AccountTypeAPIKey
			require.Equal(t, want, a.IsOpenAICreditsBillingEnabled())
			require.Equal(t, want, openAIBillingProfileForAccount(a) == OpenAIBillingProfileChatGPTCredits)
		}
	}
	require.False(t, (*Account)(nil).IsOpenAICreditsBillingEnabled())
	require.Error(t, ValidateOpenAILongContextBillingExtra(PlatformOpenAI, map[string]any{OpenAICreditsBillingEnabledKey: "true"}))
}

func TestCreditsBillingAcceptanceAndIsolation(t *testing.T) {
	billing := newTestBillingService()
	for _, tt := range []struct {
		model    string
		standard float64
	}{
		{"gpt-5.6-sol", .62}, {"openai/GPT_5.6_SOL", .62}, {"gpt-5.6-max", .62},
		{"gpt-daybreak-blue-latest", .62}, {"gpt-6-astra", 1.55}, {"gpt-6", 1.55},
		{"gpt-6-astra-2026-09-01", 1.55}, {"gpt-5.6-terra", .33}, {"gpt-5.6-luna", .033},
	} {
		for _, tier := range []string{"", "priority", "fast"} {
			for _, withResolver := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/resolver_%t", tt.model, tier, withResolver), func(t *testing.T) {
					input := CostInput{Ctx: context.Background(), Model: tt.model, ServiceTier: tier,
						Tokens:         UsageTokens{InputTokens: 100_000, CacheReadTokens: 50_000, OutputTokens: 10_000, CacheCreationTokens: 30_000},
						RateMultiplier: .7, OpenAIBillingProfile: OpenAIBillingProfileChatGPTCredits}
					if withResolver {
						input.Resolver = NewModelPricingResolver(nil, billing)
					}
					cost, err := billing.CalculateCostUnified(input)
					require.NoError(t, err)
					want := tt.standard
					if tier != "" {
						want *= 2.5
					}
					require.InDelta(t, want, cost.TotalCost, 1e-12)
					require.InDelta(t, want*.7, cost.ActualCost, 1e-12)
					require.Zero(t, cost.CacheCreationCost)
					require.False(t, cost.LongContextBillingApplied)
				})
			}
		}
	}
	// 开关外保持原订阅价与 Fast 档；共享目录不会被 credits 调用改写。
	input := CostInput{Model: "gpt-6-astra", Tokens: UsageTokens{InputTokens: 100_000}, RateMultiplier: 1, ServiceTier: "priority"}
	for _, profile := range []OpenAIBillingProfile{OpenAIBillingProfileChatGPTSubscription, OpenAIBillingProfileAPI, OpenAIBillingProfileUnknown} {
		input.OpenAIBillingProfile = profile
		cost, err := billing.CalculateCostUnified(input)
		require.NoError(t, err)
		require.InDelta(t, 2, cost.TotalCost, 1e-12)
	}
}

func TestCreditsBillingIgnoresCatalogAndCustomPricesButKeepsTimeMultiplier(t *testing.T) {
	billing := NewBillingService(&config.Config{}, newStubPricingServiceFromJSON(t, `{"gpt-5.6-sol":{"input_cost_per_token":0.2,"output_cost_per_token":0.3}}`))
	for _, n := range []int{272_000, 272_001} {
		resolved := &ResolvedPricing{Mode: BillingModePerRequest, Source: PricingSourceChannel,
			BasePricing:    &ModelPricing{InputPricePerToken: 999, FastMultiplier: creditsFloatPtr(9)},
			channelPricing: &ChannelModelPricing{TimePricing: &ChannelTimePricing{Timezone: "UTC", Periods: []ChannelTimePricingPeriod{{StartTime: "00:00", EndTime: "23:59", Multiplier: 1.3}}}}}
		cost, err := billing.CalculateCostUnified(CostInput{Model: "gpt-5.6-sol", Tokens: UsageTokens{InputTokens: n},
			OpenAIBillingProfile: OpenAIBillingProfileChatGPTCredits, RateMultiplier: .8, ServiceTier: "fast", Resolved: resolved,
			PricingAt: time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)})
		require.NoError(t, err)
		require.InDelta(t, float64(n)*4e-6*2.5, cost.TotalCost, 1e-12)
		require.InDelta(t, cost.TotalCost*.8*1.3, cost.ActualCost, 1e-12)
		require.False(t, cost.LongContextBillingApplied)
	}
}

func creditsFloatPtr(value float64) *float64 { return &value }

func TestCreditsBillingRecordUsageAndParentOptIn(t *testing.T) {
	for _, ws := range []bool{false, true} {
		for _, shadow := range []bool{false, true} {
			for _, freeFast := range []bool{false, true} {
				t.Run(fmt.Sprintf("ws_%t/shadow_%t/free_%t", ws, shadow, freeFast), func(t *testing.T) {
					repo := &openAIRecordUsageLogRepoStub{inserted: true}
					svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
					parent := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{OpenAICreditsBillingEnabledKey: true}}
					account := parent
					if shadow {
						svc.accountRepo = &openAIRecordUsageAccountRepoStub{account: parent}
						account = &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parent.ID, QuotaDimension: QuotaDimensionSpark}
					}
					apiKey := openAIRecordUsageAPIKeyWithGroup(svc, 1, true)
					apiKey.Group.FreeOpenAIFast = freeFast
					apiKey.Group.Platform = PlatformOpenAI
					apiKey.Group.RateMultiplier = 1
					apiKey.GroupID = &apiKey.Group.ID
					tier := "priority"
					err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
						Result: &OpenAIForwardResult{RequestID: "credits", Model: "gpt-6-astra", ServiceTier: &tier, OpenAIWSMode: ws, Duration: time.Second,
							Usage: OpenAIUsage{InputTokens: 150_000, CacheReadInputTokens: 50_000, OutputTokens: 10_000}},
						APIKey: apiKey, User: &User{ID: 2}, Account: account,
					})
					require.NoError(t, err)
					require.InDelta(t, 3.875, repo.lastLog.TotalCost, 1e-12)
					want := 3.875
					if freeFast {
						want = 1.55
					}
					require.InDelta(t, want, repo.lastLog.ActualCost, 1e-12)
					require.NotNil(t, repo.lastLog.AccountStatsCost)
					require.InDelta(t, 3.875, *repo.lastLog.AccountStatsCost, 1e-12)
				})
			}
		}
	}
}
