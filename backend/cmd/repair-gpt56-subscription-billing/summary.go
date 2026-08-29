package main

// CAPYBARA-PATCH: 修数报告分别统计双向差额、用户/模型分布与 drift 候选。

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type dimensionStat struct {
	key       string
	rows      int64
	changes   int64
	oldTotal  float64
	newTotal  float64
	oldActual float64
	newActual float64
}

type driftDetail struct {
	id         int64
	userID     int64
	candidates string
}

type summary struct {
	selected         int64
	reproduced       int64
	skippedUnchanged int64
	alreadyConverged int64
	needsChange      int64
	updated          int64
	drift            int64
	unrepairable     int64
	channelPriced    int64

	accountStatsNonNull int64
	statsBranches       map[statsBranch]int64
	statsDrift          int64

	oldTotal       float64
	newTotal       float64
	oldActual      float64
	newActual      float64
	totalIncrease  float64
	totalDecrease  float64
	actualIncrease float64
	actualDecrease float64

	byModel map[string]*dimensionStat
	byUser  map[string]*dimensionStat
	drifts  []driftDetail
}

func newSummary() *summary {
	return &summary{
		statsBranches: make(map[statsBranch]int64),
		byModel:       make(map[string]*dimensionStat),
		byUser:        make(map[string]*dimensionStat),
	}
}

func (s *summary) add(row *logRow, plan updatePlan) {
	s.selected++
	if row.accountStatsCost != nil {
		s.accountStatsNonNull++
	}
	if plan.unrepairable {
		s.unrepairable++
		s.drifts = append(s.drifts, driftDetail{id: row.id, userID: row.userID, candidates: describeCandidates(plan.candidates)})
		return
	}
	if plan.drift {
		s.drift++
		s.drifts = append(s.drifts, driftDetail{id: row.id, userID: row.userID, candidates: describeCandidates(plan.candidates)})
	} else {
		s.reproduced++
	}
	if plan.alreadyNew {
		s.alreadyConverged++
	}
	if plan.needsChange {
		s.needsChange++
	} else {
		s.skippedUnchanged++
	}
	if plan.pricingSource == "channel" {
		s.channelPriced++
	}
	if row.accountStatsCost != nil {
		s.statsBranches[plan.statsBranch]++
		if plan.statsDrift {
			s.statsDrift++
		}
	}

	s.oldTotal += row.totalCost
	s.newTotal += plan.newCost.TotalCost
	s.oldActual += row.actualCost
	s.newActual += plan.newCost.ActualCost
	accumulateDirectional(plan.newCost.TotalCost-row.totalCost, &s.totalIncrease, &s.totalDecrease)
	accumulateDirectional(plan.newCost.ActualCost-row.actualCost, &s.actualIncrease, &s.actualDecrease)
	s.accumulate(s.byModel, plan.billingModel, row, plan)
	s.accumulate(s.byUser, strconv.FormatInt(row.userID, 10), row, plan)
}

func accumulateDirectional(delta float64, increase, decrease *float64) {
	if delta > 0 {
		*increase += delta
	} else if delta < 0 {
		*decrease += delta
	}
}

func (s *summary) accumulate(dst map[string]*dimensionStat, key string, row *logRow, plan updatePlan) {
	stat := dst[key]
	if stat == nil {
		stat = &dimensionStat{key: key}
		dst[key] = stat
	}
	stat.rows++
	if plan.needsChange {
		stat.changes++
	}
	stat.oldTotal += row.totalCost
	stat.newTotal += plan.newCost.TotalCost
	stat.oldActual += row.actualCost
	stat.newActual += plan.newCost.ActualCost
}

func describeCandidates(candidates []candidateCost) string {
	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.err != nil {
			parts = append(parts, fmt.Sprintf("%q(error=%v)", candidate.model, candidate.err))
			continue
		}
		parts = append(parts, fmt.Sprintf("%q(source=%s old=%.10f new=%.10f)",
			candidate.model, candidate.source, candidate.old.TotalCost, candidate.new.TotalCost))
	}
	return strings.Join(parts, ", ")
}

func (s *summary) print(execute bool, from, to time.Time, topN int) {
	mode := "dry-run"
	if execute {
		mode = "execute"
	}
	fmt.Printf("mode=%s from=%s to=%s selected=%d reproduced=%d unchanged=%d already_converged=%d needs_change=%d updated=%d drift=%d unrepairable=%d\n",
		mode, from.Format(time.RFC3339), to.Format(time.RFC3339),
		s.selected, s.reproduced, s.skippedUnchanged, s.alreadyConverged,
		s.needsChange, s.updated, s.drift, s.unrepairable)
	fmt.Printf("total_cost old=%.10f new=%.10f net=%.10f increase=%.10f decrease=%.10f\n",
		s.oldTotal, s.newTotal, s.newTotal-s.oldTotal, s.totalIncrease, s.totalDecrease)
	fmt.Printf("actual_cost old=%.10f new=%.10f net=%.10f increase=%.10f decrease=%.10f\n",
		s.oldActual, s.newActual, s.newActual-s.oldActual, s.actualIncrease, s.actualDecrease)
	fmt.Printf("pricing_source channel=%d account_stats_non_null=%d branches={custom_rule:%d apply_pricing:%d model_file:%d} stats_drift=%d\n",
		s.channelPriced, s.accountStatsNonNull,
		s.statsBranches[branchCustomRule], s.statsBranches[branchApplyPricing],
		s.statsBranches[branchModelFile], s.statsDrift)

	printDimension("model", s.byModel, topN)
	printDimension("user", s.byUser, topN)
	limit := len(s.drifts)
	if topN > 0 && limit > topN {
		limit = topN
	}
	for i := 0; i < limit; i++ {
		detail := s.drifts[i]
		fmt.Printf("drift usage_log_id=%d user_id=%d candidates=[%s]\n", detail.id, detail.userID, detail.candidates)
	}
}

func printDimension(label string, stats map[string]*dimensionStat, topN int) {
	items := make([]*dimensionStat, 0, len(stats))
	for _, stat := range stats {
		items = append(items, stat)
	}
	sort.Slice(items, func(i, j int) bool {
		left := math.Abs(items[i].newTotal - items[i].oldTotal)
		right := math.Abs(items[j].newTotal - items[j].oldTotal)
		if left != right {
			return left > right
		}
		return items[i].key < items[j].key
	})
	if topN > 0 && len(items) > topN {
		items = items[:topN]
	}
	for _, stat := range items {
		fmt.Printf("%s=%s rows=%d changes=%d total_old=%.10f total_new=%.10f total_delta=%.10f actual_old=%.10f actual_new=%.10f actual_delta=%.10f\n",
			label, stat.key, stat.rows, stat.changes,
			stat.oldTotal, stat.newTotal, stat.newTotal-stat.oldTotal,
			stat.oldActual, stat.newActual, stat.newActual-stat.oldActual)
	}
}
