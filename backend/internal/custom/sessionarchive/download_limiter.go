package sessionarchive

import (
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type downloadWindow struct {
	start time.Time
	count int
}

// NewFixedWindowDownloadLimiter 提供下载端点专用的实例级固定窗口限流。
// 多实例部署仍应在路由层注入 Redis-backed limiter；本实现是安全的本地兜底。
func NewFixedWindowDownloadLimiter(maxRequests int, window time.Duration) DownloadLimiterFunc {
	if maxRequests < 1 {
		maxRequests = 10
	}
	if window <= 0 {
		window = time.Minute
	}
	var mu sync.Mutex
	entries := make(map[string]downloadWindow)
	return func(c *gin.Context) bool {
		now := time.Now()
		key := c.ClientIP()
		mu.Lock()
		defer mu.Unlock()
		current := entries[key]
		if current.start.IsZero() || now.Sub(current.start) >= window {
			entries[key] = downloadWindow{start: now, count: 1}
			return true
		}
		if current.count >= maxRequests {
			return false
		}
		current.count++
		entries[key] = current
		if len(entries) > 4096 {
			for candidate, value := range entries {
				if now.Sub(value.start) >= window {
					delete(entries, candidate)
				}
			}
		}
		return true
	}
}
