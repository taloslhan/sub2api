# Custom Changes

上游源码中的侵入式修改清单，用于同步上游时核对二开补丁是否仍然有效。

| 日期 | 文件 | 原因 |
| --- | --- | --- |
| 2026-07-13 | `backend/internal/handler/admin/grok_oauth_handler.go` | 修复上游 `v0.1.153` 中 Grok 配额重置错误分支触发的 staticcheck SA4023；该接口按设计始终返回“不支持”错误。 |
