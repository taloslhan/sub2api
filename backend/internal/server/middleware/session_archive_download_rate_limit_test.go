package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSessionArchiveDownloadRateLimitUsesFixedLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	router := gin.New()
	router.GET("/download/:ticket", SessionArchiveDownloadRateLimit(client), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	for i := 0; i < sessionArchiveDownloadRequestsPerMinute; i++ {
		request := httptest.NewRequest(http.MethodGet, "/download/ticket", nil)
		request.RemoteAddr = "203.0.113.10:12345"
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusNoContent, response.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/download/ticket", nil)
	request.RemoteAddr = "203.0.113.10:12345"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
}

func TestSessionArchiveDownloadRateLimitFailsClosedWhenRedisUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })

	router := gin.New()
	router.GET("/download/:ticket", SessionArchiveDownloadRateLimit(client), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/download/ticket", nil)
	request.RemoteAddr = "203.0.113.10:12345"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
}
