-- CAPYBARA-PATCH: 会话归档窄投影与私有 CAS 引用/删除状态机。

CREATE TABLE IF NOT EXISTS session_archive_sessions (
    id BIGSERIAL PRIMARY KEY,
    tenant_id BIGINT NOT NULL DEFAULT 0,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    protocol VARCHAR(64) NOT NULL,
    client VARCHAR(128) NOT NULL DEFAULT '',
    first_model VARCHAR(255) NOT NULL DEFAULT '',
    last_model VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    capture_coverage VARCHAR(32) NOT NULL DEFAULT 'full',
    stable_id_digest VARCHAR(64) NOT NULL DEFAULT '',
    merge_method VARCHAR(32) NOT NULL DEFAULT 'new',
    policy_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    deleting_at TIMESTAMPTZ,
    CONSTRAINT chk_session_archive_session_status CHECK (status IN ('active', 'completed', 'failed', 'deleting')),
    CONSTRAINT chk_session_archive_capture_coverage CHECK (capture_coverage IN ('full', 'control_plane_only'))
);

CREATE TABLE IF NOT EXISTS session_archive_turns (
    id BIGSERIAL PRIMARY KEY,
    session_id BIGINT NOT NULL REFERENCES session_archive_sessions(id) ON DELETE CASCADE,
    sequence_no INT NOT NULL,
    protocol_turn_id VARCHAR(255) NOT NULL DEFAULT '',
    message_chain_digest VARCHAR(64) NOT NULL DEFAULT '',
    message_chain_hashes JSONB NOT NULL DEFAULT '[]'::jsonb,
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    UNIQUE (session_id, sequence_no),
    CONSTRAINT chk_session_archive_turn_status CHECK (status IN ('active', 'completed', 'failed', 'cancelled')),
    CONSTRAINT chk_session_archive_message_chain_hashes CHECK (jsonb_typeof(message_chain_hashes) = 'array')
);

CREATE TABLE IF NOT EXISTS session_archive_requests (
    id BIGSERIAL PRIMARY KEY,
    turn_id BIGINT NOT NULL REFERENCES session_archive_turns(id) ON DELETE CASCADE,
    correlation_request_id VARCHAR(128) NOT NULL,
    billing_request_id VARCHAR(128) NOT NULL DEFAULT '',
    client_request_id VARCHAR(255) NOT NULL DEFAULT '',
    upstream_request_id VARCHAR(255) NOT NULL DEFAULT '',
    endpoint VARCHAR(255) NOT NULL DEFAULT '',
    model VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    error_class VARCHAR(64) NOT NULL DEFAULT '',
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    client_disconnected BOOLEAN NOT NULL DEFAULT FALSE,
    has_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    policy_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    CONSTRAINT chk_session_archive_request_status CHECK (status IN ('active', 'completed', 'failed', 'cancelled')),
    CONSTRAINT chk_session_archive_request_metadata CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS session_archive_attempts (
    id BIGSERIAL PRIMARY KEY,
    request_id BIGINT NOT NULL REFERENCES session_archive_requests(id) ON DELETE CASCADE,
    attempt_no INT NOT NULL,
    account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    transform_type VARCHAR(64) NOT NULL DEFAULT '',
    upstream_request_id VARCHAR(255) NOT NULL DEFAULT '',
    upstream_status INT,
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    error_class VARCHAR(64) NOT NULL DEFAULT '',
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    duration_ms BIGINT NOT NULL DEFAULT 0,
    is_final BOOLEAN NOT NULL DEFAULT FALSE,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    UNIQUE (request_id, attempt_no),
    CONSTRAINT chk_session_archive_attempt_status CHECK (status IN ('active', 'completed', 'failed', 'cancelled')),
    CONSTRAINT chk_session_archive_attempt_nonnegative CHECK (attempt_no >= 0 AND duration_ms >= 0)
);

CREATE TABLE IF NOT EXISTS session_archive_blobs (
    id BIGSERIAL PRIMARY KEY,
    stored_plaintext_sha256 CHAR(64) NOT NULL,
    stored_bytes BIGINT NOT NULL,
    compressed_bytes BIGINT NOT NULL DEFAULT 0,
    ciphertext_bytes BIGINT NOT NULL DEFAULT 0,
    gzip_version SMALLINT NOT NULL DEFAULT 1,
    format_version SMALLINT NOT NULL DEFAULT 1,
    key_id VARCHAR(128) NOT NULL,
    object_key TEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    owner_token VARCHAR(64) NOT NULL DEFAULT '',
    lease_expires_at TIMESTAMPTZ,
    gc_after TIMESTAMPTZ,
    retry_count INT NOT NULL DEFAULT 0,
    last_error VARCHAR(512) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (stored_plaintext_sha256, format_version, key_id),
    UNIQUE (object_key),
    CONSTRAINT chk_session_archive_blob_status CHECK (status IN ('pending', 'ready', 'failed', 'gc_pending', 'deleting')),
    CONSTRAINT chk_session_archive_blob_sizes CHECK (stored_bytes >= 0 AND compressed_bytes >= 0 AND ciphertext_bytes >= 0 AND retry_count >= 0)
);

CREATE TABLE IF NOT EXISTS session_archive_blob_refs (
    id BIGSERIAL PRIMARY KEY,
    blob_id BIGINT REFERENCES session_archive_blobs(id) ON DELETE SET NULL,
    owner_type VARCHAR(32) NOT NULL,
    owner_id BIGINT NOT NULL,
    purpose VARCHAR(64) NOT NULL,
    content_type VARCHAR(255) NOT NULL DEFAULT 'application/octet-stream',
    observed_sha256 CHAR(64) NOT NULL DEFAULT '',
    observed_bytes BIGINT NOT NULL DEFAULT 0,
    stored_bytes BIGINT NOT NULL DEFAULT 0,
    truncated BOOLEAN NOT NULL DEFAULT FALSE,
    dropped_reason VARCHAR(64) NOT NULL DEFAULT '',
    sequence_no BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (owner_type, owner_id, purpose, sequence_no),
    CONSTRAINT chk_session_archive_ref_owner CHECK (owner_type IN ('session', 'turn', 'request', 'attempt')),
    CONSTRAINT chk_session_archive_ref_sizes CHECK (observed_bytes >= 0 AND stored_bytes >= 0 AND sequence_no >= 0)
);

CREATE TABLE IF NOT EXISTS session_archive_policies (
    id BIGSERIAL PRIMARY KEY,
    scope_type VARCHAR(32) NOT NULL,
    scope_id BIGINT NOT NULL DEFAULT 0,
    state VARCHAR(16) NOT NULL DEFAULT 'inherit',
    capture_raw_request BOOLEAN NOT NULL DEFAULT TRUE,
    capture_upstream_request BOOLEAN NOT NULL DEFAULT FALSE,
    capture_response BOOLEAN NOT NULL DEFAULT TRUE,
    capture_tools BOOLEAN NOT NULL DEFAULT TRUE,
    capture_attachments BOOLEAN NOT NULL DEFAULT TRUE,
    payload_max_bytes BIGINT NOT NULL DEFAULT 67108864,
    retention_days INT NOT NULL DEFAULT 30,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (scope_type, scope_id),
    CONSTRAINT chk_session_archive_policy_scope CHECK (
        (scope_type = 'global' AND scope_id = 0) OR
        (scope_type IN ('group', 'user', 'api_key') AND scope_id > 0)
    ),
    CONSTRAINT chk_session_archive_policy_state CHECK (state IN ('inherit', 'on', 'off')),
    CONSTRAINT chk_session_archive_policy_limits CHECK (payload_max_bytes > 0 AND payload_max_bytes <= 67108864 AND retention_days BETWEEN 1 AND 3650)
);

CREATE TABLE IF NOT EXISTS session_archive_deletion_jobs (
    id BIGSERIAL PRIMARY KEY,
    requested_by BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    normalized_filter JSONB NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    target_count BIGINT NOT NULL DEFAULT 0,
    processed_count BIGINT NOT NULL DEFAULT 0,
    deleted_count BIGINT NOT NULL DEFAULT 0,
    released_blob_count BIGINT NOT NULL DEFAULT 0,
    failed_count BIGINT NOT NULL DEFAULT 0,
    retry_count INT NOT NULL DEFAULT 0,
    last_error VARCHAR(512) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_session_archive_deletion_status CHECK (status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    CONSTRAINT chk_session_archive_deletion_counts CHECK (target_count >= 0 AND processed_count >= 0 AND deleted_count >= 0 AND released_blob_count >= 0 AND failed_count >= 0 AND retry_count >= 0),
    CONSTRAINT chk_session_archive_deletion_filter CHECK (jsonb_typeof(normalized_filter) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_session_archive_sessions_tenant_time ON session_archive_sessions(tenant_id, last_active_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_session_archive_sessions_user_time ON session_archive_sessions(user_id, last_active_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_session_archive_sessions_api_key_time ON session_archive_sessions(api_key_id, last_active_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_session_archive_sessions_group_time ON session_archive_sessions(group_id, last_active_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_session_archive_sessions_model_time ON session_archive_sessions(last_model, last_active_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_session_archive_sessions_client_time ON session_archive_sessions(client, last_active_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_session_archive_sessions_status_time ON session_archive_sessions(status, last_active_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_session_archive_sessions_expiry ON session_archive_sessions(expires_at, id) WHERE status <> 'deleting';
CREATE INDEX IF NOT EXISTS idx_session_archive_sessions_stable ON session_archive_sessions(tenant_id, user_id, api_key_id, protocol, stable_id_digest) WHERE stable_id_digest <> '';
CREATE INDEX IF NOT EXISTS idx_session_archive_turns_session_sequence ON session_archive_turns(session_id, sequence_no);
CREATE INDEX IF NOT EXISTS idx_session_archive_requests_turn_time ON session_archive_requests(turn_id, started_at, id);
CREATE INDEX IF NOT EXISTS idx_session_archive_requests_correlation ON session_archive_requests(correlation_request_id);
CREATE INDEX IF NOT EXISTS idx_session_archive_attempts_request_attempt ON session_archive_attempts(request_id, attempt_no);
CREATE INDEX IF NOT EXISTS idx_session_archive_blob_refs_owner ON session_archive_blob_refs(owner_type, owner_id, sequence_no, id);
CREATE INDEX IF NOT EXISTS idx_session_archive_blob_refs_blob ON session_archive_blob_refs(blob_id) WHERE blob_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_archive_blobs_pending ON session_archive_blobs(status, lease_expires_at, id) WHERE status IN ('pending', 'failed');
CREATE INDEX IF NOT EXISTS idx_session_archive_blobs_gc ON session_archive_blobs(status, gc_after, id) WHERE status = 'gc_pending';
CREATE INDEX IF NOT EXISTS idx_session_archive_deletion_jobs_status ON session_archive_deletion_jobs(status, created_at, id);

INSERT INTO session_archive_policies(scope_type, scope_id, state)
VALUES ('global', 0, 'off')
ON CONFLICT (scope_type, scope_id) DO NOTHING;
