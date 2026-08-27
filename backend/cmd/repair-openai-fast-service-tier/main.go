// repair-openai-fast-service-tier 一次性修正 OpenAI 网关 fast 档位计费缺陷造成的历史 usage_logs。
//
// 背景：客户端发 service_tier: "fast"（归一化为 priority）时，上游回显的
// service_tier: "default" 曾覆盖客户端档位，导致按基准档（倍率 1.0）落库计费，少收费。
// 生产代码已修复，本工具只负责修正已经产生的错误历史记录。
//
// 默认 dry-run，只打印命中行数与新旧费用对比；确认无误后再加 --execute 真正写库。
//
// 用法：
//
//	go run ./cmd/repair-openai-fast-service-tier --from 2026-08-01T00:00:00+08:00 --to 2026-08-20T00:00:00+08:00
//	go run ./cmd/repair-openai-fast-service-tier --from ... --to ... --execute
//
// 本工具刻意没有放进 backend/migrations/：迁移会随版本自动执行，而本次修正的时间
// 范围依赖只有运维知道的外部事实（缺陷引入时刻与修复部署时刻），不具备自动执行的语义。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func main() {
	fromRaw := flag.String("from", "", "required RFC3339 lower bound (inclusive) on usage_logs.created_at")
	toRaw := flag.String("to", "", "required RFC3339 upper bound (exclusive) on usage_logs.created_at")
	execute := flag.Bool("execute", false, "write the recomputed costs (default is dry-run)")
	batchSize := flag.Int("batch-size", 1000, "scan/update batch size (1-5000)")
	topN := flag.Int("top", 20, "how many model/user rows to print in the per-dimension summary (0 = all)")
	flag.Parse()

	// 时间窗口必须显式传入：本次修正只应覆盖运维确认的缺陷存续区间，
	// 任何默认值都可能把无关时段的正常记录一起改掉。
	if *fromRaw == "" {
		log.Fatal("--from is required")
	}
	if *toRaw == "" {
		log.Fatal("--to is required")
	}
	from, err := time.Parse(time.RFC3339, *fromRaw)
	if err != nil {
		log.Fatalf("invalid --from: %v", err)
	}
	to, err := time.Parse(time.RFC3339, *toRaw)
	if err != nil {
		log.Fatalf("invalid --to: %v", err)
	}
	if !to.After(from) {
		log.Fatal("--to must be after --from")
	}
	if *batchSize < 1 || *batchSize > 5000 {
		log.Fatal("--batch-size must be between 1 and 5000")
	}
	if *topN < 0 {
		log.Fatal("--top must be >= 0")
	}

	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	client, db, err := repository.InitEnt(cfg)
	if err != nil {
		log.Fatalf("initialize database: %v", err)
	}
	defer func() { _ = client.Close() }()

	// 依赖组装照抄 cmd/server/wire_gen.go 的计费链路。
	// 必须构造出完整的 ModelPricingResolver：CalculateCostUnified 在 Resolver == nil 时
	// 会把 channelPricing 硬编码为 nil，跳过 channel_model_pricing 的渠道定价覆盖，
	// 也无法触发按次/图片计费分支，算出来的费用是错的。
	pricingService, err := service.ProvidePricingService(cfg, repository.ProvidePricingRemoteClient(cfg))
	if err != nil {
		log.Fatalf("initialize pricing service: %v", err)
	}
	defer pricingService.Stop()

	groupRepo := repository.NewGroupRepository(client, db)
	channelRepo := repository.NewChannelRepository(db)
	billingService := service.NewBillingService(cfg, pricingService)
	// authCacheInvalidator 传 nil：本工具只读渠道配置，不触发任何鉴权缓存失效。
	channelService := service.NewChannelService(channelRepo, groupRepo, nil, pricingService)
	resolver := service.NewModelPricingResolver(channelService, billingService)

	ctx := context.Background()
	r := &repairer{
		db:             db,
		groupRepo:      groupRepo,
		channelService: channelService,
		billingService: billingService,
		resolver:       resolver,
		groupCache:     make(map[int64]*service.Group),
	}

	summary, err := r.run(ctx, from, to, *batchSize, *execute)
	if err != nil {
		log.Fatalf("repair failed: %v", err)
	}
	summary.print(*execute, from, to, *topN)

	if !*execute {
		fmt.Println("dry-run finished; re-run with --execute to apply")
		return
	}

	// 聚合重算必须同步执行：service 层的 TriggerRecomputeRange 内部是 go func(){...}()，
	// 不暴露完成信号，CLI 进程退出会直接把那个 goroutine 杀掉。
	//
	// 区间固定取显式传入的 [--from, --to) 向外各扩一个聚合桶（最小桶为小时；
	// RecomputeRange 内部会把天桶对齐到自然日），而不是「本次实际修改行」的时间范围：
	// 若明细已更新后 CLI 崩溃，重跑时这些行因为已经是 priority 而不再命中，
	// 按「本次修改行」取范围会得到空区间，聚合桶将永久停留在旧值。
	//
	// 即使命中/更新行数为 0 也要执行，这样「重跑一次」始终能修复聚合。
	aggStart := from.Add(-time.Hour)
	aggEnd := to.Add(time.Hour)
	if err := repository.NewDashboardAggregationRepository(db).RecomputeRange(ctx, aggStart, aggEnd); err != nil {
		log.Fatalf("dashboard aggregation recompute failed for [%s, %s): %v; detail rows are already updated, re-run this command to repair the aggregates",
			aggStart.UTC().Format(time.RFC3339), aggEnd.UTC().Format(time.RFC3339), err)
	}
	fmt.Printf("aggregation recomputed range=[%s, %s)\n",
		aggStart.UTC().Format(time.RFC3339), aggEnd.UTC().Format(time.RFC3339))

	// 分组日汇总由 usage_logs 的 UPDATE 触发器自动失效重建（触发器监听
	// created_at / group_id / actual_cost，本次会改 actual_cost），无需显式处理。
	fmt.Println("repair complete")
}
