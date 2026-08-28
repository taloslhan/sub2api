package sessionarchive

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/setup"

	"github.com/gin-gonic/gin"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
)

const exportTicketRedisPrefix = "session_archive:export_ticket:"

// ProvideService 构造进程级归档服务；外部存储自检与 worker 启动由应用生命周期负责。
func ProvideService(cfg *config.Config, db *sql.DB) (*Service, error) {
	return NewService(context.Background(), ServiceOptions{Config: cfg.SessionArchive, DB: db, DataDir: setup.GetDataDir()})
}

// ProvideRedisTicketStore 构造多实例共享、原子消费的短时导出票据存储。
func ProvideRedisTicketStore(client *redis.Client) (*RedisTicketStore, error) {
	return NewRedisTicketStore(client, exportTicketRedisPrefix)
}

// ProvideHandler 按现有 Handler API 汇总控制面依赖。
func ProvideHandler(
	archive *Service,
	tickets TicketStore,
	requiredAudit RequiredAuditFunc,
	adminID AdminIDFunc,
) (*Handler, error) {
	return NewHandler(HandlerOptions{
		Service: archive, Tickets: tickets, RequiredAudit: requiredAudit,
		MarkRequiredAudit: func(c *gin.Context) {
			c.Set("required_audit_recorded", true)
		},
		AdminID: adminID, DownloadLimiter: NewFixedWindowDownloadLimiter(20, time.Minute),
		TicketTTL: 2 * time.Minute,
	})
}

// ProviderSet 提供归档服务、控制面 Handler、Redis TicketStore 与 Ops 指标端口绑定。
var ProviderSet = wire.NewSet(
	ProvideService,
	ProvideRedisTicketStore,
	wire.Bind(new(TicketStore), new(*RedisTicketStore)),
	ProvideHandler,
	wire.Bind(new(service.SessionArchiveOpsMetrics), new(*Service)),
)
