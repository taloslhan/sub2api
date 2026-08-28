package sessionarchive

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionArchiveMigrationContracts(t *testing.T) {
	foundation, err := os.ReadFile("../../../migrations/231_session_archive_foundation.sql")
	require.NoError(t, err)
	ordering, err := os.ReadFile("../../../migrations/234_session_archive_ref_ordering.sql")
	require.NoError(t, err)
	fences, err := os.ReadFile("../../../migrations/235_session_archive_correlation_fences.sql")
	require.NoError(t, err)

	require.Contains(t, string(foundation), "UNIQUE (owner_type, owner_id, purpose, sequence_no)")
	require.NotContains(t, string(ordering), "DROP CONSTRAINT", "234 must not invalidate AddBlobRef's four-column ON CONFLICT target")
	require.Contains(t, string(fences), "CREATE TABLE IF NOT EXISTS session_archive_correlation_fences")
	require.Contains(t, string(fences), "PRIMARY KEY (tenant_id, user_id, api_key_id, protocol, correlation_request_id)")
	require.Contains(t, string(fences), "idx_session_archive_correlation_fences_expiry")
}
