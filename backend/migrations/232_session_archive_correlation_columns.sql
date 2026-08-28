-- CAPYBARA-PATCH: 新关联键与计费 request_id 分离，既有高写入表只增加可空列。
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS correlation_request_id VARCHAR(128);
ALTER TABLE prompt_audit_jobs ADD COLUMN IF NOT EXISTS correlation_request_id VARCHAR(128);
ALTER TABLE prompt_audit_events ADD COLUMN IF NOT EXISTS correlation_request_id VARCHAR(128);
ALTER TABLE ops_error_logs ADD COLUMN IF NOT EXISTS correlation_request_id VARCHAR(128);
ALTER TABLE ops_system_logs ADD COLUMN IF NOT EXISTS correlation_request_id VARCHAR(128);
