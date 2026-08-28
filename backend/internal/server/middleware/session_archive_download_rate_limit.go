package middleware

import (
	"time"

	basemiddleware "github.com/Wei-Shaw/sub2api/internal/middleware"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const sessionArchiveDownloadRequestsPerMinute = 20

// SessionArchiveDownloadRateLimit 保护位于管理员认证组外的 capability 下载地址。
// ticket 本身是 bearer 凭据，因此 Redis 故障时必须 fail-closed。
func SessionArchiveDownloadRateLimit(redisClient *redis.Client) gin.HandlerFunc {
	return basemiddleware.NewRateLimiter(redisClient).LimitWithOptions(
		"session_archive_download",
		sessionArchiveDownloadRequestsPerMinute,
		time.Minute,
		basemiddleware.RateLimitOptions{FailureMode: basemiddleware.RateLimitFailClose},
	)
}
