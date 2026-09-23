ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS upstream_cyber_access_program VARCHAR(64);
