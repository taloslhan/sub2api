//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateOpsAlertRulePayloadAcceptsSessionArchiveMetrics(t *testing.T) {
	t.Parallel()

	metrics := []string{
		"session_archive_queue_dropped",
		"session_archive_storage_failures",
		"session_archive_pending_backlog",
		"session_archive_gc_backlog",
	}
	for _, metric := range metrics {
		metric := metric
		t.Run(metric, func(t *testing.T) {
			t.Parallel()
			input := map[string]json.RawMessage{
				"name":        json.RawMessage(`"archive health"`),
				"metric_type": json.RawMessage(`"` + metric + `"`),
				"operator":    json.RawMessage(`">"`),
				"threshold":   json.RawMessage(`0`),
			}
			validated, err := validateOpsAlertRulePayload(input)
			require.NoError(t, err)
			require.Equal(t, metric, validated.MetricType)
		})
	}
}
