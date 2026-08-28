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
	compat, err := os.ReadFile("../../../migrations/236_session_archive_storage_backends_compat.sql")
	require.NoError(t, err)
	finalize, err := os.ReadFile("../../../migrations/237_session_archive_storage_backends_finalize.sql")
	require.NoError(t, err)

	require.Contains(t, string(foundation), "UNIQUE (owner_type, owner_id, purpose, sequence_no)")
	require.NotContains(t, string(ordering), "DROP CONSTRAINT", "234 must not invalidate AddBlobRef's four-column ON CONFLICT target")
	require.Contains(t, string(fences), "CREATE TABLE IF NOT EXISTS session_archive_correlation_fences")
	require.Contains(t, string(fences), "PRIMARY KEY (tenant_id, user_id, api_key_id, protocol, correlation_request_id)")
	require.Contains(t, string(fences), "idx_session_archive_correlation_fences_expiry")
	require.Contains(t, string(compat), "storage_backend VARCHAR(32) NOT NULL DEFAULT 's3'")
	require.Contains(t, string(compat), "uq_session_archive_blobs_backend_cas")
	require.Contains(t, string(compat), "session_archive_pg_object_chunks")
	require.Contains(t, string(compat), "next_retry_at TIMESTAMPTZ NOT NULL DEFAULT NOW()")
	require.NotContains(t, string(compat), "DROP CONSTRAINT")
	require.Contains(t, string(finalize), "pg_constraint")
	require.Contains(t, string(finalize), "pg_attribute")
	require.NotContains(t, string(finalize), "session_archive_blobs_stored_plaintext_sha256_format_version_key_id_key")
}
