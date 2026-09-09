package main

// CAPYBARA-PATCH: 按指定凭据账号修正固定日期；只更新账单与聚合，不触碰资金/配额。
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/custom/openaiprice"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const creditsFrom = "2026-09-07T00:00:00+08:00"
const creditsTo = "2026-09-08T00:00:00+08:00"

type creditsChange struct {
	ID           int64           `json:"id"`
	UserID       int64           `json:"user_id"`
	Model        string          `json:"billing_model"`
	FreeFast     bool            `json:"free_fast_verified"`
	Before       json.RawMessage `json:"before"`
	After        map[string]any  `json:"after"`
	SettingsHash string          `json:"settings_sha256"`
	ActualDelta  float64         `json:"accrued_minus_original_charge"`
}

type creditsSnapshot struct {
	AstraCacheDouble  bool            `json:"astra_cache_double"`
	AstraCacheRestore bool            `json:"astra_cache_restore"`
	Version           string          `json:"rate_card_version"`
	AccountID         int64           `json:"credential_account_id"`
	From              string          `json:"from"`
	To                string          `json:"to"`
	LegacySHA         string          `json:"legacy_catalog_sha256"`
	Selected          int             `json:"selected"`
	Changes           []creditsChange `json:"changes"`
	Errors            []string        `json:"errors"`
}

type creditsRepair struct {
	*repairer
	accountID         int64
	profile           service.OpenAIBillingProfile
	accountGate       bool
	astraCacheDouble  bool
	astraCacheRestore bool
}

func runCreditsRepair(accountID int64, path, legacyPath string, batchSize int, execute, astraCacheDouble, astraCacheRestore bool) error {
	if astraCacheDouble && astraCacheRestore {
		return fmt.Errorf("astra cache double and restore modes are mutually exclusive")
	}
	expectedVersion := "2026-09-07.v2"
	if astraCacheRestore {
		expectedVersion = "2026-09-08.v1"
	}
	if openaiprice.Version != expectedVersion {
		return fmt.Errorf("historical credits repair requires frozen rate card %s; current card is %s", expectedVersion, openaiprice.Version)
	}
	if accountID <= 0 || path == "" || legacyPath == "" || batchSize < 1 || batchSize > 5000 {
		return fmt.Errorf("credits repair requires --credits-account-id, --snapshot, --legacy-pricing-file and batch size 1..5000")
	}
	legacy, err := os.ReadFile(legacyPath)
	if err != nil {
		return err
	}
	legacySHA := fmt.Sprintf("%x", sha256.Sum256(legacy))
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		return err
	}
	if err = timezone.Init(cfg.Timezone); err != nil {
		return err
	}
	// 不调用 InitEnt：dry-run 不跑迁移，也不写 bootstrap secrets。
	drv, err := entsql.Open(dialect.Postgres, cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return err
	}
	client := ent.NewClient(ent.Driver(drv))
	defer func() { _ = client.Close() }()
	tmp, err := os.MkdirTemp("", "credits-legacy-pricing-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	cfg.Pricing = config.PricingConfig{DataDir: tmp, FallbackFile: legacyPath}
	pricing := service.NewPricingService(cfg, nil)
	if err = pricing.Initialize(); err != nil {
		return err
	}
	defer pricing.Stop()
	groups := repository.NewGroupRepository(client, drv.DB())
	channels := service.NewChannelService(repository.NewChannelRepository(drv.DB()), groups, nil, pricing, nil)
	billing := service.NewBillingService(cfg, pricing)
	r := &creditsRepair{repairer: &repairer{db: drv.DB(), groupRepo: groups, channelService: channels, billingService: billing,
		resolver: service.NewModelPricingResolver(channels, billing), groupCache: make(map[int64]*service.Group)}, accountID: accountID, astraCacheDouble: astraCacheDouble, astraCacheRestore: astraCacheRestore}
	fromRaw, toRaw := r.window()
	ctx := context.Background()
	var typ, platform string
	var parent sql.NullInt64
	if err = r.db.QueryRowContext(ctx, `SELECT type, platform, parent_account_id, COALESCE(extra->'openai_long_context_billing_enabled' = 'true'::jsonb, false) FROM accounts WHERE id=$1`, accountID).Scan(&typ, &platform, &parent, &r.accountGate); err != nil {
		return err
	}
	if platform != "openai" || parent.Valid || (typ != "oauth" && typ != "setup-token") {
		return fmt.Errorf("account %d must be an OpenAI subscription credential account", accountID)
	}
	r.profile = service.OpenAIBillingProfileChatGPTSubscription // 真实旧 profile；不是零值。
	if astraCacheDouble || astraCacheRestore {
		var enabled bool
		if err = r.db.QueryRowContext(ctx, `SELECT COALESCE(extra->'openai_credits_billing_enabled' = 'true'::jsonb, false) FROM accounts WHERE id=$1`, accountID).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return fmt.Errorf("account %d does not have credits billing enabled", accountID)
		}
		r.profile = service.OpenAIBillingProfileChatGPTCredits
	}
	if execute {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var snapshot creditsSnapshot
		if err = json.Unmarshal(data, &snapshot); err != nil {
			return err
		}
		if snapshot.AstraCacheDouble != astraCacheDouble || snapshot.AstraCacheRestore != astraCacheRestore || snapshot.AccountID != accountID || snapshot.From != fromRaw || snapshot.To != toRaw || snapshot.Version != openaiprice.Version || snapshot.LegacySHA != legacySHA || len(snapshot.Errors) != 0 {
			return fmt.Errorf("snapshot identity, catalog or preflight errors do not permit execution")
		}
		if err = r.applyCredits(ctx, snapshot.Changes, batchSize); err != nil {
			return err
		}
		from, _ := time.Parse(time.RFC3339, fromRaw)
		to, _ := time.Parse(time.RFC3339, toRaw)
		if err = repository.NewDashboardAggregationRepository(r.db).RecomputeRange(ctx, from, to); err != nil {
			return fmt.Errorf("details updated; rerun snapshot to rebuild aggregates: %w", err)
		}
		snapshot.print()
		fmt.Println("snapshot applied; aggregates rebuilt; balances, quotas and financial ledgers untouched")
		return nil
	}
	snapshot := creditsSnapshot{AstraCacheDouble: astraCacheDouble, AstraCacheRestore: astraCacheRestore, Version: openaiprice.Version, AccountID: accountID, From: fromRaw, To: toRaw, LegacySHA: legacySHA}
	var cursor int64
	for {
		rows, err := r.db.QueryContext(ctx, `SELECT ul.id, to_jsonb(ul) FROM usage_logs ul JOIN accounts a ON a.id=ul.account_id
		 WHERE COALESCE(a.parent_account_id,a.id)=$1 AND ul.created_at >= $2::timestamptz AND ul.created_at < $3::timestamptz AND ul.id>$4 ORDER BY ul.id LIMIT $5`, accountID, fromRaw, toRaw, cursor, batchSize)
		if err != nil {
			return err
		}
		var batch []json.RawMessage
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&cursor, &raw); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, raw)
		}
		err = rows.Err()
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, raw := range batch {
			change, selected, err := r.planCredits(ctx, raw)
			if selected {
				snapshot.Selected++
			}
			if err != nil {
				snapshot.Errors = append(snapshot.Errors, err.Error())
				continue
			}
			if change != nil {
				snapshot.Changes = append(snapshot.Changes, *change)
			}
		}
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("snapshot must be a new file: %w", err)
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	snapshot.print()
	if len(snapshot.Errors) > 0 {
		return fmt.Errorf("%d ambiguous records; snapshot cannot be executed", len(snapshot.Errors))
	}
	return nil
}

func (r *creditsRepair) planCredits(ctx context.Context, raw json.RawMessage) (*creditsChange, bool, error) {
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, false, err
	}
	number := func(k string) float64 { v, _ := row[k].(float64); return v }
	str := func(k string) string { v, _ := row[k].(string); return v }
	id := int64(number("id"))
	tier := strings.ToLower(strings.TrimSpace(str("service_tier")))
	switch tier {
	case "", "auto", "default", "standard", "priority", "fast":
	default:
		return nil, false, nil
	}
	if (str("billing_mode") != "" && str("billing_mode") != "token") || number("image_count") != 0 || number("video_count") != 0 {
		return nil, false, nil
	}
	models := (&logRow{model: str("model"), upstreamModel: str("upstream_model"), requestedModel: str("requested_model"), upstreamResponseModel: str("upstream_response_model")}).billingModelCandidates()
	var candidates []string
	seen := map[string]bool{}
	for _, model := range models {
		if (r.astraCacheDouble || r.astraCacheRestore) && service.UnifiedOpenAIModel(model) != "gpt-6-astra" {
			continue
		}
		if canonical := service.UnifiedOpenAIModel(model); canonical != "" && !seen[canonical] {
			seen[canonical] = true
			candidates = append(candidates, model)
		}
	}
	if len(candidates) == 0 {
		return nil, false, nil
	}
	var groupID *int64
	if row["group_id"] != nil {
		v := int64(number("group_id"))
		groupID = &v
	}
	group, err := r.group(ctx, groupID)
	if err != nil {
		return nil, true, err
	}
	at, err := time.Parse(time.RFC3339Nano, str("created_at"))
	if err != nil {
		return nil, true, err
	}
	tokens := service.UsageTokens{InputTokens: int(number("input_tokens")), OutputTokens: int(number("output_tokens")),
		CacheCreationTokens: int(number("cache_creation_tokens")), CacheReadTokens: int(number("cache_read_tokens")),
		CacheCreation5mTokens: int(number("cache_creation_5m_tokens")), CacheCreation1hTokens: int(number("cache_creation_1h_tokens")),
		ImageInputTokens: int(number("image_input_tokens")), ImageOutputTokens: int(number("image_output_tokens"))}
	var matched []*creditsChange
	for _, model := range candidates {
		input := service.CostInput{Ctx: ctx, Model: model, GroupID: groupID, Group: group, Tokens: tokens,
			RateMultiplier: number("rate_multiplier"), PricingAt: at, ServiceTier: tier, RequestCount: 1,
			OpenAIBillingProfile: r.profile, LongContextBillingEnabled: &r.accountGate}
		if group != nil {
			input.Resolver = r.resolver
		}
		old, err := r.billingService.CalculateCostUnified(input)
		if err != nil {
			continue
		}
		input.OpenAIBillingProfile = service.OpenAIBillingProfileChatGPTCredits
		updated, err := r.billingService.CalculateCostUnified(input)
		if err != nil {
			continue
		}
		if r.astraCacheDouble {
			old = scaleAstraCacheCost(old, .5)
		} else if r.astraCacheRestore {
			old = scaleAstraCacheCost(old, 2)
		}
		for _, free := range []bool{false, true} {
			if free && tier != "priority" && tier != "fast" {
				continue
			}
			oldActual, newActual := old.ActualCost, updated.ActualCost
			if free {
				input.ServiceTier = ""
				standard, e := r.billingService.CalculateCostUnified(input)
				if e != nil {
					continue
				}
				newActual = standard.ActualCost
				input.OpenAIBillingProfile = r.profile
				standard, e = r.billingService.CalculateCostUnified(input)
				if e != nil {
					continue
				}
				if r.astraCacheDouble {
					standard = scaleAstraCacheCost(standard, .5)
				} else if r.astraCacheRestore {
					standard = scaleAstraCacheCost(standard, 2)
				}
				oldActual = standard.ActualCost
			}
			// 优惠只凭历史金额匹配，不读取当前 Free Fast 配置。
			oldMatches := creditsAmountEqual(number("total_cost"), old.TotalCost) && creditsAmountEqual(number("actual_cost"), oldActual)
			newMatches := creditsAmountEqual(number("total_cost"), updated.TotalCost) && creditsAmountEqual(number("actual_cost"), newActual)
			if !oldMatches && !newMatches {
				continue
			}
			updated.ActualCost = newActual
			statsModel := str("upstream_model")
			if statsModel == "" {
				statsModel = model
			}
			if service.UnifiedOpenAIModel(statsModel) == "" || ((r.astraCacheDouble || r.astraCacheRestore) && service.UnifiedOpenAIModel(statsModel) != "gpt-6-astra") {
				continue
			}
			stats, e := r.billingService.CalculateCostUnified(service.CostInput{Model: statsModel, Tokens: tokens, RateMultiplier: 1, ServiceTier: tier, OpenAIBillingProfile: service.OpenAIBillingProfileChatGPTCredits})
			if e != nil {
				continue
			}
			// 非空旧统计需能由旧链或新链复现，不能强行覆盖来源不明的成本。
			if row["account_stats_cost"] != nil && !creditsAmountEqual(number("account_stats_cost"), stats.TotalCost) && !creditsAmountEqual(number("account_stats_cost"), old.TotalCost) {
				continue
			}
			after := creditsCostFields(updated, stats.TotalCost)
			matched = append(matched, &creditsChange{ID: id, UserID: int64(number("user_id")), Model: model, FreeFast: free, Before: raw, After: after, ActualDelta: newActual - number("actual_cost")})
		}
	}
	if len(matched) == 0 {
		return nil, true, fmt.Errorf("usage_log %d: historical model, profile, discount or configuration cannot be reproduced", id)
	}
	choice := matched[0]
	for _, candidate := range matched[1:] {
		if !sameCreditsFields(choice.After, candidate.After) {
			return nil, true, fmt.Errorf("usage_log %d: multiple billing interpretations", id)
		}
	}
	if sameCreditsFields(row, choice.After) {
		return nil, true, nil
	}
	choice.SettingsHash, err = r.settingsHash(ctx, groupID)
	return choice, true, err
}

// scaleAstraCacheCost 复现旧缓存价；业务倍率和 Fast 优惠分别沿原链计算。
func scaleAstraCacheCost(cost *service.CostBreakdown, multiplier float64) *service.CostBreakdown {
	previous := *cost
	previous.CacheReadCost = cost.CacheReadCost * multiplier
	previous.TotalCost = cost.TotalCost + previous.CacheReadCost - cost.CacheReadCost
	businessMultiplier := 0.0
	if cost.BusinessRateMultiplier != nil {
		businessMultiplier = *cost.BusinessRateMultiplier
	} else if cost.TotalCost != 0 {
		businessMultiplier = cost.ActualCost / cost.TotalCost
	}
	previous.ActualCost = previous.TotalCost * businessMultiplier
	return &previous
}

func creditsCostFields(cost *service.CostBreakdown, stats float64) map[string]any {
	return map[string]any{"input_cost": cost.InputCost, "image_input_cost": cost.ImageInputCost, "output_cost": cost.OutputCost,
		"image_output_cost": cost.ImageOutputCost, "cache_creation_cost": cost.CacheCreationCost, "cache_read_cost": cost.CacheReadCost,
		"total_cost": cost.TotalCost, "actual_cost": cost.ActualCost, "account_stats_cost": stats, "long_context_billing_applied": cost.LongContextBillingApplied}
}

func creditsAmountEqual(a, b float64) bool {
	return math.Abs(math.Round(a*1e10)-math.Round(b*1e10)) < .5
}

func sameCreditsFields(row, desired map[string]any) bool {
	for key, want := range desired {
		if value, ok := want.(float64); ok {
			got, valid := row[key].(float64)
			if !valid || !creditsAmountEqual(got, value) {
				return false
			}
		} else if !reflect.DeepEqual(row[key], want) {
			return false
		}
	}
	return true
}

func (r *creditsRepair) settingsHash(ctx context.Context, groupID *int64) (string, error) {
	group, err := r.group(ctx, groupID)
	if err != nil {
		return "", err
	}
	var channel *service.Channel
	if groupID != nil {
		channel, err = r.channelService.GetChannelForGroup(ctx, *groupID)
		if err != nil {
			return "", err
		}
	}
	// 仅计费输入；账号被正常使用产生的运行态字段不会误报配置漂移。
	data, err := json.Marshal(struct {
		Group   *service.Group
		Channel *service.Channel
		Profile service.OpenAIBillingProfile
		Gate    bool
	}{group, channel, r.profile, r.accountGate})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (r *creditsRepair) applyCredits(ctx context.Context, changes []creditsChange, batchSize int) error {
	for start := 0; start < len(changes); start += batchSize {
		end := start + batchSize
		if end > len(changes) {
			end = len(changes)
		}
		if err := r.applyCreditsBatch(ctx, changes[start:end]); err != nil {
			return err
		}
		fmt.Printf("committed %d/%d snapshot rows\n", end, len(changes))
	}
	return nil
}

func (r *creditsRepair) applyCreditsBatch(ctx context.Context, changes []creditsChange) error {
	fromRaw, toRaw := r.window()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, change := range changes {
		var before map[string]any
		if err := json.Unmarshal(change.Before, &before); err != nil {
			return err
		}
		var groupID *int64
		if n, ok := before["group_id"].(float64); ok {
			id := int64(n)
			groupID = &id
		}
		hash, err := r.settingsHash(ctx, groupID)
		if err != nil {
			return err
		}
		if hash != change.SettingsHash {
			return fmt.Errorf("usage_log %d: billing configuration drift since snapshot", change.ID)
		}
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT to_jsonb(ul) FROM usage_logs ul JOIN accounts a ON a.id=ul.account_id WHERE ul.id=$1 AND COALESCE(a.parent_account_id,a.id)=$2 AND ul.created_at >= $3::timestamptz AND ul.created_at < $4::timestamptz FOR UPDATE OF ul`, change.ID, r.accountID, fromRaw, toRaw).Scan(&raw); err != nil {
			return err
		}
		var current map[string]any
		if err := json.Unmarshal(raw, &current); err != nil {
			return err
		}
		// 幂等重跑只接受原记录或精确的新记录；tokens/请求/倍率均须保持不变。
		expected := make(map[string]any, len(before))
		for k, v := range before {
			expected[k] = v
		}
		for k, v := range change.After {
			expected[k] = v
		}
		if sameCreditsFields(current, expected) {
			continue
		}
		if !sameCreditsFields(current, before) {
			return fmt.Errorf("usage_log %d: original row changed since dry-run", change.ID)
		}
		p := change.After
		_, err = tx.ExecContext(ctx, `UPDATE usage_logs SET input_cost=$2,image_input_cost=$3,output_cost=$4,image_output_cost=$5,cache_creation_cost=$6,cache_read_cost=$7,total_cost=$8,actual_cost=$9,account_stats_cost=$10,long_context_billing_applied=$11 WHERE id=$1`,
			change.ID, p["input_cost"], p["image_input_cost"], p["output_cost"], p["image_output_cost"], p["cache_creation_cost"], p["cache_read_cost"], p["total_cost"], p["actual_cost"], p["account_stats_cost"], p["long_context_billing_applied"])
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s creditsSnapshot) print() {
	fmt.Printf("account=%d window=[%s,%s) selected=%d needs_change=%d errors=%d\n", s.AccountID, s.From, s.To, s.Selected, len(s.Changes), len(s.Errors))
	type totals struct {
		Count                     int
		Overcharged, Undercharged float64
	}
	byKey := map[string]*totals{}
	for _, c := range s.Changes {
		for _, key := range []string{"model=" + service.UnifiedOpenAIModel(c.Model), fmt.Sprintf("user=%d", c.UserID)} {
			if byKey[key] == nil {
				byKey[key] = &totals{}
			}
			v := byKey[key]
			v.Count++
			if c.ActualDelta < 0 {
				v.Overcharged -= c.ActualDelta
			} else {
				v.Undercharged += c.ActualDelta
			}
		}
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := byKey[k]
		fmt.Printf("%s rows=%d originally_overcharged=%.10f originally_undercharged=%.10f net_accrual_delta=%.10f\n", k, v.Count, v.Overcharged, v.Undercharged, v.Undercharged-v.Overcharged)
	}
	for _, e := range s.Errors {
		fmt.Println(e)
	}
}

// window 将两次一次性修数固定到各自日期，禁止把恢复价套用到前一天。
func (r *creditsRepair) window() (string, string) {
	if r.astraCacheRestore {
		return "2026-09-08T00:00:00+08:00", "2026-09-09T00:00:00+08:00"
	}
	return creditsFrom, creditsTo
}
