//go:build unit

package service

// CAPYBARA-PATCH: 覆盖 GPT-5.6 订阅 credits 与 API token pricing 的目标计费矩阵。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func gpt56BoundaryTokens(totalInput int) UsageTokens {
	return UsageTokens{
		InputTokens:         totalInput - 100_000,
		OutputTokens:        1_000,
		CacheCreationTokens: 50_000,
		CacheReadTokens:     50_000,
	}
}

func calculateGPT56ProfileCost(
	t *testing.T,
	profile OpenAIBillingProfile,
	tokens UsageTokens,
	serviceTier string,
	groupEnabled bool,
	accountGate *bool,
) *CostBreakdown {
	t.Helper()
	// Long-context ladders are catalog-driven; the fallback price card contains
	// only the base tier.
	billing := NewBillingService(&config.Config{}, newStubPricingServiceFromJSON(t, gpt56LadderCatalogJSON))
	group := &Group{ID: 1, LongContextPricingEnabled: groupEnabled}
	cost, err := billing.CalculateCostUnified(CostInput{
		Ctx:                       context.Background(),
		Model:                     "gpt-5.6-sol",
		Group:                     group,
		Tokens:                    tokens,
		RateMultiplier:            1,
		ServiceTier:               serviceTier,
		Resolver:                  NewModelPricingResolver(nil, billing),
		LongContextBillingEnabled: accountGate,
		OpenAIBillingProfile:      profile,
	})
	require.NoError(t, err)
	require.NotNil(t, cost)
	return cost
}

func requireGPT56CostMultipliers(
	t *testing.T,
	cost *CostBreakdown,
	tokens UsageTokens,
	inputMultiplier float64,
	outputMultiplier float64,
) {
	t.Helper()
	require.InDelta(t, float64(tokens.InputTokens)*5e-6*inputMultiplier, cost.InputCost, 1e-10)
	require.InDelta(t, float64(tokens.OutputTokens)*30e-6*outputMultiplier, cost.OutputCost, 1e-10)
	require.InDelta(t, float64(tokens.CacheCreationTokens)*6.25e-6*inputMultiplier, cost.CacheCreationCost, 1e-10)
	require.InDelta(t, float64(tokens.CacheReadTokens)*0.5e-6*inputMultiplier, cost.CacheReadCost, 1e-10)
	require.InDelta(t, cost.InputCost+cost.OutputCost+cost.CacheCreationCost+cost.CacheReadCost, cost.TotalCost, 1e-10)
	require.InDelta(t, cost.TotalCost, cost.ActualCost, 1e-10)
}

func TestOpenAIBillingProfileForAccount(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    OpenAIBillingProfile
	}{
		{name: "nil", account: nil, want: OpenAIBillingProfileUnknown},
		{name: "oauth", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, want: OpenAIBillingProfileChatGPTSubscription},
		{name: "setup token", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken}, want: OpenAIBillingProfileChatGPTSubscription},
		{name: "api key", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, want: OpenAIBillingProfileAPI},
		{name: "openai upstream", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeUpstream}, want: OpenAIBillingProfileUnknown},
		{name: "non openai oauth", account: &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, want: OpenAIBillingProfileUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, openAIBillingProfileForAccount(tt.account))
		})
	}
}

func TestApplyOpenAIBillingProfilePolicyClonesGPT56Pricing(t *testing.T) {
	original := &ModelPricing{
		InputPricePerToken:                 2,
		OutputPricePerToken:                3,
		CacheCreationPricePerToken:         4,
		CacheReadPricePerToken:             5,
		LongContextInputThreshold:          272_000,
		LongContextInputMultiplier:         2,
		LongContextOutputMultiplier:        1.5,
		InputPricePerTokenPriority:         4,
		OutputPricePerTokenPriority:        6,
		CacheCreationPricePerTokenPriority: 8,
		CacheReadPricePerTokenPriority:     10,
	}

	got := applyOpenAIBillingProfilePolicy(OpenAIBillingProfileChatGPTSubscription, "GPT_5.6_SOL", original)
	require.NotSame(t, original, got)
	require.Equal(t, 272_000, original.LongContextInputThreshold, "不得污染共享定价指针")
	require.Zero(t, got.LongContextInputThreshold)
	require.Equal(t, 1.0, got.LongContextInputMultiplier)
	require.Equal(t, 1.0, got.LongContextOutputMultiplier)
	require.Equal(t, 5.0, got.InputPricePerTokenPriority)
	require.Equal(t, 7.5, got.OutputPricePerTokenPriority)
	require.Equal(t, 10.0, got.CacheCreationPricePerTokenPriority)
	require.Equal(t, 12.5, got.CacheReadPricePerTokenPriority)
	require.Same(t, original, applyOpenAIBillingProfilePolicy(OpenAIBillingProfileAPI, "gpt-5.6-sol", original))
	require.Same(t, original, applyOpenAIBillingProfilePolicy(OpenAIBillingProfileChatGPTSubscription, "gpt-5.5", original))
}

func TestGPT56SubscriptionProfileUsesLinearCreditsAcrossBoundaryAndTier(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		profile := openAIBillingProfileForAccount(&Account{Platform: PlatformOpenAI, Type: accountType})
		for _, totalInput := range []int{272_000, 272_001} {
			for _, tier := range []struct {
				name       string
				value      string
				multiplier float64
			}{
				{name: "standard", value: "", multiplier: 1},
				{name: "fast", value: "priority", multiplier: 2.5},
			} {
				name := fmt.Sprintf("%s/%s/%d", accountType, tier.name, totalInput)
				t.Run(name, func(t *testing.T) {
					tokens := gpt56BoundaryTokens(totalInput)
					cost := calculateGPT56ProfileCost(t, profile, tokens, tier.value, true, boolPtr(true))
					requireGPT56CostMultipliers(t, cost, tokens, tier.multiplier, tier.multiplier)
					require.False(t, cost.LongContextBillingApplied)
				})
			}
		}
	}
}

func TestGPT56SubscriptionProfileIgnoresLongContextGates(t *testing.T) {
	for _, groupEnabled := range []bool{false, true} {
		for _, accountEnabled := range []bool{false, true} {
			for _, tier := range []struct {
				value      string
				multiplier float64
			}{{value: "", multiplier: 1}, {value: "priority", multiplier: 2.5}} {
				name := fmt.Sprintf("group_%t/account_%t/tier_%s", groupEnabled, accountEnabled, tier.value)
				t.Run(name, func(t *testing.T) {
					tokens := gpt56BoundaryTokens(272_001)
					cost := calculateGPT56ProfileCost(
						t, OpenAIBillingProfileChatGPTSubscription, tokens, tier.value, groupEnabled, boolPtr(accountEnabled),
					)
					requireGPT56CostMultipliers(t, cost, tokens, tier.multiplier, tier.multiplier)
					require.False(t, cost.LongContextBillingApplied)
				})
			}
		}
	}
}

func TestGPT56SubscriptionProfileAppliesToDaybreakSolTerraAndLuna(t *testing.T) {
	for _, model := range []string{openai.DaybreakBlueModelID, "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		t.Run(model, func(t *testing.T) {
			billing := newTestBillingService()
			resolver := NewModelPricingResolver(nil, billing)
			calculate := func(tier string) *CostBreakdown {
				cost, err := billing.CalculateCostUnified(CostInput{
					Ctx:                  context.Background(),
					Model:                model,
					Tokens:               gpt56BoundaryTokens(272_001),
					RateMultiplier:       1,
					ServiceTier:          tier,
					Resolver:             resolver,
					OpenAIBillingProfile: OpenAIBillingProfileChatGPTSubscription,
				})
				require.NoError(t, err)
				return cost
			}

			standard := calculate("")
			fast := calculate("priority")
			require.False(t, standard.LongContextBillingApplied)
			require.False(t, fast.LongContextBillingApplied)
			require.InDelta(t, standard.InputCost*2.5, fast.InputCost, 1e-10)
			require.InDelta(t, standard.OutputCost*2.5, fast.OutputCost, 1e-10)
			require.InDelta(t, standard.CacheCreationCost*2.5, fast.CacheCreationCost, 1e-10)
			require.InDelta(t, standard.CacheReadCost*2.5, fast.CacheReadCost, 1e-10)
		})
	}
}

func TestGPT56APIProfilePreservesPriorityAndLongContextPricing(t *testing.T) {
	for _, totalInput := range []int{272_000, 272_001} {
		for _, tier := range []struct {
			name       string
			value      string
			multiplier float64
		}{
			{name: "standard", value: "", multiplier: 1},
			{name: "priority", value: "priority", multiplier: 2},
		} {
			t.Run(fmt.Sprintf("%s/%d", tier.name, totalInput), func(t *testing.T) {
				tokens := gpt56BoundaryTokens(totalInput)
				cost := calculateGPT56ProfileCost(t, OpenAIBillingProfileAPI, tokens, tier.value, true, boolPtr(false))
				inputMultiplier := tier.multiplier
				outputMultiplier := tier.multiplier
				if totalInput > 272_000 {
					inputMultiplier *= 2
					outputMultiplier *= 1.5
				}
				requireGPT56CostMultipliers(t, cost, tokens, inputMultiplier, outputMultiplier)
				require.Equal(t, totalInput > 272_000, cost.LongContextBillingApplied)
			})
		}
	}
}

func TestGPT56APIProfileWithoutResolverKeepsUserAndAccountStatsAligned(t *testing.T) {
	billing := newTestBillingService()
	tokens := gpt56BoundaryTokens(272_001)
	accountGate := false
	userCost, err := billing.CalculateCostUnified(CostInput{
		Model:                     "gpt-5.6-sol",
		Tokens:                    tokens,
		RateMultiplier:            1,
		OpenAIBillingProfile:      OpenAIBillingProfileAPI,
		LongContextBillingEnabled: &accountGate,
	})
	require.NoError(t, err)
	accountStatsCost := tryModelFilePricing(
		billing, "gpt-5.6-sol", tokens, "", OpenAIBillingProfileAPI, false,
	)
	require.NotNil(t, accountStatsCost)
	requireGPT56CostMultipliers(t, userCost, tokens, 1, 1)
	require.InDelta(t, userCost.TotalCost, *accountStatsCost, 1e-10)
	require.False(t, userCost.LongContextBillingApplied)
}

func TestGPT56SubscriptionProfileAlignsUserAccountStatsAndAggregateCost(t *testing.T) {
	tokens := gpt56BoundaryTokens(272_001)
	userCost := calculateGPT56ProfileCost(
		t, OpenAIBillingProfileChatGPTSubscription, tokens, "priority", true, boolPtr(true),
	)
	billing := newTestBillingService()
	accountStatsCost := tryModelFilePricing(
		billing, "gpt-5.6-sol", tokens, "priority", OpenAIBillingProfileChatGPTSubscription, true,
	)
	require.NotNil(t, accountStatsCost)
	accountRateMultiplier := 1.0
	accountCost := *accountStatsCost * accountRateMultiplier
	require.InDelta(t, userCost.TotalCost, userCost.ActualCost, 1e-10)
	require.InDelta(t, userCost.TotalCost, *accountStatsCost, 1e-10)
	require.InDelta(t, userCost.TotalCost, accountCost, 1e-10)
}

func TestGPT56APIProfileFastAliasRemainsPriorityTwoTimes(t *testing.T) {
	tier := normalizeOpenAIServiceTier("fast")
	require.NotNil(t, tier)
	tokens := gpt56BoundaryTokens(272_000)
	cost := calculateGPT56ProfileCost(t, OpenAIBillingProfileAPI, tokens, *tier, true, boolPtr(false))
	requireGPT56CostMultipliers(t, cost, tokens, 2, 2)
}

func TestOpenAIRecordUsageGPT56SubscriptionFastAcrossHTTPAndWebSocket(t *testing.T) {
	for _, transport := range []struct {
		name   string
		wsMode bool
	}{
		{name: "http responses", wsMode: false},
		{name: "responses websocket", wsMode: true},
	} {
		t.Run(transport.name, func(t *testing.T) {
			groupID := int64(56)
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(
				usageRepo,
				&openAIRecordUsageUserRepoStub{},
				&openAIRecordUsageSubRepoStub{},
				nil,
			)
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			svc.channelService = newTestChannelServiceForStats(t, &Channel{
				ID:     1,
				Status: StatusActive,
			}, groupID, PlatformOpenAI)
			serviceTier := "priority"
			group := &Group{
				ID:                        groupID,
				Platform:                  PlatformOpenAI,
				RateMultiplier:            1,
				LongContextPricingEnabled: true,
			}

			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID:    fmt.Sprintf("gpt56_subscription_%t", transport.wsMode),
					Model:        "gpt-5.6-sol",
					ServiceTier:  &serviceTier,
					OpenAIWSMode: transport.wsMode,
					Duration:     time.Second,
					Usage: OpenAIUsage{
						InputTokens:              272_001,
						OutputTokens:             1_000,
						CacheCreationInputTokens: 50_000,
						CacheReadInputTokens:     50_000,
					},
				},
				APIKey: &APIKey{ID: 1, GroupID: &groupID, Group: group},
				User:   &User{ID: 2},
				Account: &Account{
					ID:       3,
					Platform: PlatformOpenAI,
					Type:     AccountTypeOAuth,
					Extra: map[string]any{
						openAILongContextBillingEnabledKey: true,
					},
				},
				InboundEndpoint: "/v1/responses",
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, transport.wsMode, usageRepo.lastLog.OpenAIWSMode)
			require.False(t, usageRepo.lastLog.LongContextBillingApplied)

			tokens := gpt56BoundaryTokens(272_001)
			require.InDelta(t, float64(tokens.InputTokens)*5e-6*2.5, usageRepo.lastLog.InputCost, 1e-10)
			require.InDelta(t, float64(tokens.OutputTokens)*30e-6*2.5, usageRepo.lastLog.OutputCost, 1e-10)
			require.InDelta(t, float64(tokens.CacheCreationTokens)*6.25e-6*2.5, usageRepo.lastLog.CacheCreationCost, 1e-10)
			require.InDelta(t, float64(tokens.CacheReadTokens)*0.5e-6*2.5, usageRepo.lastLog.CacheReadCost, 1e-10)
			require.InDelta(t, usageRepo.lastLog.TotalCost, usageRepo.lastLog.ActualCost, 1e-10)
			require.NotNil(t, usageRepo.lastLog.AccountStatsCost)
			require.InDelta(t, usageRepo.lastLog.TotalCost, *usageRepo.lastLog.AccountStatsCost, 1e-10)
			accountRateMultiplier := 1.0
			if usageRepo.lastLog.AccountRateMultiplier != nil {
				accountRateMultiplier = *usageRepo.lastLog.AccountRateMultiplier
			}
			accountCost := *usageRepo.lastLog.AccountStatsCost * accountRateMultiplier
			require.InDelta(t, usageRepo.lastLog.TotalCost, accountCost, 1e-10)
		})
	}
}

func TestOpenAIRecordUsageGPT56ShadowUsesResolvedOAuthParentProfile(t *testing.T) {
	parentID := int64(9056)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	accountRepo := &openAIRecordUsageAccountRepoStub{account: &Account{
		ID:       parentID,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			openAILongContextBillingEnabledKey: false,
		},
	}}
	svc := newOpenAIRecordUsageServiceForTest(
		usageRepo,
		&openAIRecordUsageUserRepoStub{},
		&openAIRecordUsageSubRepoStub{},
		nil,
	)
	svc.accountRepo = accountRepo
	apiKey := openAIRecordUsageAPIKeyWithGroup(svc, 1056, true)
	serviceTier := "priority"

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:   "gpt56_subscription_shadow",
			Model:       "gpt-5.6-sol",
			ServiceTier: &serviceTier,
			Duration:    time.Second,
			Usage:       OpenAIUsage{InputTokens: 272_001, OutputTokens: 1_000},
		},
		APIKey: apiKey,
		User:   &User{ID: 2056},
		Account: &Account{
			ID:              3056,
			Platform:        PlatformOpenAI,
			Type:            AccountTypeOAuth,
			ParentAccountID: &parentID,
			QuotaDimension:  QuotaDimensionSpark,
			Extra: map[string]any{
				openAILongContextBillingEnabledKey: true,
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, accountRepo.calls)
	require.NotNil(t, usageRepo.lastLog)
	require.False(t, usageRepo.lastLog.LongContextBillingApplied)
	require.InDelta(t, 272_001*5e-6*2.5, usageRepo.lastLog.InputCost, 1e-10)
	require.InDelta(t, 1_000*30e-6*2.5, usageRepo.lastLog.OutputCost, 1e-10)
}
