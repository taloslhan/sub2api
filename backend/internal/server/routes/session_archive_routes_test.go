package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/custom/sessionarchive"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSessionArchiveManagementRoutesUseAdminAuthWithoutForcedStepUpAndDownloadStaysOutsideAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	archiveService, err := sessionarchive.NewService(context.Background(), sessionarchive.ServiceOptions{
		Config: config.SessionArchiveConfig{Enabled: false},
	})
	require.NoError(t, err)
	archiveHandler, err := sessionarchive.NewHandler(sessionarchive.HandlerOptions{
		Service: archiveService, DownloadLimiter: func(*gin.Context) bool { return true },
	})
	require.NoError(t, err)

	router := gin.New()
	router.Use(gin.Recovery())
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{SessionArchive: archiveHandler}}
	adminAuthCalls := 0
	adminAuth := servermiddleware.AdminAuthMiddleware(func(c *gin.Context) {
		adminAuthCalls++
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 7})
		c.Set(string(servermiddleware.ContextKeyUserRole), service.RoleAdmin)
		c.Set("auth_method", service.AuditAuthMethodJWT)
		c.Next()
	})
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })
	requiredAudit := servermiddleware.RequiredAuditMiddleware(func(string) gin.HandlerFunc {
		return func(c *gin.Context) { c.Next() }
	})
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	RegisterAdminRoutes(
		router.Group("/api/v1"), handlers, adminAuth, auditLog, stepUp,
		requiredAudit, nil, nil, servermiddleware.SessionArchiveDownloadRateLimit(redisClient),
	)

	for _, item := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/admin/session-archive/requests/8/content?kind=raw", ""},
		{http.MethodPut, "/api/v1/admin/session-archive/policies", `{}`},
		{http.MethodDelete, "/api/v1/admin/session-archive/policies", ""},
		{http.MethodPost, "/api/v1/admin/session-archive/export-preflight", `{}`},
		{http.MethodPost, "/api/v1/admin/session-archive/export-tickets", `{}`},
		{http.MethodPost, "/api/v1/admin/session-archive/deletion-jobs", `{}`},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
		router.ServeHTTP(response, request)
		require.NotEqual(t, http.StatusUnauthorized, response.Code, "%s %s", item.method, item.path)
		require.NotEqual(t, http.StatusForbidden, response.Code, "%s %s", item.method, item.path)
	}
	require.Equal(t, 6, adminAuthCalls)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/session-archive/download/opaque-ticket", nil)
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, 6, adminAuthCalls, "download capability route must stay outside admin authentication")
}
