-- CAPYBARA-PATCH: 删除 Session 后阻止迟到 Collector 事件按相同 correlation 复活归档。

CREATE TABLE IF NOT EXISTS session_archive_correlation_fences (
    tenant_id BIGINT NOT NULL DEFAULT 0,
    user_id BIGINT NOT NULL DEFAULT 0,
    api_key_id BIGINT NOT NULL DEFAULT 0,
    protocol VARCHAR(64) NOT NULL,
    correlation_request_id VARCHAR(128) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, user_id, api_key_id, protocol, correlation_request_id)
);

CREATE INDEX IF NOT EXISTS idx_session_archive_correlation_fences_expiry
    ON session_archive_correlation_fences(expires_at);
