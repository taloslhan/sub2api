package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultModelsIncludeBareGPT56Alias(t *testing.T) {
	require.Contains(t, DefaultModelIDs(), "gpt-5.6")
}

func TestDefaultModelsIncludeDaybreakBlueAlias(t *testing.T) {
	require.Equal(t, "gpt-daybreak-blue-latest", DaybreakBlueModelID)
	require.Equal(t, "gpt-5.6-sol", DaybreakBlueUnderlyingModelID)
	require.Contains(t, DefaultModelIDs(), DaybreakBlueModelID)
}

func TestDefaultModelsPreferConcreteGPT56SolForAccountTests(t *testing.T) {
	require.NotEmpty(t, DefaultModels)
	require.Equal(t, "gpt-5.6-sol", DefaultModels[0].ID)
}
