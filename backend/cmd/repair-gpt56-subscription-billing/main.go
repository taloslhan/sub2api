// repair-gpt56-subscription-billing 一次性修正 GPT-5.6 订阅账号计费口径错误。
//
// 默认只做 dry-run。只有显式传入 --execute 才会写回 usage_logs，并同步重算
// dashboard 聚合。--from 与 --to 都必须使用带时区的 RFC3339 时间。
//
// CAPYBARA-PATCH: 修正 2026-08-29 当天 GPT-5.6 订阅 credits 历史计费。
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
	execute := flag.Bool("execute", false, "write recomputed costs and rebuild aggregates (default is dry-run)")
	allowDrift := flag.Bool("allow-drift", false, "allow --execute when stored costs cannot be reproduced exactly")
	batchSize := flag.Int("batch-size", 1000, "scan/update batch size (1-5000)")
	topN := flag.Int("top", 20, "model/user/drift rows to print (0 = all)")
	flag.Parse()

	from, to := parseWindow(*fromRaw, *toRaw)
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

	pricingService, err := service.ProvidePricingService(cfg, repository.ProvidePricingRemoteClient(cfg))
	if err != nil {
		log.Fatalf("initialize pricing service: %v", err)
	}
	defer pricingService.Stop()

	groupRepo := repository.NewGroupRepository(client, db)
	channelRepo := repository.NewChannelRepository(db)
	billingService := service.NewBillingService(cfg, pricingService)
	channelService := service.NewChannelService(channelRepo, groupRepo, nil, pricingService)
	r := &repairer{
		db:             db,
		groupRepo:      groupRepo,
		channelService: channelService,
		billingService: billingService,
		resolver:       service.NewModelPricingResolver(channelService, billingService),
		groupCache:     make(map[int64]*service.Group),
	}

	ctx := context.Background()
	if *execute {
		preflight, runErr := r.run(ctx, from, to, *batchSize, false)
		if runErr != nil {
			log.Fatalf("preflight failed: %v", runErr)
		}
		if preflight.unrepairable > 0 || (preflight.drift > 0 && !*allowDrift) {
			preflight.print(false, from, to, *topN)
			if preflight.unrepairable > 0 {
				log.Fatalf("refusing to --execute: %d selected rows have no recomputable candidate", preflight.unrepairable)
			}
			log.Fatalf("refusing to --execute: %d rows have recompute drift; investigate first or pass --allow-drift deliberately", preflight.drift)
		}
	}

	sum, err := r.run(ctx, from, to, *batchSize, *execute)
	if err != nil {
		log.Fatalf("repair failed: %v", err)
	}
	sum.print(*execute, from, to, *topN)
	if !*execute {
		fmt.Println("dry-run finished; inspect account type drift and passthrough-account SQL checks before re-running with --execute")
		return
	}

	aggStart := from.Add(-time.Hour)
	aggEnd := to.Add(time.Hour)
	if err := repository.NewDashboardAggregationRepository(db).RecomputeRange(ctx, aggStart, aggEnd); err != nil {
		log.Fatalf("dashboard aggregation recompute failed for [%s, %s): %v; detail rows are already updated, re-run this command",
			aggStart.Format(time.RFC3339), aggEnd.Format(time.RFC3339), err)
	}
	fmt.Printf("aggregation recomputed range=[%s, %s)\n", aggStart.Format(time.RFC3339), aggEnd.Format(time.RFC3339))
	fmt.Println("repair complete")
}

func parseWindow(fromRaw, toRaw string) (time.Time, time.Time) {
	if fromRaw == "" {
		log.Fatal("--from is required")
	}
	if toRaw == "" {
		log.Fatal("--to is required")
	}
	from, err := time.Parse(time.RFC3339, fromRaw)
	if err != nil {
		log.Fatalf("invalid --from: %v", err)
	}
	to, err := time.Parse(time.RFC3339, toRaw)
	if err != nil {
		log.Fatalf("invalid --to: %v", err)
	}
	if !to.After(from) {
		log.Fatal("--to must be after --from")
	}
	return from, to
}
