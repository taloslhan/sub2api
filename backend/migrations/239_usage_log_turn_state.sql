-- CAPYBARA-PATCH: 管理员使用日志保留上游 state 快照；历史数据保持 NULL。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS upstream_request_turn_state TEXT,
    ADD COLUMN IF NOT EXISTS upstream_response_turn_state TEXT,
    ADD COLUMN IF NOT EXISTS turn_state_transport TEXT,
    ADD COLUMN IF NOT EXISTS turn_state_connection_reused BOOLEAN;
