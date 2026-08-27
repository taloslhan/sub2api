package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// dimensionStat 是「按模型」或「按用户」维度的新旧总额对比。
type dimensionStat struct {
	key        string
	rows       int64
	oldTotal   float64
	newTotal   float64
	oldActual  float64
	newActual  float64
	oldStats   float64
	newStats   float64
	hasStatsNN bool
}

type summary struct {
	matched int64
	updated int64

	oldTotal  float64
	newTotal  float64
	oldActual float64
	newActual float64

	// recomputeDrift 统计「按旧档位重算出来的 total_cost 与落库值对不上」的行数。
	// 常见原因是定价文件或渠道定价在落库之后变过，也可能是当初的 billingModel
	// 与 usage_logs.model 不同（BillingModel / ChannelMappedModel / OriginalModel
	// 覆盖都没有落库）。差额里属于这部分的不是本次档位修正带来的。
	recomputeDrift int64

	statsBranches   map[statsBranch]int64
	statsValueDrift int64

	byModel map[string]*dimensionStat
	byUser  map[string]*dimensionStat
}

func newSummary() *summary {
	return &summary{
		statsBranches: make(map[statsBranch]int64),
		byModel:       make(map[string]*dimensionStat),
		byUser:        make(map[string]*dimensionStat),
	}
}

func (s *summary) add(row *logRow, plan updatePlan) {
	s.matched++
	s.oldTotal += row.totalCost
	s.newTotal += plan.cost.TotalCost
	s.oldActual += row.actualCost
	s.newActual += plan.cost.ActualCost

	if !nearlyEqual(plan.oldRecomputed, row.totalCost) {
		s.recomputeDrift++
	}
	s.statsBranches[plan.statsBranch]++
	if plan.statsValueDrift {
		s.statsValueDrift++
	}

	s.accumulate(s.byModel, row.billingModel(), row, plan)
	s.accumulate(s.byUser, strconv.FormatInt(row.userID, 10), row, plan)
}

func (s *summary) accumulate(dst map[string]*dimensionStat, key string, row *logRow, plan updatePlan) {
	stat, ok := dst[key]
	if !ok {
		stat = &dimensionStat{key: key}
		dst[key] = stat
	}
	stat.rows++
	stat.oldTotal += row.totalCost
	stat.newTotal += plan.cost.TotalCost
	stat.oldActual += row.actualCost
	stat.newActual += plan.cost.ActualCost
	if row.accountStatsCost != nil {
		stat.hasStatsNN = true
		stat.oldStats += *row.accountStatsCost
		if plan.statsCost != nil {
			stat.newStats += *plan.statsCost
		}
	}
}

func (s *summary) print(execute bool, from, to time.Time, topN int) {
	mode := "dry-run"
	if execute {
		mode = "execute"
	}
	fmt.Printf("mode=%s from=%s to=%s matched=%d updated=%d\n",
		mode,
		from.UTC().Format(time.RFC3339),
		to.UTC().Format(time.RFC3339),
		s.matched, s.updated)

	fmt.Printf("total_cost old=%.6f new=%.6f delta=%.6f\n", s.oldTotal, s.newTotal, s.newTotal-s.oldTotal)
	fmt.Printf("actual_cost old=%.6f new=%.6f delta=%.6f\n", s.oldActual, s.newActual, s.newActual-s.oldActual)

	fmt.Printf("account_stats_cost null=%d custom_rule=%d apply_pricing=%d model_file=%d\n",
		s.statsBranches[branchNull],
		s.statsBranches[branchCustomRule],
		s.statsBranches[branchApplyPricing],
		s.statsBranches[branchModelFile])

	if s.recomputeDrift > 0 {
		fmt.Printf("WARN recompute_drift rows=%d (recomputing the OLD tier does not reproduce the stored total_cost; pricing config likely changed since these rows were written)\n",
			s.recomputeDrift)
	}
	if s.statsValueDrift > 0 {
		fmt.Printf("WARN account_stats_drift rows=%d (the inferred source branch does not reproduce the stored account_stats_cost; the \"channel config unchanged since write\" assumption may not hold)\n",
			s.statsValueDrift)
	}

	printDimension("model", s.byModel, topN)
	printDimension("user", s.byUser, topN)
}

func printDimension(label string, stats map[string]*dimensionStat, topN int) {
	items := make([]*dimensionStat, 0, len(stats))
	for _, stat := range stats {
		items = append(items, stat)
	}
	// 按差额绝对值降序，让影响最大的先被人工核对；差额相同则按 key 稳定排序。
	sort.Slice(items, func(i, j int) bool {
		di := math.Abs(items[i].newTotal - items[i].oldTotal)
		dj := math.Abs(items[j].newTotal - items[j].oldTotal)
		if di != dj {
			return di > dj
		}
		return items[i].key < items[j].key
	})
	if topN > 0 && len(items) > topN {
		items = items[:topN]
	}
	for _, stat := range items {
		line := fmt.Sprintf("%s=%s rows=%d total_old=%.6f total_new=%.6f total_delta=%.6f actual_old=%.6f actual_new=%.6f actual_delta=%.6f",
			label, stat.key, stat.rows,
			stat.oldTotal, stat.newTotal, stat.newTotal-stat.oldTotal,
			stat.oldActual, stat.newActual, stat.newActual-stat.oldActual)
		if stat.hasStatsNN {
			line += fmt.Sprintf(" stats_old=%.6f stats_new=%.6f stats_delta=%.6f",
				stat.oldStats, stat.newStats, stat.newStats-stat.oldStats)
		}
		fmt.Println(line)
	}
}
