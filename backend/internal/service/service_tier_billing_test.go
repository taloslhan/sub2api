package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveBillingServiceTier(t *testing.T) {
	tests := []struct {
		name       string
		requested  string
		observed   string
		billing    string
		downgraded bool
	}{
		{name: "openai priority served as default", requested: "priority", observed: "default", billing: "default", downgraded: true},
		{name: "anthropic fast served as standard", requested: "fast", observed: "standard", billing: "standard", downgraded: true},
		{name: "priority honoured", requested: "priority", observed: "priority", billing: "priority"},
		{name: "no declaration keeps request", requested: "priority", observed: "", billing: "priority"},
		{name: "no request no declaration", requested: "", observed: "", billing: ""},
		{name: "response never raises the tier", requested: "", observed: "priority", billing: ""},
		{name: "flex never raised to default", requested: "flex", observed: "default", billing: "flex"},
		{name: "default echoed for untiered request", requested: "", observed: "default", billing: ""},
		{name: "unknown response tier ignored", requested: "priority", observed: "turbo", billing: "priority"},
		{name: "case and whitespace normalised", requested: " Priority ", observed: "DEFAULT", billing: "default", downgraded: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveBillingServiceTier(tt.requested, tt.observed)
			require.Equal(t, tt.billing, got.Billing)
			require.Equal(t, tt.downgraded, got.Downgraded)
		})
	}
}

// CAPYBARA-PATCH: client-requested fast wins over the upstream echo — the
// OpenAI entry point exempts fast/priority from the only-lowers rule, while the
// Anthropic entry point keeps downgrading. The two are asserted separately so
// the divergence stays explicit.
func TestApplyServiceTierBillingResolution(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		// CAPYBARA-PATCH: client-requested fast wins over the upstream echo.
		t.Run("fast is exempt from the upstream default echo", func(t *testing.T) {
			requested := "priority"
			result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "default"}
			resolution := ApplyOpenAIServiceTierBillingResolution(result)
			require.False(t, resolution.Downgraded)
			require.Equal(t, "priority", resolution.Requested)
			require.Equal(t, "default", resolution.Observed, "the echo is still reported for the audit log")
			require.Equal(t, "priority", resolution.Billing)
			require.Same(t, &requested, result.ServiceTier, "an exempt tier must not be rewritten")
		})

		// CAPYBARA-PATCH: client-requested fast wins over the upstream echo.
		t.Run("fast alias is exempt too", func(t *testing.T) {
			requested := "fast"
			result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "default"}
			resolution := ApplyOpenAIServiceTierBillingResolution(result)
			require.False(t, resolution.Downgraded)
			require.True(t, isPriorityBillingServiceTier(resolution.Billing),
				"both fast and priority settle on a fast-priced tier")
			require.Same(t, &requested, result.ServiceTier)
		})

		t.Run("honoured tier keeps pointer", func(t *testing.T) {
			requested := "priority"
			result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "priority"}
			require.False(t, ApplyOpenAIServiceTierBillingResolution(result).Downgraded)
			require.Same(t, &requested, result.ServiceTier)
		})

		t.Run("untiered request stays nil", func(t *testing.T) {
			result := &OpenAIForwardResult{UpstreamResponseServiceTier: "priority"}
			require.False(t, ApplyOpenAIServiceTierBillingResolution(result).Downgraded)
			require.Nil(t, result.ServiceTier)
		})

		// CAPYBARA-PATCH: the exemption is scoped to fast/priority; flex keeps the
		// original only-lowers behaviour.
		t.Run("flex is not exempt", func(t *testing.T) {
			requested := "flex"
			result := &OpenAIForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "default"}
			resolution := ApplyOpenAIServiceTierBillingResolution(result)
			require.False(t, resolution.Downgraded, "default never lowers flex")
			require.Equal(t, "flex", resolution.Billing)
			require.Same(t, &requested, result.ServiceTier)
		})

		t.Run("nil result is ignored", func(t *testing.T) {
			require.False(t, ApplyOpenAIServiceTierBillingResolution(nil).Downgraded)
		})
	})

	t.Run("anthropic", func(t *testing.T) {
		t.Run("standard speed rewrites fast", func(t *testing.T) {
			requested := "fast"
			result := &ForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "standard"}
			resolution := ApplyForwardServiceTierBillingResolution(result)
			require.True(t, resolution.Downgraded, "the Anthropic side keeps the only-lowers rule")
			require.Equal(t, "standard", resolution.Billing)
			require.Equal(t, "standard", *result.ServiceTier)
		})

		t.Run("honoured fast keeps pointer", func(t *testing.T) {
			requested := "fast"
			result := &ForwardResult{ServiceTier: &requested, UpstreamResponseServiceTier: "fast"}
			require.False(t, ApplyForwardServiceTierBillingResolution(result).Downgraded)
			require.Same(t, &requested, result.ServiceTier)
		})

		t.Run("nil result is ignored", func(t *testing.T) {
			require.False(t, ApplyForwardServiceTierBillingResolution(nil).Downgraded)
		})
	})
}
