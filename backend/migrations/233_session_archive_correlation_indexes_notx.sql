-- CAPYBARA-PATCH: 高写入表的关联查询索引必须并发创建，避免阻塞网关写入。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_correlation_request_id ON usage_logs(correlation_request_id) WHERE correlation_request_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_prompt_audit_jobs_correlation_request_id ON prompt_audit_jobs(correlation_request_id) WHERE correlation_request_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_prompt_audit_events_correlation_request_id ON prompt_audit_events(correlation_request_id) WHERE correlation_request_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ops_error_logs_correlation_request_id ON ops_error_logs(correlation_request_id) WHERE correlation_request_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_ops_system_logs_correlation_request_id ON ops_system_logs(correlation_request_id) WHERE correlation_request_id IS NOT NULL;
