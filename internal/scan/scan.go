// Package scan 全量扫描 ~/.kimi-code/sessions/**/wire.jsonl 的 usage.record 事件，
// 生成用量统计报告。移植自 kimi-usage-tracker 的 scanner.rs（2026-08-20，对拍基准见测试）。
package scan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"kimi-hud/internal/pricing"
)

const (
	maxScanDepth         = 7
	maxWireFiles         = 50_000
	maxScannedLines      = 1_000_000
	maxUsageRecordBytes  = 256 * 1024
	maxModelLength       = 160
	maxModelBuckets      = 2_048
	maxSeriesBuckets     = 4_096
	maxDimensionValues   = 2_000
	overflowModel        = "other-models"
	unspecifiedScope     = "未标注"
	maxConfigBytes       = 1024 * 1024
	maxConfigModels      = 2_048
	maxSessionLogBytes   = 8 * 1024 * 1024
	maxSessionLogFiles   = 8
	maxEnvModelSessions  = 2_048
	maxEnvModelEvents    = 100_000
	envModelAlias        = "__kimi_env_model__"
	maxRecentRecords     = 1_000
	maxRequestDurationMs = 24 * 60 * 60 * 1_000
)

// ScanError 扫描错误。
type ScanError struct{ msg string }

func (e *ScanError) Error() string { return e.msg }

func invalidHome(msg string) *ScanError { return &ScanError{msg: msg} }

// TokenTotals token 四维累计（camelCase 对齐 tracker UsageReport）。
type TokenTotals struct {
	InputOther         uint64 `json:"inputOther"`
	Output             uint64 `json:"output"`
	InputCacheRead     uint64 `json:"inputCacheRead"`
	InputCacheCreation uint64 `json:"inputCacheCreation"`
	Total              uint64 `json:"total"`
}

func (t *TokenTotals) add(usage *rawTokenUsage) {
	t.InputOther = satAdd(t.InputOther, usage.InputOther)
	t.Output = satAdd(t.Output, usage.Output)
	t.InputCacheRead = satAdd(t.InputCacheRead, usage.InputCacheRead)
	t.InputCacheCreation = satAdd(t.InputCacheCreation, usage.InputCacheCreation)
	t.Total = satAdd(t.Total, usage.grandTotal())
}

func (t *TokenTotals) merge(other *TokenTotals) {
	t.InputOther = satAdd(t.InputOther, other.InputOther)
	t.Output = satAdd(t.Output, other.Output)
	t.InputCacheRead = satAdd(t.InputCacheRead, other.InputCacheRead)
	t.InputCacheCreation = satAdd(t.InputCacheCreation, other.InputCacheCreation)
	t.Total = satAdd(t.Total, other.Total)
}

func (t *TokenTotals) inputTotal() uint64 {
	return satAdd(satAdd(t.InputOther, t.InputCacheRead), t.InputCacheCreation)
}

// SeriesBucket 时间序列桶。
type SeriesBucket struct {
	StartMs  int64       `json:"startMs"`
	Requests uint64      `json:"requests"`
	Tokens   TokenTotals `json:"tokens"`
	CostCny  string      `json:"costCny"`
}

// DimensionValue 维度值计数。
type DimensionValue struct {
	Value    string `json:"value"`
	Requests uint64 `json:"requests"`
}

// FilterDimensions 可筛选维度。
type FilterDimensions struct {
	Models   []DimensionValue `json:"models"`
	Sessions []DimensionValue `json:"sessions"`
	Agents   []DimensionValue `json:"agents"`
	Scopes   []DimensionValue `json:"scopes"`
	// Providers 厂家（provider）维度，供详情窗口筛选器下拉选项。
	Providers []DimensionValue `json:"providers"`
	// ModelProviders 模型名 -> 厂家映射，供前端按厂家级联过滤模型选项。
	// 同一模型理论上可能经不同别名来自不同厂家，此处取首个出现者（仅展示用）。
	ModelProviders map[string]string `json:"modelProviders"`
}

// ComparisonSummary 对比周期汇总。
type ComparisonSummary struct {
	Requests      uint64      `json:"requests"`
	Sessions      uint64      `json:"sessions"`
	Tokens        TokenTotals `json:"tokens"`
	CacheHitRate  float64     `json:"cacheHitRate"`
	PricedCostCny string      `json:"pricedCostCny"`
}

// CostSegments 四维分项费用（注意字段名与索引顺序不同，见 buildReport）。
type CostSegments struct {
	InputOther    string `json:"inputOther"`
	CacheRead     string `json:"cacheRead"`
	CacheCreation string `json:"cacheCreation"`
	Output        string `json:"output"`
}

// ModelUsage 单模型统计。
type ModelUsage struct {
	Model        string                   `json:"model"`
	Provider     string                   `json:"provider"`
	Aliases      []string                 `json:"aliases"`
	Requests     uint64                   `json:"requests"`
	Tokens       TokenTotals              `json:"tokens"`
	CacheHitRate float64                  `json:"cacheHitRate"`
	CostCny      *string                  `json:"costCny"`
	Pricing      *pricing.PricingMetadata `json:"pricing"`
}

// RecentUsage 最近请求记录。
type RecentUsage struct {
	Time         int64       `json:"time"`
	Model        string      `json:"model"`
	SessionID    string      `json:"sessionId"`
	Agent        string      `json:"agent"`
	Scope        string      `json:"scope"`
	Tokens       TokenTotals `json:"tokens"`
	CacheHitRate float64     `json:"cacheHitRate"`
	DurationMs   *uint64     `json:"durationMs"`
	CostCny      *string     `json:"costCny"`
}

// UsageReport 全量统计报告。
type UsageReport struct {
	Home             string             `json:"home"`
	GeneratedAt      int64              `json:"generatedAt"`
	Granularity      string             `json:"granularity"`
	Requests         uint64             `json:"requests"`
	Sessions         uint64             `json:"sessions"`
	ScannedFiles     uint64             `json:"scannedFiles"`
	ScannedLines     uint64             `json:"scannedLines"`
	ParsedRecords    uint64             `json:"parsedRecords"`
	MalformedRecords uint64             `json:"malformedRecords"`
	OversizedRecords uint64             `json:"oversizedRecords"`
	LimitsReached    bool               `json:"limitsReached"`
	UnpricedRequests uint64             `json:"unpricedRequests"`
	CacheHitRate     float64            `json:"cacheHitRate"`
	PricedCostCny    string             `json:"pricedCostCny"`
	Tokens           TokenTotals        `json:"tokens"`
	CostSegments     CostSegments       `json:"costSegments"`
	Series           []SeriesBucket     `json:"series"`
	Dimensions       FilterDimensions   `json:"dimensions"`
	Comparison       *ComparisonSummary `json:"comparison"`
	Models           []ModelUsage       `json:"models"`
	Recent           []RecentUsage      `json:"recent"`
}

// ScanFilter 扫描过滤条件（camelCase 对齐 tracker ScanFilter，前端直接透传）。
type ScanFilter struct {
	StartMs     *int64   `json:"startMs"`
	EndMs       *int64   `json:"endMs"`
	Models      []string `json:"models"`
	Providers   []string `json:"providers"`
	Sessions    []string `json:"sessions"`
	Agents      []string `json:"agents"`
	Scopes      []string `json:"scopes"`
	Billing     string   `json:"billing"`
	Granularity string   `json:"granularity"`
}

// ---- wire 事件解析 ----

type wireEventMetadata struct {
	RecordType string `json:"type"`
	Time       *int64 `json:"time"`
}

type wireUsageRecord struct {
	RecordType string        `json:"type"`
	Model      string        `json:"model"`
	Time       *int64        `json:"time"`
	Usage      rawTokenUsage `json:"usage"`
	UsageScope *string       `json:"usageScope"`
}

type rawTokenUsage struct {
	InputOther         uint64 `json:"inputOther"`
	Output             uint64 `json:"output"`
	InputCacheRead     uint64 `json:"inputCacheRead"`
	InputCacheCreation uint64 `json:"inputCacheCreation"`
}

func (u *rawTokenUsage) grandTotal() uint64 {
	return satAdd(satAdd(satAdd(u.InputOther, u.Output), u.InputCacheRead), u.InputCacheCreation)
}

func (u *rawTokenUsage) asTotals() TokenTotals {
	var t TokenTotals
	t.add(u)
	return t
}

// ---- 粒度 ----

type granularity int

const (
	granMinute granularity = iota
	granHour
	granDay
	granWeek
	granMonth
)

func parseGranularity(value string) (granularity, bool) {
	switch value {
	case "minute":
		return granMinute, true
	case "hour":
		return granHour, true
	case "day":
		return granDay, true
	case "week":
		return granWeek, true
	case "month":
		return granMonth, true
	}
	return granMinute, false
}

func (g granularity) str() string {
	switch g {
	case granMinute:
		return "minute"
	case granHour:
		return "hour"
	case granDay:
		return "day"
	case granWeek:
		return "week"
	case granMonth:
		return "month"
	}
	return "minute"
}

func (g granularity) coarser() (granularity, bool) {
	switch g {
	case granMinute:
		return granHour, true
	case granHour:
		return granDay, true
	case granDay:
		return granWeek, true
	case granWeek:
		return granMonth, true
	case granMonth:
		return granMonth, false
	}
	return granMinute, false
}

// ---- 过滤 ----

type billingFilter int

const (
	billingAll billingFilter = iota
	billingPriced
	billingUnpriced
)

func parseBilling(value string) billingFilter {
	switch value {
	case "priced":
		return billingPriced
	case "unpriced":
		return billingUnpriced
	}
	return billingAll
}

func (b billingFilter) matches(priced bool) bool {
	switch b {
	case billingAll:
		return true
	case billingPriced:
		return priced
	case billingUnpriced:
		return !priced
	}
	return true
}

type resolvedFilter struct {
	startMs     *int64
	endMs       *int64
	models      []string
	providers   []string
	sessions    []string
	agents      []string
	scopes      []string
	billing     billingFilter
	granularity *granularity
}

func resolveFilter(filter *ScanFilter) *resolvedFilter {
	if filter == nil {
		filter = &ScanFilter{}
	}
	g, ok := parseGranularity(filter.Granularity)
	var gp *granularity
	if ok {
		gp = &g
	}
	return &resolvedFilter{
		startMs:     filter.StartMs,
		endMs:       filter.EndMs,
		models:      filter.Models,
		providers:   filter.Providers,
		sessions:    filter.Sessions,
		agents:      filter.Agents,
		scopes:      filter.Scopes,
		billing:     parseBilling(filter.Billing),
		granularity: gp,
	}
}

func (f *resolvedFilter) matches(model, provider, session, agent, scope string, priced bool) bool {
	return (len(f.models) == 0 || containsStr(f.models, model)) &&
		(len(f.providers) == 0 || containsStr(f.providers, provider)) &&
		(len(f.sessions) == 0 || containsStr(f.sessions, session)) &&
		(len(f.agents) == 0 || containsStr(f.agents, agent)) &&
		(len(f.scopes) == 0 || containsStr(f.scopes, scope)) &&
		f.billing.matches(priced)
}

func (f *resolvedFilter) comparisonWindow() (int64, int64, bool) {
	if f.startMs == nil || f.endMs == nil {
		return 0, 0, false
	}
	start, end := *f.startMs, *f.endMs
	if end < start {
		return 0, 0, false
	}
	length := end - start + 1
	return start - length, start - 1, true
}

type recordWindow int

const (
	winMain recordWindow = iota
	winComparison
	winSkip
)

func classifyWindow(time int64, f *resolvedFilter) recordWindow {
	inMain := (f.startMs == nil || time >= *f.startMs) && (f.endMs == nil || time <= *f.endMs)
	if inMain {
		return winMain
	}
	if cs, ce, ok := f.comparisonWindow(); ok && time >= cs && time <= ce {
		return winComparison
	}
	return winSkip
}

// ---- 聚合 ----

type aggregate struct {
	requests       uint64
	totals         TokenTotals
	costNanos      int64
	pricedRequests uint64
}

func (a *aggregate) add(usage *rawTokenUsage, costNanos *int64) {
	a.requests = satAdd(a.requests, 1)
	a.totals.add(usage)
	if costNanos != nil {
		a.costNanos = satAddI64(a.costNanos, *costNanos)
		a.pricedRequests = satAdd(a.pricedRequests, 1)
	}
}

func (a *aggregate) merge(other *aggregate) {
	a.requests = satAdd(a.requests, other.requests)
	a.totals.merge(&other.totals)
	a.costNanos = satAddI64(a.costNanos, other.costNanos)
	a.pricedRequests = satAdd(a.pricedRequests, other.pricedRequests)
}

type modelAggregate struct {
	aggregate aggregate
	aliases   map[string]struct{}
	provider  string
}

// ---- config.toml 模型别名解析（对齐 modelcfg 的手写解析风格，零 TOML 依赖）----

var (
	configModelTableRe    = regexp.MustCompile(`^\[\s*models\s*\.\s*"([^"]+)"\s*\]`)
	configModelFieldRe    = regexp.MustCompile(`^\s*model\s*=\s*"([^"]*)"`)
	configProviderFieldRe = regexp.MustCompile(`^\s*provider\s*=\s*"([^"]*)"`)
)

// configModelEntry config.toml 单个 [models."alias"] 表的条目（保序，见 readBoundedConfig）。
type configModelEntry struct {
	alias    string
	model    string
	provider string
}

// readBoundedConfig 读取 config.toml 的 [models."alias"] 表，返回保序条目
// （alias / 真实模型名 / provider）。与 tracker 的 toml::from_str 对齐：只关心
// models.<alias> 的 model/provider 字段；保序用于确定性构建 model→provider 反向索引。
func readBoundedConfig(path string) []configModelEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	// 上限 MAX_CONFIG_BYTES，多读 1 字节用于检测超限。
	bytes_ := make([]byte, 0, maxConfigBytes+1)
	buf := make([]byte, 64*1024)
	total := 0
	for {
		n, rerr := f.Read(buf)
		total += n
		if n > 0 {
			if len(bytes_)+n > maxConfigBytes {
				return nil
			}
			bytes_ = append(bytes_, buf[:n]...)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil
		}
	}
	var entries []configModelEntry
	idx := map[string]int{}
	curAlias := ""
	for _, rawLine := range strings.Split(string(bytes_), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if m := configModelTableRe.FindStringSubmatch(line); m != nil {
				curAlias = m[1]
				if _, ok := idx[curAlias]; !ok {
					idx[curAlias] = len(entries)
					entries = append(entries, configModelEntry{alias: curAlias})
				}
			} else {
				curAlias = ""
			}
			continue
		}
		if curAlias == "" {
			continue
		}
		i := idx[curAlias]
		if m := configModelFieldRe.FindStringSubmatch(line); m != nil && m[1] != "" {
			entries[i].model = m[1]
		} else if m := configProviderFieldRe.FindStringSubmatch(line); m != nil {
			entries[i].provider = m[1]
		}
	}
	return entries
}

// ---- 环境模型（__kimi_env_model__）解析 ----

type modelAliasEvent struct {
	time  int64
	model string
}

type modelResolver struct {
	aliases           map[string]string
	modelProvider     map[string]string
	aliasProvider     map[string]string
	sessionEvents     map[string][]modelAliasEvent
	loadedSessions    map[string]struct{}
	remainingCapacity int
}

func newModelResolver(home string) *modelResolver {
	r := &modelResolver{
		aliases:           map[string]string{},
		modelProvider:     map[string]string{},
		aliasProvider:     map[string]string{},
		sessionEvents:     map[string][]modelAliasEvent{},
		loadedSessions:    map[string]struct{}{},
		remainingCapacity: maxEnvModelEvents,
	}
	config := readBoundedConfig(filepath.Join(home, "config.toml"))
	for _, entry := range config {
		if len(r.aliases) >= maxConfigModels {
			break
		}
		a := sanitizeModel(entry.alias)
		m := sanitizeModel(entry.model)
		if a != "unknown" && m != "unknown" {
			r.aliases[a] = m
			if p := sanitizeProvider(entry.provider); p != "" {
				r.aliasProvider[a] = p
				if _, ok := r.modelProvider[m]; !ok {
					r.modelProvider[m] = p
				}
			}
		}
	}
	return r
}

// sanitizeProvider 清洗 config.toml 的 provider 值（空值返回 ""，调用方视为未配置）。
func sanitizeProvider(provider string) string {
	value := strings.TrimSpace(provider)
	if len(value) > maxModelLength {
		value = value[:maxModelLength]
		for len(value) > 0 && !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

// providerFor 解析一条用量记录的厂家（provider）：优先按 alias 命中 config.toml
// 的 provider 字段，其次按解析后的模型名反查（config model 字段反向索引），再退化为
// 原始模型名 "provider/model" 的前缀启发式（config 已删除的历史别名仍可按前缀分组，
// 与 config alias 的 "provider/model" 命名惯例一致），最后归入 unspecifiedScope。
func (r *modelResolver) providerFor(resolvedModel string, alias *string) string {
	if alias != nil {
		if p, ok := r.aliasProvider[*alias]; ok {
			return p
		}
	}
	if p, ok := r.modelProvider[resolvedModel]; ok {
		return p
	}
	original := resolvedModel
	if alias != nil {
		original = *alias
	}
	if i := strings.IndexByte(original, '/'); i > 0 {
		return sanitizeProvider(original[:i])
	}
	return unspecifiedScope
}

func (r *modelResolver) resolve(recordedModel string, time int64, wirePath string) (string, *string) {
	original := sanitizeModel(recordedModel)
	configured := r.aliases[original]
	if configured == "" && original == envModelAlias {
		if m, ok := r.resolveSessionModel(wirePath, time); ok {
			configured = m
		}
	}
	target := configured
	if target == "" {
		target = original
	}
	resolved, ok := pricing.CanonicalModel(target)
	if !ok {
		resolved = target
	}
	var alias *string
	if original != resolved {
		a := original
		alias = &a
	}
	return resolved, alias
}

func (r *modelResolver) resolveSessionModel(wirePath string, time int64) (string, bool) {
	sessionRoot, ok := sessionRootForWire(wirePath)
	if !ok {
		return "", false
	}
	if _, loaded := r.loadedSessions[sessionRoot]; !loaded {
		if len(r.loadedSessions) >= maxEnvModelSessions {
			return "", false
		}
		events := loadSessionModelEvents(sessionRoot, r.remainingCapacity)
		r.remainingCapacity -= len(events)
		if r.remainingCapacity < 0 {
			r.remainingCapacity = 0
		}
		r.loadedSessions[sessionRoot] = struct{}{}
		r.sessionEvents[sessionRoot] = events
	}
	events := r.sessionEvents[sessionRoot]
	if len(events) == 0 {
		return "", false
	}
	// partition_point(|e| e.time <= time)：第一个 time > time 的下标。
	idx := sort.Search(len(events), func(i int) bool { return events[i].time > time })
	if idx == 0 {
		return events[0].model, true
	}
	return events[idx-1].model, true
}

func sessionRootForWire(path string) (string, bool) {
	dir := filepath.Dir(path)   // .../agents/<agent>
	agents := filepath.Dir(dir) // .../sessions/<session>/agents
	if filepath.Base(agents) != "agents" {
		return "", false
	}
	return filepath.Dir(agents), true
}

func loadSessionModelEvents(sessionRoot string, limit int) []modelAliasEvent {
	if limit <= 0 {
		return nil
	}
	logs := filepath.Join(sessionRoot, "logs")
	entries, err := os.ReadDir(logs)
	if err != nil {
		return nil
	}
	var paths []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "kimi-code.log") {
			paths = append(paths, filepath.Join(logs, entry.Name()))
		}
	}
	sort.Strings(paths)
	if len(paths) > maxSessionLogFiles {
		paths = paths[:maxSessionLogFiles]
	}
	var events []modelAliasEvent
	for _, path := range paths {
		if len(events) >= limit {
			break
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		reader := bufio.NewReaderSize(io.LimitReader(f, maxSessionLogBytes), 32*1024)
		var line []byte
		for len(events) < limit {
			oversized, ok, lerr := readCappedLine(reader, &line, maxUsageRecordBytes)
			if lerr != nil || !ok {
				break
			}
			if oversized || !bytes.Contains(line, []byte(envModelAlias)) {
				continue
			}
			if event, ok := parseSessionModelEvent(line); ok {
				events = append(events, event)
			}
		}
		f.Close()
	}
	sort.Slice(events, func(i, j int) bool { return events[i].time < events[j].time })
	return events
}

func parseSessionModelEvent(line []byte) (modelAliasEvent, bool) {
	fields := strings.Fields(string(line))
	if len(fields) == 0 {
		return modelAliasEvent{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, fields[0])
	if err != nil {
		return modelAliasEvent{}, false
	}
	var model string
	isEnvAlias := false
	for _, field := range fields[1:] {
		if v, ok := strings.CutPrefix(field, "model="); ok {
			model = sanitizeModel(v)
		} else if v, ok := strings.CutPrefix(field, "modelAlias="); ok {
			isEnvAlias = v == envModelAlias
		}
	}
	if model == "" || !isEnvAlias || model == "unknown" {
		return modelAliasEvent{}, false
	}
	return modelAliasEvent{time: t.UnixMilli(), model: model}, true
}

// ---- 维度计数 ----

type dimensionCounts struct {
	models         map[string]uint64
	modelProviders map[string]string
	providers      map[string]uint64
	sessions       map[string]uint64
	agents         map[string]uint64
	scopes         map[string]uint64
}

func (d *dimensionCounts) add(model, provider, session, agent, scope string) bool {
	limited := false
	limited = bumpDimension(d.models, model) || limited
	// modelProviders 与 models 同键同步：仅记录成功进入 models 的模型（超限丢弃者不记）。
	if _, ok := d.models[model]; ok {
		if _, ok := d.modelProviders[model]; !ok {
			d.modelProviders[model] = provider
		}
	}
	limited = bumpDimension(d.providers, provider) || limited
	limited = bumpDimension(d.sessions, session) || limited
	limited = bumpDimension(d.agents, agent) || limited
	limited = bumpDimension(d.scopes, scope) || limited
	return limited
}

// addModelProvider 今日档专用轻量计数：只统计模型/厂家两个维度（sessions/agents/scopes
// 三张 map 在 todayState 中不初始化，写它们会 panic；跳过三次空 map 操作也是热路径收益）。
func (d *dimensionCounts) addModelProvider(model, provider string) {
	bumpDimension(d.models, model)
	if _, ok := d.models[model]; ok {
		if _, ok := d.modelProviders[model]; !ok {
			d.modelProviders[model] = provider
		}
	}
	bumpDimension(d.providers, provider)
}

func bumpDimension(m map[string]uint64, value string) bool {
	if _, ok := m[value]; ok {
		m[value]++
		return false
	}
	if len(m) < maxDimensionValues {
		m[value] = 1
		return false
	}
	return true
}

// ---- 状态机 ----

type comparisonState struct {
	aggregate aggregate
	sessions  map[string]struct{}
}

type scanState struct {
	aggregate         aggregate
	costSegments      [4]int64
	series            map[int64]*aggregate
	seriesGranularity granularity
	autoGranularity   bool
	minTime           *int64
	maxTime           *int64
	models            map[string]*modelAggregate
	recent            []recentUsage
	sessions          map[string]struct{}
	dimensions        dimensionCounts
	comparison        comparisonState
	scannedLines      uint64
	parsedRecords     uint64
	malformedRecords  uint64
	oversizedRecords  uint64
	limitsReached     bool
}

func newScanState(g granularity, auto bool) *scanState {
	return &scanState{
		series:            map[int64]*aggregate{},
		seriesGranularity: g,
		autoGranularity:   auto,
		models:            map[string]*modelAggregate{},
		sessions:          map[string]struct{}{},
		dimensions: dimensionCounts{
			models:         map[string]uint64{},
			modelProviders: map[string]string{},
			providers:      map[string]uint64{},
			sessions:       map[string]uint64{},
			agents:         map[string]uint64{},
			scopes:         map[string]uint64{},
		},
		comparison: comparisonState{sessions: map[string]struct{}{}},
	}
}

// Scan 全量扫描 Kimi 数据目录（home 为 ~/.kimi-code；空则按 KIMI_CODE_HOME/HOME/USERPROFILE 定位）。
func Scan(home string, filter *ScanFilter) (*UsageReport, error) {
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
	if err := collectWireFiles(sessionsRoot, 0, &wireFiles); err != nil {
		return nil, err
	}
	sort.Strings(wireFiles)

	now := nowMillis()
	rf := resolveFilter(filter)
	initialGran, auto := granMinute, false
	if rf.granularity != nil {
		initialGran = *rf.granularity
	} else {
		if rf.startMs != nil && rf.endMs != nil && *rf.endMs >= *rf.startMs {
			initialGran = autoGranularity(*rf.endMs - *rf.startMs)
			auto = true
		} else {
			initialGran, auto = granMinute, true
		}
	}
	state := newScanState(initialGran, auto)
	resolver := newModelResolver(home)

	for _, path := range wireFiles {
		stop, err := scanWireFile(path, rf, resolver, state)
		if err != nil {
			return nil, err
		}
		if stop {
			break
		}
	}

	return buildReport(home, now, rf, len(wireFiles), state), nil
}

// DefaultKimiHome 返回默认 Kimi 数据目录。
func DefaultKimiHome() (string, bool) {
	if v := os.Getenv("KIMI_CODE_HOME"); v != "" {
		return v, true
	}
	var homes []string
	if v := os.Getenv("HOME"); v != "" {
		homes = append(homes, v)
	}
	if v := os.Getenv("USERPROFILE"); v != "" {
		homes = append(homes, v)
	}
	return selectDefaultKimiHome(homes)
}

func selectDefaultKimiHome(homes []string) (string, bool) {
	seen := map[string]struct{}{}
	first := ""
	existing := ""
	for _, home := range homes {
		candidate := filepath.Join(home, ".kimi-code")
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if first == "" {
			first = candidate
		}
		if isDir(filepath.Join(candidate, "sessions")) {
			return candidate, true
		}
		if existing == "" && isDir(candidate) {
			existing = candidate
		}
	}
	if existing != "" {
		return existing, true
	}
	if first != "" {
		return first, true
	}
	return "", false
}

func resolveHome(home string) (string, error) {
	path := strings.TrimSpace(home)
	if path == "" {
		var ok bool
		path, ok = DefaultKimiHome()
		if !ok {
			return "", invalidHome("Unable to locate the current user home directory")
		}
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return "", invalidHome("Kimi Code data directory does not exist: " + path)
	}
	return path, nil
}

func collectWireFiles(directory string, depth int, files *[]string) error {
	if depth > maxScanDepth || len(*files) >= maxWireFiles {
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
		if len(*files) >= maxWireFiles {
			break
		}
		if entry.IsDir() {
			if err := collectWireFiles(filepath.Join(directory, entry.Name()), depth+1, files); err != nil {
				return err
			}
		} else if entry.Type().IsRegular() && entry.Name() == "wire.jsonl" {
			*files = append(*files, filepath.Join(directory, entry.Name()))
		}
	}
	return nil
}

func scanWireFile(path string, filter *resolvedFilter, resolver *modelResolver, state *scanState) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
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
		if state.scannedLines >= maxScannedLines {
			state.limitsReached = true
			return true, nil
		}
		state.scannedLines = satAdd(state.scannedLines, 1)
		if oversized {
			state.oversizedRecords = satAdd(state.oversizedRecords, 1)
			if bytes.Contains(line, []byte("llm.request")) {
				pendingRequestTime = nil
			}
			continue
		}
		// llm.request 与 usage.record 互斥（单行单一事件类型）：
		// 分路后 usage.record 行只需一次 JSON 解析（省掉 meta 二次解析的热路径开销，
		// 与 ScanToday 同款优化，保持两路径口径一致）。
		if bytes.Contains(line, []byte("llm.request")) {
			var metadata wireEventMetadata
			if err := json.Unmarshal(line, &metadata); err != nil {
				pendingRequestTime = nil
			} else if metadata.RecordType == "llm.request" {
				if metadata.Time != nil && *metadata.Time > 0 {
					v := normalizeTimestamp(*metadata.Time)
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
			state.malformedRecords = satAdd(state.malformedRecords, 1)
			continue
		}
		if record.RecordType != "usage.record" || record.Usage.grandTotal() == 0 {
			continue
		}
		durationMs := requestDurationMs(pendingRequestTime, record.Time)
		pendingRequestTime = nil
		var t int64
		if record.Time != nil {
			t = normalizeTimestamp(*record.Time)
		} else {
			t = normalizeTimestamp(0)
		}
		switch classifyWindow(t, filter) {
		case winMain:
			consumeMainRecord(path, record, t, durationMs, filter, resolver, state)
		case winComparison:
			consumeComparisonRecord(path, record, t, filter, resolver, state)
		}
	}
	return false, nil
}

func recordScope(record *wireUsageRecord) string {
	if record.UsageScope != nil {
		s := strings.TrimSpace(*record.UsageScope)
		if s != "" {
			return s
		}
	}
	return unspecifiedScope
}

type recentUsage struct {
	time         int64
	model        string
	sessionID    string
	agent        string
	scope        string
	tokens       TokenTotals
	cacheHitRate float64
	durationMs   *uint64
	// 存原始费用 nanos + 是否有价，格式化延后到 buildRecentUsages（热路径不构建字符串）。
	costNanos int64
	priced    bool
}

func consumeMainRecord(path string, record wireUsageRecord, t int64, durationMs *uint64,
	filter *resolvedFilter, resolver *modelResolver, state *scanState) {
	model, alias := resolver.resolve(record.Model, t, path)
	provider := resolver.providerFor(model, alias)
	price := pricing.Find(model)
	sessionID, agent := sessionIdentity(path)
	scope := recordScope(&record)

	if state.dimensions.add(model, provider, sessionID, agent, scope) {
		state.limitsReached = true
	}
	if !filter.matches(model, provider, sessionID, agent, scope, price != nil) {
		return
	}

	var costNanos *int64
	if price != nil {
		c := price.CalculateNanos(record.Usage.InputOther, record.Usage.Output,
			record.Usage.InputCacheRead, record.Usage.InputCacheCreation)
		costNanos = &c
		segments := price.CalculateSegmentsNanos(record.Usage.InputOther, record.Usage.Output,
			record.Usage.InputCacheRead, record.Usage.InputCacheCreation)
		for i, segment := range segments {
			state.costSegments[i] = satAddI64(state.costSegments[i], segment)
		}
	}

	state.aggregate.add(&record.Usage, costNanos)
	if state.minTime == nil || t < *state.minTime {
		state.minTime = &t
	}
	if state.maxTime == nil || t > *state.maxTime {
		state.maxTime = &t
	}
	insertSeriesBucket(state, t, &record.Usage, costNanos)

	modelBucket := model
	if _, ok := state.models[model]; !ok && len(state.models) >= maxModelBuckets {
		state.limitsReached = true
		modelBucket = overflowModel
	}
	ma, ok := state.models[modelBucket]
	if !ok {
		ma = &modelAggregate{aliases: map[string]struct{}{}, provider: provider}
		state.models[modelBucket] = ma
	}
	ma.aggregate.add(&record.Usage, costNanos)
	if modelBucket != overflowModel && alias != nil {
		ma.aliases[*alias] = struct{}{}
	}
	state.sessions[sessionID] = struct{}{}
	state.parsedRecords = satAdd(state.parsedRecords, 1)

	tokens := record.Usage.asTotals()
	costN := int64(0)
	priced := false
	if costNanos != nil {
		costN = *costNanos
		priced = true
	}
	state.recent = appendRecent(state.recent, recentUsage{
		time:         t,
		model:        model,
		sessionID:    sessionID,
		agent:        agent,
		scope:        scope,
		cacheHitRate: cacheHitRate(&tokens),
		durationMs:   durationMs,
		tokens:       tokens,
		costNanos:    costN,
		priced:       priced,
	})
}

// buildModelUsages 由 models map 构建排序后的模型统计列表（token 降序，同名按模型名）。
// 全量 Scan 与今日轻量 ScanToday 共用（复用优先），保证两处口径/排序完全一致。
func buildModelUsages(models map[string]*modelAggregate) []ModelUsage {
	out := make([]ModelUsage, 0, len(models))
	for model, ma := range models {
		agg := ma.aggregate
		fullyPriced := agg.requests == agg.pricedRequests
		var costCny *string
		if fullyPriced {
			c := pricing.FormatCnyNanos(agg.costNanos)
			costCny = &c
		}
		aliases := make([]string, 0, len(ma.aliases))
		for a := range ma.aliases {
			aliases = append(aliases, a)
		}
		sort.Strings(aliases)
		out = append(out, ModelUsage{
			Pricing:      pricing.Metadata(model),
			Model:        model,
			Provider:     ma.provider,
			Aliases:      aliases,
			Requests:     agg.requests,
			Tokens:       agg.totals,
			CacheHitRate: cacheHitRate(&agg.totals),
			CostCny:      costCny,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tokens.Total != out[j].Tokens.Total {
			return out[i].Tokens.Total > out[j].Tokens.Total
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// appendRecent 追加一条最近请求到缓冲。超过 maxRecentRecords 的 2 倍才排序+裁剪一次，
// 保持"缓冲恒为当前最新 maxRecentRecords 条"的不变量（见 buildRecentUsages 最终兜底排序）。
// 逐条 sort 是热路径灾难（超过上限后每追加一行都 O(n log n)），分批排序把成本摊薄到
// 每 ~1000 行一次，内存有界（≤2×maxRecentRecords）。全量 Scan 与今日轻量共用。
func appendRecent(buf []recentUsage, r recentUsage) []recentUsage {
	buf = append(buf, r)
	if len(buf) > maxRecentRecords*2 {
		sort.Slice(buf, func(i, j int) bool { return buf[i].time > buf[j].time })
		buf = buf[:maxRecentRecords]
	}
	return buf
}

// consumeComparisonRecord 对比周期记录（不含模型聚合）。
func consumeComparisonRecord(path string, record wireUsageRecord, t int64,
	filter *resolvedFilter, resolver *modelResolver, state *scanState) {
	model, alias := resolver.resolve(record.Model, t, path)
	provider := resolver.providerFor(model, alias)
	price := pricing.Find(model)
	sessionID, agent := sessionIdentity(path)
	scope := recordScope(&record)
	if !filter.matches(model, provider, sessionID, agent, scope, price != nil) {
		return
	}
	var costNanos *int64
	if price != nil {
		c := price.CalculateNanos(record.Usage.InputOther, record.Usage.Output,
			record.Usage.InputCacheRead, record.Usage.InputCacheCreation)
		costNanos = &c
	}
	state.comparison.aggregate.add(&record.Usage, costNanos)
	state.comparison.sessions[sessionID] = struct{}{}
}

func insertSeriesBucket(state *scanState, t int64, usage *rawTokenUsage, costNanos *int64) {
	key := bucketStart(t, state.seriesGranularity)
	agg, ok := state.series[key]
	if !ok {
		agg = &aggregate{}
		state.series[key] = agg
	}
	agg.add(usage, costNanos)
	for len(state.series) > maxSeriesBuckets {
		coarser, ok := state.seriesGranularity.coarser()
		if !ok {
			state.limitsReached = true
			break
		}
		rekeySeries(state.series, coarser)
		state.seriesGranularity = coarser
	}
}

func rekeySeries(series map[int64]*aggregate, g granularity) {
	prev := make(map[int64]*aggregate, len(series))
	for k, v := range series {
		prev[k] = v
	}
	for k := range series {
		delete(series, k)
	}
	for key, agg := range prev {
		nk := bucketStart(key, g)
		if existing, ok := series[nk]; ok {
			existing.merge(agg)
		} else {
			series[nk] = agg
		}
	}
}

func buildReport(home string, now int64, filter *resolvedFilter, scannedFiles int, state *scanState) *UsageReport {
	sort.Slice(state.recent, func(i, j int) bool { return state.recent[i].time > state.recent[j].time })

	if state.autoGranularity {
		start := filter.startMs
		if start == nil {
			start = state.minTime
		}
		end := filter.endMs
		if end == nil {
			end = state.maxTime
		}
		if start != nil && end != nil {
			desired := autoGranularity(*end - *start)
			for state.seriesGranularity < desired {
				coarser, ok := state.seriesGranularity.coarser()
				if !ok {
					break
				}
				rekeySeries(state.series, coarser)
				state.seriesGranularity = coarser
			}
		}
	}

	keys := sortedSeriesKeys(state.series)
	series := make([]SeriesBucket, 0, len(keys))
	for _, k := range keys {
		agg := state.series[k]
		series = append(series, SeriesBucket{
			StartMs:  k,
			Requests: agg.requests,
			Tokens:   agg.totals,
			CostCny:  pricing.FormatCnyNanos(agg.costNanos),
		})
	}

	dimensions := FilterDimensions{
		Models:         dimensionValues(state.dimensions.models),
		Sessions:       dimensionValues(state.dimensions.sessions),
		Agents:         dimensionValues(state.dimensions.agents),
		Scopes:         dimensionValues(state.dimensions.scopes),
		Providers:      dimensionValues(state.dimensions.providers),
		ModelProviders: state.dimensions.modelProviders,
	}

	var comparison *ComparisonSummary
	if _, _, ok := filter.comparisonWindow(); ok {
		agg := state.comparison.aggregate
		comparison = &ComparisonSummary{
			Requests:      agg.requests,
			Sessions:      uint64(len(state.comparison.sessions)),
			CacheHitRate:  cacheHitRate(&agg.totals),
			PricedCostCny: pricing.FormatCnyNanos(agg.costNanos),
			Tokens:        agg.totals,
		}
	}

	costSegments := CostSegments{
		InputOther:    pricing.FormatCnyNanos(state.costSegments[0]),
		CacheRead:     pricing.FormatCnyNanos(state.costSegments[2]),
		CacheCreation: pricing.FormatCnyNanos(state.costSegments[3]),
		Output:        pricing.FormatCnyNanos(state.costSegments[1]),
	}

	models := buildModelUsages(state.models)

	recent := buildRecentUsages(state.recent)

	return &UsageReport{
		Home:             home,
		GeneratedAt:      now,
		Granularity:      state.seriesGranularity.str(),
		Requests:         state.aggregate.requests,
		Sessions:         uint64(len(state.sessions)),
		ScannedFiles:     uint64(scannedFiles),
		ScannedLines:     state.scannedLines,
		ParsedRecords:    state.parsedRecords,
		MalformedRecords: state.malformedRecords,
		OversizedRecords: state.oversizedRecords,
		LimitsReached:    state.limitsReached,
		UnpricedRequests: state.aggregate.requests - state.aggregate.pricedRequests,
		CacheHitRate:     cacheHitRate(&state.aggregate.totals),
		PricedCostCny:    pricing.FormatCnyNanos(state.aggregate.costNanos),
		Tokens:           state.aggregate.totals,
		CostSegments:     costSegments,
		Series:           series,
		Dimensions:       dimensions,
		Comparison:       comparison,
		Models:           models,
		Recent:           recent,
	}
}

func dimensionValues(m map[string]uint64) []DimensionValue {
	values := make([]DimensionValue, 0, len(m))
	for value, requests := range m {
		values = append(values, DimensionValue{Value: value, Requests: requests})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Requests != values[j].Requests {
			return values[i].Requests > values[j].Requests
		}
		return values[i].Value < values[j].Value
	})
	return values
}

func sortedSeriesKeys(series map[int64]*aggregate) []int64 {
	keys := make([]int64, 0, len(series))
	for k := range series {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func cacheHitRate(t *TokenTotals) float64 {
	input := t.inputTotal()
	if input == 0 {
		return 0.0
	}
	return float64(t.InputCacheRead) / float64(input)
}

func requestDurationMs(start *int64, end *int64) *uint64 {
	if start == nil {
		return nil
	}
	if end == nil || *end <= 0 {
		return nil
	}
	endN := normalizeTimestamp(*end)
	duration := endN - *start
	if duration < 0 || duration > maxRequestDurationMs {
		return nil
	}
	d := uint64(duration)
	return &d
}

// readCappedLine 读取一行（不含换行）到 output，行超 max 字节时截断并标记 oversized。
// 返回 (oversized, 是否有行数据, error)；ok=false 表示 EOF 且无数据。
func readCappedLine(r *bufio.Reader, output *[]byte, max int) (bool, bool, error) {
	*output = (*output)[:0]
	oversized := false
	sawBytes := false
	for {
		chunk, rerr := r.ReadSlice('\n')
		hasNL := len(chunk) > 0 && chunk[len(chunk)-1] == '\n'
		if rerr == bufio.ErrBufferFull {
			sawBytes = true
			if !oversized {
				content := chunk
				remaining := max - len(*output)
				if len(content) > remaining {
					*output = append(*output, content[:remaining]...)
					oversized = true
				} else {
					*output = append(*output, content...)
				}
			}
			continue
		}
		if rerr == io.EOF {
			if len(chunk) > 0 {
				sawBytes = true
				if !oversized {
					content := chunk
					remaining := max - len(*output)
					if len(content) > remaining {
						*output = append(*output, content[:remaining]...)
						oversized = true
					} else {
						*output = append(*output, content...)
					}
				}
			}
			if sawBytes {
				return oversized, true, nil
			}
			return false, false, nil
		}
		if rerr != nil {
			return false, false, rerr
		}
		sawBytes = true
		content := chunk
		if hasNL {
			content = chunk[:len(chunk)-1]
		}
		if !oversized {
			remaining := max - len(*output)
			if len(content) > remaining {
				*output = append(*output, content[:remaining]...)
				oversized = true
			} else {
				*output = append(*output, content...)
			}
		}
		return oversized, true, nil
	}
}

func sanitizeModel(model string) string {
	value := strings.TrimSpace(model)
	if len(value) > maxModelLength {
		// 截断到 rune 边界。
		value = value[:maxModelLength]
		for len(value) > 0 && !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	if value == "" {
		return "unknown"
	}
	return value
}

func normalizeTimestamp(value int64) int64 {
	if value <= 0 {
		return nowMillis()
	}
	if value < 10_000_000_000 {
		return value * 1_000
	}
	return value
}

func autoGranularity(spanMs int64) granularity {
	const (
		hourMs = 3_600_000
		dayMs  = 86_400_000
	)
	switch {
	case spanMs <= 6*hourMs:
		return granMinute
	case spanMs <= 72*hourMs:
		return granHour
	case spanMs <= 120*dayMs:
		return granDay
	case spanMs <= 730*dayMs:
		return granWeek
	default:
		return granMonth
	}
}

func bucketStart(ms int64, g granularity) int64 {
	moment := time.UnixMilli(ms).In(time.Local)
	switch g {
	case granMinute:
		return time.Date(moment.Year(), moment.Month(), moment.Day(),
			moment.Hour(), moment.Minute(), 0, 0, time.Local).UnixMilli()
	case granHour:
		return time.Date(moment.Year(), moment.Month(), moment.Day(),
			moment.Hour(), 0, 0, 0, time.Local).UnixMilli()
	case granDay:
		return time.Date(moment.Year(), moment.Month(), moment.Day(),
			0, 0, 0, 0, time.Local).UnixMilli()
	case granWeek:
		daysFromMonday := (int(moment.Weekday()) + 6) % 7
		monday := moment.AddDate(0, 0, -daysFromMonday)
		return time.Date(monday.Year(), monday.Month(), monday.Day(),
			0, 0, 0, 0, time.Local).UnixMilli()
	case granMonth:
		return time.Date(moment.Year(), moment.Month(), 1,
			0, 0, 0, 0, time.Local).UnixMilli()
	}
	return ms
}

func sessionIdentity(path string) (string, string) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	idx := -1
	for i, part := range parts {
		if part == "agents" {
			idx = i
			break
		}
	}
	if idx >= 0 {
		session := "unknown"
		if idx >= 1 {
			session = parts[idx-1]
		}
		agent := "unknown"
		if idx+1 < len(parts) {
			agent = parts[idx+1]
		}
		return session, agent
	}
	return "unknown", "unknown"
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}

func satAdd(a, b uint64) uint64 {
	s := a + b
	if s < a {
		return ^uint64(0)
	}
	return s
}

func satAddI64(a, b int64) int64 {
	s := a + b
	if b > 0 && s < a {
		return int64(^uint64(0) >> 1)
	}
	if b < 0 && s > a {
		return -int64(^uint64(0)>>1) - 1
	}
	return s
}

func containsStr(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
