// Package scan 的 today-only 轻量模式：统计窗口内的 token 四维、请求数、模型统计与
// 最近请求（含 llm.request 配对求时长），不构建 series/dimensions/comparison，
// 供托盘"今日用量"与详情窗口"今日"档共用
// （对齐方案 §5.2.3 的 D2a 同口径 + 收紧上限 + mtime 粗筛）。
package scan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"kimi-hud/internal/pricing"
)

const (
	// today-only 收紧上限（对齐方案 §5.2.3：行数 1M→256K、文件 50k→5k，
	// 超限即停 + limits_reached，与全量 Scan 的同名常量区分）。
	maxTodayWireFiles    = 5_000
	maxTodayScannedLines = 256_000
)

// TodayReport 今日用量轻量结果（camelCase 对齐 tracker，M2 窗口"今日"档可直接透传）。
// Dimensions 只填 Models/Providers/ModelProviders 三项（筛选前全量口径，无
// sessions/agents/scopes），供详情窗口筛选器在"今日"档也拥有完整厂家/模型级联选项，
// 与全量档同结构（前端无需分支）。
type TodayReport struct {
	GeneratedAt      int64             `json:"generatedAt"`
	Tokens           TokenTotals       `json:"tokens"`
	Requests         uint64            `json:"requests"`
	PricedRequests   uint64            `json:"pricedRequests"`
	PricedCostCny    string            `json:"pricedCostCny"`
	CacheHitRate     float64           `json:"cacheHitRate"`
	ScannedFiles     uint64            `json:"scannedFiles"`
	ScannedLines     uint64            `json:"scannedLines"`
	MalformedRecords uint64            `json:"malformedRecords"`
	OversizedRecords uint64            `json:"oversizedRecords"`
	LimitsReached    bool              `json:"limitsReached"`
	Models           []ModelUsage      `json:"models"`
	Recent           []RecentUsage     `json:"recent"`
	Dimensions       *FilterDimensions `json:"dimensions"`
}

// ScanToday 轻量扫描 home 内 [startMs, endMs] 窗口的 token 用量。
// 相比 Scan：只累加 totals/模型/最近请求，不构建 series/dimensions/comparison（省内存省 CPU）；
// 文件 mtime 粗筛——wire.jsonl 为追加写，mtime 早于窗口起点则无窗口内写入，整文件跳过。
// filter 非 nil 时按 models/providers 维度筛选（与全量 Scan 同一 resolvedFilter 口径），
// 仅过滤聚合，不回传维度选项（详情窗口选项来自全量档报告）。
// home 语义与 Scan 一致（KIMI_CODE_HOME/HOME/USERPROFILE 定位，详见 resolveHome）。
func ScanToday(home string, startMs, endMs int64, filter *ScanFilter) (*TodayReport, error) {
	home, err := resolveHome(home)
	if err != nil {
		return nil, err
	}
	sessionsRoot := filepath.Join(home, "sessions")
	fi, err := os.Stat(sessionsRoot)
	if err != nil || !fi.IsDir() {
		return nil, invalidHome("No Kimi Code sessions directory was found at " + sessionsRoot)
	}

	var wireFiles []string
	if err := collectTodayWireFiles(sessionsRoot, 0, startMs, &wireFiles); err != nil {
		return nil, err
	}
	sort.Strings(wireFiles)

	rf := resolveFilter(filter)
	st := &todayState{
		models: map[string]*modelAggregate{},
		dims: dimensionCounts{
			models:         map[string]uint64{},
			modelProviders: map[string]string{},
			providers:      map[string]uint64{},
		},
	}
	resolver := newModelResolver(home)
	for _, path := range wireFiles {
		stop, err := scanTodayWireFile(path, startMs, endMs, rf, resolver, st)
		if err != nil {
			return nil, err
		}
		if stop {
			break
		}
	}
	// 无任何已定价记录时置空，区分"零费用"与"未配置价格"。
	// （订阅模型默认无单价，PricedCostCny 为空；用户配置 [pricing] 后此处有值。）
	pricedCostCny := ""
	if st.pricedRequests > 0 {
		pricedCostCny = pricing.FormatCnyNanos(st.costNanos)
	}
	return &TodayReport{
		GeneratedAt:      nowMillis(),
		Tokens:           st.tokens,
		Requests:         st.requests,
		PricedRequests:   st.pricedRequests,
		PricedCostCny:    pricedCostCny,
		CacheHitRate:     cacheHitRate(&st.tokens), // 复用全量 Scan 同一口径（scan.go cacheHitRate）
		ScannedFiles:     uint64(len(wireFiles)),
		ScannedLines:     st.scannedLines,
		MalformedRecords: st.malformed,
		OversizedRecords: st.oversized,
		LimitsReached:    st.limitsReached,
		Models:           buildModelUsages(st.models),
		Recent:           buildRecentUsages(st.recent),
		Dimensions: &FilterDimensions{
			Models:         dimensionValues(st.dims.models),
			Providers:      dimensionValues(st.dims.providers),
			ModelProviders: st.dims.modelProviders,
		},
	}, nil
}

type todayState struct {
	tokens         TokenTotals
	requests       uint64
	costNanos      int64
	pricedRequests uint64
	scannedLines   uint64
	malformed      uint64
	oversized      uint64
	limitsReached  bool
	// dims 轻量维度计数：与全量 Scan 的 dimensionCounts 同口径，但只在筛选前对
	// 厂家/模型计数（今日档不做 sessions/agents/scopes 维度）。
	dims   dimensionCounts
	models map[string]*modelAggregate
	recent []recentUsage
}

// buildRecentUsages 由 recent 内部结构构建排序后的最近请求列表（时间倒序，截断 maxRecentRecords）。
// 与全量 Scan 的 buildReport 最近请求口径完全一致（复用 display/render 结构）。
func buildRecentUsages(recent []recentUsage) []RecentUsage {
	sort.Slice(recent, func(i, j int) bool { return recent[i].time > recent[j].time })
	if len(recent) > maxRecentRecords {
		recent = recent[:maxRecentRecords]
	}
	out := make([]RecentUsage, 0, len(recent))
	for _, r := range recent {
		var costCny *string
		if r.priced {
			c := pricing.FormatCnyNanos(r.costNanos)
			costCny = &c
		}
		out = append(out, RecentUsage{
			Time:         r.time,
			Model:        r.model,
			SessionID:    r.sessionID,
			Agent:        r.agent,
			Scope:        r.scope,
			Tokens:       r.tokens,
			CacheHitRate: r.cacheHitRate,
			DurationMs:   r.durationMs,
			CostCny:      costCny,
		})
	}
	return out
}

// collectTodayWireFiles 收集 mtime >= startMs 的 wire.jsonl（mtime 粗筛）。
func collectTodayWireFiles(directory string, depth int, startMs int64, files *[]string) error {
	if depth > maxScanDepth || len(*files) >= maxTodayWireFiles {
		return nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if len(*files) >= maxTodayWireFiles {
			break
		}
		if entry.IsDir() {
			if err := collectTodayWireFiles(filepath.Join(directory, entry.Name()), depth+1, startMs, files); err != nil {
				return err
			}
		} else if entry.Type().IsRegular() && entry.Name() == "wire.jsonl" {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().UnixMilli() < startMs {
				continue
			}
			*files = append(*files, filepath.Join(directory, entry.Name()))
		}
	}
	return nil
}

// scanTodayWireFile 逐行解析单个 wire.jsonl，累加窗口内 usage.record 的 totals、模型统计
// 与最近请求；llm.request 配对求请求时长（对齐全量 Scan 的 scanWireFile）。
// filter 非 nil 时按 resolvedFilter 维度过滤聚合记录。
func scanTodayWireFile(path string, startMs, endMs int64, filter *resolvedFilter, resolver *modelResolver, st *todayState) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	// sessionIdentity 逐行解析是热路径开销（strings.Split 每次分配），每文件只算一次。
	sessionID, agent := sessionIdentity(path)
	reader := bufio.NewReaderSize(f, 64*1024)
	var line []byte
	var pendingRequestTime *int64

	for {
		oversized, ok, err := readCappedLine(reader, &line, maxUsageRecordBytes)
		if err != nil {
			return false, err
		}
		if !ok {
			break
		}
		if st.scannedLines >= maxTodayScannedLines {
			st.limitsReached = true
			return true, nil
		}
		st.scannedLines = satAdd(st.scannedLines, 1)
		if oversized {
			st.oversized = satAdd(st.oversized, 1)
			if bytes.Contains(line, []byte("llm.request")) {
				pendingRequestTime = nil
			}
			continue
		}
		// llm.request 与 usage.record 互斥（单行单一事件类型）：
		// 分路后 usage.record 行只需一次 JSON 解析（省掉 meta 二次解析的热路径开销）。
		if bytes.Contains(line, []byte("llm.request")) {
			var meta wireEventMetadata
			if err := json.Unmarshal(line, &meta); err != nil {
				pendingRequestTime = nil
			} else if meta.RecordType == "llm.request" {
				if meta.Time != nil && *meta.Time > 0 {
					v := normalizeTimestamp(*meta.Time)
					pendingRequestTime = &v
				} else {
					pendingRequestTime = nil
				}
			}
			continue
		}
		if !bytes.Contains(line, []byte("usage.record")) {
			continue
		}
		var record wireUsageRecord
		if err := json.Unmarshal(line, &record); err != nil {
			pendingRequestTime = nil
			st.malformed = satAdd(st.malformed, 1)
			continue
		}
		if record.Usage.grandTotal() == 0 {
			continue
		}
		var t int64
		if record.Time != nil {
			t = normalizeTimestamp(*record.Time)
		} else {
			t = normalizeTimestamp(0)
		}
		if t < startMs || t > endMs {
			continue
		}
		// 模型统计：与全量 Scan 同口径（resolver 归一化 + pricing 定价），仅聚合不建 series。
		model, alias := resolver.resolve(record.Model, t, path)
		provider := resolver.providerFor(model, alias)
		price := pricing.Find(model)
		// 维度计数在筛选前（对齐全量 Scan 的 consumeMainRecord：选项恒为全量口径）。
		st.dims.addModelProvider(model, provider)
		if filter != nil && !filter.matches(model, provider, sessionID, agent, recordScope(&record), price != nil) {
			continue
		}
		st.tokens.add(&record.Usage)
		st.requests = satAdd(st.requests, 1)

		var costNanos *int64
		if price != nil {
			c := price.CalculateNanos(record.Usage.InputOther, record.Usage.Output,
				record.Usage.InputCacheRead, record.Usage.InputCacheCreation)
			costNanos = &c
			st.costNanos = satAddI64(st.costNanos, c)
			st.pricedRequests = satAdd(st.pricedRequests, 1)
		}
		modelBucket := model
		if _, ok := st.models[model]; !ok && len(st.models) >= maxModelBuckets {
			st.limitsReached = true
			modelBucket = overflowModel
		}
		ma, ok := st.models[modelBucket]
		if !ok {
			ma = &modelAggregate{aliases: map[string]struct{}{}, provider: provider}
			st.models[modelBucket] = ma
		}
		ma.aggregate.add(&record.Usage, costNanos)
		if modelBucket != overflowModel && alias != nil {
			ma.aliases[*alias] = struct{}{}
		}

		// 最近请求：与全量 Scan 一致（时长来自 llm.request 配对，无配对则为 nil）。
		tokens := record.Usage.asTotals()
		costN := int64(0)
		priced := false
		if costNanos != nil {
			costN = *costNanos
			priced = true
		}
		st.recent = appendRecent(st.recent, recentUsage{
			time:         t,
			model:        model,
			sessionID:    sessionID,
			agent:        agent,
			scope:        recordScope(&record),
			cacheHitRate: cacheHitRate(&tokens),
			durationMs:   requestDurationMs(pendingRequestTime, record.Time),
			tokens:       tokens,
			costNanos:    costN,
			priced:       priced,
		})
	}
	return false, nil
}
