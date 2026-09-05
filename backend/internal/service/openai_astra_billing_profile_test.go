//go:build unit

package service

// CAPYBARA-PATCH: Astra 订阅免缓存创建费和长上下文附加费，API 保持独立价卡。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAstraSubscriptionPricingPolicyPreservesSharedAndAPIPrices(t *testing.T) {
	original := &ModelPricing{
		InputPricePerToken: 10, OutputPricePerToken: 50, CacheReadPricePerToken: 1,
		InputPricePerTokenPriority: 20, OutputPricePerTokenPriority: 100,
		CacheCreationPricePerToken: 12.5, CacheCreationPricePerTokenPriority: 25,
		CacheCreation5mPrice: 12.5, CacheCreation1hPrice: 20, SupportsCacheBreakdown: true,
		LongContextInputThreshold: 272_000, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5,
	}
	before := *original
	for _, model := range []string{"gpt-6", "gpt-6-astra", "openai/GPT_6_ASTRA", "gpt-6-astra-2026-09-04"} {
		got := applyOpenAIBillingProfilePolicy(OpenAIBillingProfileChatGPTSubscription, model, original)
		require.NotSame(t, original, got)
		require.Equal(t, before, *original)
		require.Zero(t, got.LongContextInputThreshold)
		require.Equal(t, 1.0, got.LongContextInputMultiplier)
		require.Equal(t, 1.0, got.LongContextOutputMultiplier)
		require.True(t, got.CacheCreationPriceExplicit)
		cost := newTestBillingService().computeTokenBreakdown(got, UsageTokens{
			InputTokens: 300_000, OutputTokens: 1, CacheReadTokens: 1,
			CacheCreationTokens: 20, CacheCreation5mTokens: 10, CacheCreation1hTokens: 10,
		}, 1, "priority", true)
		require.Zero(t, cost.CacheCreationCost)
		require.False(t, cost.LongContextBillingApplied)
		require.Equal(t, 300_000*20.0, cost.InputCost, "不得顺带更改 Astra 的 Fast 倍率")
		for _, profile := range []OpenAIBillingProfile{OpenAIBillingProfileAPI, OpenAIBillingProfileUnknown} {
			require.Same(t, original, applyOpenAIBillingProfilePolicy(profile, model, original))
		}
	}
	require.Same(t, original, applyOpenAIBillingProfilePolicy(OpenAIBillingProfileChatGPTSubscription, "gpt-6-other", original))
}

func TestAstraSubscriptionBillingAcrossBoundaryAndAccountStats(t *testing.T) {
	billing := newTestBillingService()
	for _, profile := range []OpenAIBillingProfile{OpenAIBillingProfileChatGPTSubscription, OpenAIBillingProfileAPI} {
		for _, totalInput := range []int{272_000, 272_001, 800_000} {
			for _, tier := range []string{"", "priority"} {
				for _, withResolver := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%d/%s/resolver_%t", profile, totalInput, tier, withResolver), func(t *testing.T) {
						tokens := gpt56BoundaryTokens(totalInput)
						input := CostInput{
							Ctx: context.Background(), Model: "gpt-6-astra", Tokens: tokens,
							Group: &Group{ID: 6, LongContextPricingEnabled: true}, RateMultiplier: 1,
							ServiceTier: tier, LongContextBillingEnabled: boolPtr(true), OpenAIBillingProfile: profile,
						}
						if withResolver {
							input.Resolver = NewModelPricingResolver(nil, billing)
						}
						cost, err := billing.CalculateCostUnified(input)
						require.NoError(t, err)
						inputMultiplier, outputMultiplier := 1.0, 1.0
						if tier == "priority" {
							inputMultiplier, outputMultiplier = 2, 2
						}
						longContext := profile == OpenAIBillingProfileAPI && totalInput > 272_000
						if longContext {
							inputMultiplier *= 2
							outputMultiplier *= 1.5
						}
						require.Equal(t, longContext, cost.LongContextBillingApplied)
						require.InDelta(t, float64(tokens.InputTokens)*10e-6*inputMultiplier, cost.InputCost, 1e-10)
						require.InDelta(t, float64(tokens.OutputTokens)*50e-6*outputMultiplier, cost.OutputCost, 1e-10)
						require.InDelta(t, float64(tokens.CacheReadTokens)*1e-6*inputMultiplier, cost.CacheReadCost, 1e-10)
						if profile == OpenAIBillingProfileChatGPTSubscription {
							require.Zero(t, cost.CacheCreationCost)
						} else {
							require.InDelta(t, float64(tokens.CacheCreationTokens)*12.5e-6*inputMultiplier, cost.CacheCreationCost, 1e-10)
						}
						stats := tryModelFilePricing(billing, input.Model, tokens, tier, profile, true)
						require.NotNil(t, stats)
						require.InDelta(t, cost.TotalCost, *stats, 1e-10)
					})
				}
			}
		}
	}
}

func TestAstraSubscriptionRecordUsageAcrossHTTPAndWebSocket(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		for _, wsMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ws_%t", accountType, wsMode), func(t *testing.T) {
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				svc.resolver = NewModelPricingResolver(nil, svc.billingService)
				groupID := int64(6)
				svc.channelService = newTestChannelServiceForStats(t, &Channel{ID: 1, Status: StatusActive}, groupID, PlatformOpenAI)
				tier := "priority"
				err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: &OpenAIForwardResult{
						RequestID: fmt.Sprintf("astra_%s_%t", accountType, wsMode), Model: "gpt-6-astra",
						ServiceTier: &tier, OpenAIWSMode: wsMode, Duration: time.Second,
						Usage: OpenAIUsage{InputTokens: 300_000, OutputTokens: 1_000, CacheCreationInputTokens: 50_000, CacheReadInputTokens: 50_000},
					},
					APIKey: &APIKey{ID: 1, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1, LongContextPricingEnabled: true}},
					User:   &User{ID: 2}, Account: &Account{ID: 3, Platform: PlatformOpenAI, Type: accountType}, InboundEndpoint: "/v1/responses",
				})
				require.NoError(t, err)
				log := usageRepo.lastLog
				require.NotNil(t, log)
				require.Equal(t, 50_000, log.CacheCreationTokens, "保留上游用量事实，只免除对应费用")
				require.Zero(t, log.CacheCreationCost)
				require.False(t, log.LongContextBillingApplied)
				require.InDelta(t, 4.0, log.InputCost, 1e-10)
				require.InDelta(t, 0.1, log.OutputCost, 1e-10)
				require.InDelta(t, 0.1, log.CacheReadCost, 1e-10)
				require.InDelta(t, 4.2, log.TotalCost, 1e-10)
				require.NotNil(t, log.AccountStatsCost)
				require.InDelta(t, log.TotalCost, *log.AccountStatsCost, 1e-10)
			})
		}
	}
}
