package middleware

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/custom/sessionarchive"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// BindSessionArchiveRequiredAuditActor 把认证后的管理员快照放入 request context。
// 快照刻意排除 header、ticket 与请求正文，供 context-only 的归档必要审计端口使用。
func BindSessionArchiveRequiredAuditActor() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil {
			return
		}
		subject, ok := GetAuthSubjectFromContext(c)
		if !ok || subject.UserID <= 0 {
			c.Next()
			return
		}
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		role, _ := GetUserRoleFromContext(c)
		actor := service.RequiredAuditActor{
			UserID: subject.UserID, Email: c.GetString(ContextKeyAuthEmail), Role: role,
			AuthMethod: c.GetString("auth_method"), Method: c.Request.Method, Path: path,
			ClientIP: SecurityClientIP(c), UserAgent: c.Request.UserAgent(),
		}
		c.Request = c.Request.WithContext(service.WithRequiredAuditActor(c.Request.Context(), actor))
		c.Next()
	}
}

// NewSessionArchiveAdminID 提供归档 Handler 所需的已认证管理员 ID 读取器。
func NewSessionArchiveAdminID() sessionarchive.AdminIDFunc {
	return func(c *gin.Context) (int64, bool) {
		subject, ok := GetAuthSubjectFromContext(c)
		return subject.UserID, ok && subject.UserID > 0
	}
}

// NewSessionArchiveRequiredAudit 创建同步必要审计适配器。
// 仅持久化固定动作、规范化目标与标量摘要，绝不接收 ticket、正文或请求凭据。
func NewSessionArchiveRequiredAudit(auditService *service.AuditLogService) sessionarchive.RequiredAuditFunc {
	return func(ctx context.Context, action, target string, extra map[string]any) error {
		if auditService == nil {
			return errors.New("required audit service unavailable")
		}
		actor, ok := service.RequiredAuditActorFromContext(ctx)
		if !ok {
			adminID, parsed := sessionArchiveTicketAdminID(target)
			if !parsed {
				return errors.New("required audit actor unavailable")
			}
			actor = service.RequiredAuditActor{
				UserID: adminID, Role: service.RoleAdmin, AuthMethod: "export_ticket",
				Method: "GET", Path: "/api/v1/session-archive/download/:ticket",
			}
			if binding := service.SessionBindingFromContext(ctx); binding != nil {
				actor.ClientIP, actor.UserAgent = binding.IP, binding.UserAgent
			}
		}

		actorID := actor.UserID
		entry := &service.AuditLog{
			CreatedAt: time.Now().UTC(), ActorUserID: &actorID,
			ActorEmail: actor.Email, ActorRole: actor.Role, AuthMethod: actor.AuthMethod,
			Action: strings.TrimSpace(action), Method: actor.Method, Path: actor.Path,
			ClientIP: actor.ClientIP, UserAgent: actor.UserAgent, StatusCode: 200,
			Extra: sessionArchiveAuditExtra(target, extra),
		}
		if requestID, exists := ctx.Value(ctxkey.CorrelationRequestID).(string); exists {
			entry.RequestID = strings.TrimSpace(requestID)
		} else if requestID, exists := ctx.Value(ctxkey.RequestID).(string); exists {
			entry.RequestID = strings.TrimSpace(requestID)
		}
		return auditService.RecordRequired(ctx, entry)
	}
}

func sessionArchiveTicketAdminID(target string) (int64, bool) {
	const prefix = "admin:"
	if !strings.HasPrefix(target, prefix) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(target, prefix)), 10, 64)
	return id, err == nil && id > 0
}

func sessionArchiveAuditExtra(target string, input map[string]any) map[string]any {
	extra := map[string]any{"target": normalizeSessionArchiveAuditTarget(target)}
	for key, value := range input {
		switch key {
		case "request_id", "stored_bytes", "scope_id", "matched_sessions":
			extra[key] = value
		case "kind":
			extra[key] = normalizeSessionArchiveContentKind(fmt.Sprint(value))
		case "scope_type", "state", "format":
			extra[key] = truncateAuditExtraString(fmt.Sprint(value), 32)
		}
	}
	return extra
}

func normalizeSessionArchiveAuditTarget(target string) string {
	parts := strings.Split(strings.TrimSpace(target), ":")
	if len(parts) < 2 {
		return "session_archive"
	}
	if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
		return "session_archive"
	}
	switch parts[0] {
	case "admin", "sessions", "global", "group", "user", "api_key":
		return parts[0] + ":" + parts[1]
	case "request":
		if len(parts) == 3 {
			return "request:" + parts[1] + ":" + normalizeSessionArchiveContentKind(parts[2])
		}
	}
	return "session_archive"
}

func normalizeSessionArchiveContentKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "request", "upstream", "response", "tool", "attachment", "raw":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "invalid"
	}
}
