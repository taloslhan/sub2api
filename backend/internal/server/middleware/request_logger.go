package middleware

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const requestIDHeader = "X-Request-ID"

const sessionArchiveDownloadPathPrefix = "/api/v1/session-archive/download/"

func requestLogPath(path string) string {
	if strings.HasPrefix(path, sessionArchiveDownloadPathPrefix) {
		return sessionArchiveDownloadPathPrefix + ":ticket"
	}
	return path
}

// RequestLogger 在请求入口注入 request-scoped logger。
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil {
			c.Next()
			return
		}

		requestID, validRequestID := normalizeCorrelationID(c.GetHeader(requestIDHeader))
		if !validRequestID {
			requestID = uuid.NewString()
		}
		c.Header(requestIDHeader, requestID)

		ctx := context.WithValue(c.Request.Context(), ctxkey.RequestID, requestID)
		// CAPYBARA-PATCH: HTTP 每请求关联 ID 复用网关已有 request ID，但保持独立 context key，
		// 后续 usage 计费键仍由 resolveUsageBillingRequestID 单独解析。
		ctx = context.WithValue(ctx, ctxkey.CorrelationRequestID, requestID)
		clientRequestID, _ := ctx.Value(ctxkey.ClientRequestID).(string)
		clientRequestID, _ = normalizeCorrelationID(clientRequestID)

		requestLogger := logger.With(
			zap.String("component", "http"),
			zap.String("request_id", requestID),
			// CAPYBARA-PATCH: Ops system-log sink 将关联 ID 写入独立索引列。
			zap.String("correlation_request_id", requestID),
			zap.String("client_request_id", strings.TrimSpace(clientRequestID)),
			zap.String("path", requestLogPath(c.Request.URL.Path)),
			zap.String("method", c.Request.Method),
		)

		ctx = logger.IntoContext(ctx, requestLogger)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
