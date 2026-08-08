# Custom Changes

上游源码中的侵入式修改清单，用于同步上游时核对二开补丁是否仍然有效。

| 日期 | 文件 | 原因 |
| --- | --- | --- |
| 2026-07-13 | `backend/internal/handler/admin/grok_oauth_handler.go` | 修复上游 `v0.1.153` 中 Grok 配额重置错误分支触发的 staticcheck SA4023；该接口按设计始终返回“不支持”错误。 |
| 2026-07-21 | `backend/Makefile` | 固定默认 `LDFLAGS`，避免 shell 环境变量意外覆盖 Go 链接参数，同时保留命令行显式覆盖能力。 |
| 2026-07-21 | `deploy/Makefile` | 固定默认 `LDFLAGS`，避免 shell 环境变量意外覆盖 Go 链接参数，同时保留命令行显式覆盖能力。 |
| 2026-08-08 | `backend/internal/handler/dto/settings.go`<br>`backend/internal/handler/admin/setting_handler.go`<br>`backend/internal/handler/admin/setting_handler_update.go` | Crisp 在线客服设置项（`crisp_enabled` / `crisp_website_id`）的 DTO 字段、读取与更新映射、Website ID UUID 校验。补登记 2026-07 的 `feat(settings): add Crisp customer support widget`。 |
| 2026-08-08 | `backend/internal/server/middleware/security_headers.go` | 在 `requiredCSPDirectiveValues` 注入表中加入 Crisp 域名常量（`https://*.crisp.chat`、`wss://*.relay.crisp.chat`、`wss://*.relay.rescue.crisp.chat`、`blob:`、`data:`）。补登记同上。 |
| 2026-08-08 | `backend/internal/config/config.go`<br>`deploy/config.example.yaml` | 将 Crisp 域名补入 `DefaultCSPPolicy` 默认策略字符串。上游 `0.1.172`（提交 `8e102b3a0`）新增不变量测试，要求默认策略与中间件注入表同形，否则示例配置会误导自建用户。 |
| 2026-08-08 | `frontend/src/api/__tests__/admin.system.rollback.spec.ts` | **修上游 bug**：上游 `35b5edb24` 给 `rollback()` 请求加了 `timeout` 选项却未同步本文件断言，`v0.1.162`~`v0.1.172` 纯净 main 上持续失败。补全断言第三参数（`UPDATE_REQUEST_TIMEOUT_MS` 未导出，测试内镜像其值）。**上游修复后本补丁应撤除。** |
