# Custom Changes

上游源码中的侵入式修改清单，用于同步上游时核对二开补丁是否仍然有效。

| 日期 | 文件 | 原因 |
| --- | --- | --- |
| ~~2026-07-13~~<br>2026-08-25 撤除 | ~~`backend/internal/handler/admin/grok_oauth_handler.go`~~ | ~~修复上游 `v0.1.153` 中 Grok 配额重置错误分支触发的 staticcheck SA4023；该接口按设计始终返回“不支持”错误。~~ **已撤除**：上游 `v0.1.181` 的 `cbe258fd1` 已用 `//nolint:staticcheck` 修复同一问题，同步时采用上游版本。 |
| 2026-07-21 | `backend/Makefile` | 固定默认 `LDFLAGS`，避免 shell 环境变量意外覆盖 Go 链接参数，同时保留命令行显式覆盖能力。 |
| 2026-07-21 | `deploy/Makefile` | 固定默认 `LDFLAGS`，避免 shell 环境变量意外覆盖 Go 链接参数，同时保留命令行显式覆盖能力。 |
| 2026-08-08 | `backend/internal/handler/dto/settings.go`<br>`backend/internal/handler/admin/setting_handler.go`<br>`backend/internal/handler/admin/setting_handler_update.go` | Crisp 在线客服设置项（`crisp_enabled` / `crisp_website_id`）的 DTO 字段、读取与更新映射、Website ID UUID 校验。补登记 2026-07 的 `feat(settings): add Crisp customer support widget`。 |
| 2026-08-08 | `backend/internal/server/middleware/security_headers.go` | 在 `requiredCSPDirectiveValues` 注入表中加入 Crisp 域名常量（`https://*.crisp.chat`、`wss://*.relay.crisp.chat`、`wss://*.relay.rescue.crisp.chat`、`blob:`、`data:`）。补登记同上。 |
| 2026-08-08 | `backend/internal/config/config.go`<br>`deploy/config.example.yaml` | 将 Crisp 域名补入 `DefaultCSPPolicy` 默认策略字符串。上游 `0.1.172`（提交 `8e102b3a0`）新增不变量测试，要求默认策略与中间件注入表同形，否则示例配置会误导自建用户。 |
| ~~2026-08-08~~<br>2026-08-09 撤除 | ~~`frontend/src/api/__tests__/admin.system.rollback.spec.ts`~~ | ~~**修上游 bug**：上游 `35b5edb24` 给 `rollback()` 请求加了 `timeout` 选项却未同步本文件断言，`v0.1.162`~`v0.1.172` 纯净 main 上持续失败。补全断言第三参数（`UPDATE_REQUEST_TIMEOUT_MS` 未导出，测试内镜像其值）。~~ **已撤除**：上游 `0.1.173` 的 `85fb77615` 自行修复了同一问题，同步时取上游版本，本文件回归与 main 完全一致。 |
| 2026-08-25 | `backend/internal/handler/dto/types.go`<br>`backend/internal/handler/dto/mappers.go` | 用量明细 DTO 新增请求级 `output_tokens_per_second`（`output_tokens × 1000 / duration_ms`，含首 Token 等待）。仅 `output_tokens > 0 且 duration_ms > 0` 时有值，否则为 `nil` 并序列化为 `null`，不用 `0` 冒充有效速度。`AdminUsageLog` 靠嵌入继承，未复制字段。 |
| 2026-08-25 | `backend/internal/pkg/usagestats/usage_log_types.go`<br>`backend/internal/repository/usage_log_repo_stats.go` | `UsageStats` 新增 `average_output_tokens_per_second` / `output_tokens_per_second_samples` / `average_first_token_ms` / `first_token_ms_samples`。`GetStatsWithFilters` 的 `scoped` CTE 带出 `first_token_ms`，用 `AVG(...) FILTER (...)` 计算**逐请求速率的算术平均**（非总量加权），仅写入 `GROUPING SETS` 总计行，端点拆分结构不变。平均值用 `sql.NullFloat64` 扫描，无有效样本时返回 `null`。 |
| 2026-08-25 | `backend/internal/server/api_contract_test.go`<br>`backend/internal/repository/usage_log_repo_request_type_test.go` | 同步上游契约测试与 sqlmock 列定义，使其覆盖上述两组新字段。**同步上游时注意**：这两处会随上游改动冲突，需要重新对齐列顺序与期望 JSON。 |
| 2026-08-25 | `frontend/src/types/index.ts`<br>`frontend/src/api/admin/usage.ts`<br>`frontend/src/utils/format.ts` | 前端类型补齐上述可空字段（写成可选，兼容新前端访问旧后端）；`format.ts` 新增 `formatOutputTokensPerSecond`，统一两位小数、`tok/s` 单位与空值占位。 |
| 2026-08-25 | `frontend/src/components/admin/usage/UsageTable.vue`<br>`frontend/src/components/admin/usage/UsageStatsCards.vue` | 延迟单元格在首 Token、总耗时之外增加第三行输出吞吐（复用现有单元格，**不新增独立列**，避免两套列可见性 localStorage 迁移）；平均耗时卡扩展为性能卡，主值仍为平均耗时，附平均首 Token、平均输出吞吐及各自有效样本数，样本数为 0 时显示 `-`。 |
| 2026-08-25 | `frontend/src/views/user/UsageView.vue`<br>`frontend/src/views/admin/UsageView.vue`<br>`frontend/src/i18n/locales/{zh,en}/dashboard.ts` | 用户 CSV 与管理员 XLSX 在 Duration 之后增加输出吞吐列（纯数值、无效样本留空）。CSV 表头沿用该函数既有的硬编码英文风格，XLSX 表头走 i18n。新增 6 个 `usage.*` 双语键。 |
