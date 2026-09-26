package scan

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- 移植自 tracker scanner.rs 的测试 ----

// default_home_prefers_the_candidate_with_kimi_sessions
func TestDefaultHomePrefersTheCandidateWithKimiSessions(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	userProfile := filepath.Join(dir, "user-profile")
	mkdirAll(t, filepath.Join(home, ".kimi-code"))
	mkdirAll(t, filepath.Join(userProfile, ".kimi-code", "sessions"))

	got, ok := selectDefaultKimiHome([]string{home, userProfile})
	want := filepath.Join(userProfile, ".kimi-code")
	if !ok || got != want {
		t.Fatalf("selectDefaultKimiHome = %q,%v; want %q", got, ok, want)
	}
}

// default_home_falls_back_to_the_first_user_home
func TestDefaultHomeFallsBackToTheFirstUserHome(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	userProfile := filepath.Join(dir, "user-profile")

	got, ok := selectDefaultKimiHome([]string{home, userProfile})
	want := filepath.Join(home, ".kimi-code")
	if !ok || got != want {
		t.Fatalf("selectDefaultKimiHome = %q,%v; want %q", got, ok, want)
	}
}

// scans_only_usage_records_and_separates_cache_reads
func TestScansOnlyUsageRecordsAndSeparatesCacheReads(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "wd_demo", "session-1", "agents", "main")
	mkdirAll(t, agent)
	writeFile(t, filepath.Join(agent, "wire.jsonl"),
		`{"type":"context.append_loop_event","event":{"usage":{"inputOther":99,"output":99,"inputCacheRead":99,"inputCacheCreation":0}},"time":1784500000000}`+"\n"+
			`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":1784500000000}`+"\n",
	)

	report, err := Scan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 {
		t.Fatalf("requests = %d; want 1", report.Requests)
	}
	if report.Tokens.InputOther != 100 || report.Tokens.Output != 20 ||
		report.Tokens.InputCacheRead != 80 || report.Tokens.Total != 200 {
		t.Fatalf("tokens = %+v", report.Tokens)
	}
	if report.Sessions != 1 {
		t.Fatalf("sessions = %d; want 1", report.Sessions)
	}
	if report.UnpricedRequests != 0 {
		t.Fatalf("unpriced = %d; want 0", report.UnpricedRequests)
	}
	if diff := absF(report.Recent[0].CacheHitRate - (80.0 / 180.0)); diff > 1e-9 {
		t.Fatalf("cache_hit_rate = %f", report.Recent[0].CacheHitRate)
	}
	if report.Recent[0].DurationMs != nil {
		t.Fatalf("duration_ms = %v; want nil", report.Recent[0].DurationMs)
	}
}

// associates_each_usage_record_with_the_preceding_request
func TestAssociatesEachUsageRecordWithThePrecedingRequest(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "wd_demo", "session-1", "agents", "main")
	mkdirAll(t, agent)
	base := int64(1_784_500_000_000)
	writeFile(t, filepath.Join(agent, "wire.jsonl"),
		`{"type":"llm.request","time":`+itoa(base)+`,"messages":[{"role":"user","content":"ignored"}]}`+"\n"+
			`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":20},"usageScope":"turn","time":`+itoa(base+1_500)+`}`+"\n"+
			`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":50,"output":10,"inputCacheRead":0,"inputCacheCreation":0},"usageScope":"turn","time":`+itoa(base+2_000)+`}`+"\n",
	)

	report, err := Scan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 2 {
		t.Fatalf("requests = %d; want 2", report.Requests)
	}
	if report.Recent[0].DurationMs != nil {
		t.Fatalf("recent[0].duration_ms = %v; want nil", *report.Recent[0].DurationMs)
	}
	if report.Recent[1].DurationMs == nil || *report.Recent[1].DurationMs != 1_500 {
		t.Fatalf("recent[1].duration_ms = %v; want 1500", report.Recent[1].DurationMs)
	}
	if diff := absF(report.Recent[1].CacheHitRate - 0.4); diff > 1e-9 {
		t.Fatalf("recent[1].cache_hit_rate = %f; want 0.4", report.Recent[1].CacheHitRate)
	}
}

// rejects_invalid_request_durations
func TestRejectsInvalidRequestDurations(t *testing.T) {
	base := int64(1_784_500_000_000)
	start := base + 2_000
	end := base + 1_000
	if d := requestDurationMs(&start, &end); d != nil {
		t.Fatalf("negative duration = %v; want nil", *d)
	}
	e := base + maxRequestDurationMs + 1
	if d := requestDurationMs(&base, &e); d != nil {
		t.Fatalf("too-long duration = %v; want nil", *d)
	}
}

// prices_environment_defined_k3_alias
func TestPricesEnvironmentDefinedK3Alias(t *testing.T) {
	home := t.TempDir()
	writeUsageRecord(t, home, "k3")

	report, err := Scan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Models) != 1 {
		t.Fatalf("models len = %d; want 1", len(report.Models))
	}
	if report.Models[0].Model != "kimi-k3" {
		t.Fatalf("model = %q; want kimi-k3", report.Models[0].Model)
	}
	if len(report.Models[0].Aliases) != 1 || report.Models[0].Aliases[0] != "k3" {
		t.Fatalf("aliases = %v; want [k3]", report.Models[0].Aliases)
	}
	if report.Models[0].CostCny == nil {
		t.Fatal("cost_cny should be set")
	}
	if report.UnpricedRequests != 0 {
		t.Fatalf("unpriced = %d; want 0", report.UnpricedRequests)
	}
}

// resolves_kimi_internal_environment_alias_from_session_log
func TestResolvesKimiInternalEnvironmentAliasFromSessionLog(t *testing.T) {
	home := t.TempDir()
	writeUsageRecord(t, home, envModelAlias)
	logs := filepath.Join(home, "sessions", "wd_demo", "session-1", "logs")
	mkdirAll(t, logs)
	writeFile(t, filepath.Join(logs, "kimi-code.log"),
		"2026-01-01T00:00:00.000Z INFO llm config provider=openai model=kimi-k3 modelAlias=__kimi_env_model__ thinkingEffort=high\n",
	)

	report, err := Scan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Models) != 1 || report.Models[0].Model != "kimi-k3" {
		t.Fatalf("model = %+v; want kimi-k3", report.Models)
	}
	if len(report.Models[0].Aliases) != 1 || report.Models[0].Aliases[0] != envModelAlias {
		t.Fatalf("aliases = %v", report.Models[0].Aliases)
	}
	if report.Models[0].CostCny == nil {
		t.Fatal("cost_cny should be set")
	}
	if report.UnpricedRequests != 0 {
		t.Fatalf("unpriced = %d; want 0", report.UnpricedRequests)
	}
}

// resolves_configured_model_alias_without_exposing_provider_config
func TestResolvesConfiguredModelAlias(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), `
[providers.custom]
type = "kimi"
api_key = "EXAMPLE_KEY_NOT_USED"

[models."my-k3"]
provider = "custom"
model = "kimi-k3"
max_context_size = 1048576
`)
	writeUsageRecord(t, home, "my-k3")

	report, err := Scan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Models[0].Model != "kimi-k3" {
		t.Fatalf("model = %q; want kimi-k3", report.Models[0].Model)
	}
	if len(report.Models[0].Aliases) != 1 || report.Models[0].Aliases[0] != "my-k3" {
		t.Fatalf("aliases = %v", report.Models[0].Aliases)
	}
	if report.Models[0].Pricing == nil {
		t.Fatal("pricing should be present")
	}
	if report.UnpricedRequests != 0 {
		t.Fatalf("unpriced = %d; want 0", report.UnpricedRequests)
	}
}

// keeps_managed_subscription_model_unpriced
func TestKeepsManagedSubscriptionModelUnpriced(t *testing.T) {
	home := t.TempDir()
	writeUsageRecord(t, home, "kimi-for-coding")

	report, err := Scan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Models[0].Model != "kimi-for-coding" {
		t.Fatalf("model = %q", report.Models[0].Model)
	}
	if report.Models[0].CostCny != nil {
		t.Fatal("cost_cny should be nil")
	}
	if report.UnpricedRequests != 1 {
		t.Fatalf("unpriced = %d; want 1", report.UnpricedRequests)
	}
}

// bounds_distinct_model_buckets
func TestBoundsDistinctModelBuckets(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "wd_demo", "session-1", "agents", "main")
	mkdirAll(t, agent)
	var sb strings.Builder
	for i := 0; i < maxModelBuckets+2; i++ {
		sb.WriteString(`{"type":"usage.record","model":"custom-model-` + itoa(int64(i)) +
			`","usage":{"inputOther":1,"output":0,"inputCacheRead":0,"inputCacheCreation":0},"time":1784500000000}` + "\n")
	}
	writeFile(t, filepath.Join(agent, "wire.jsonl"), sb.String())

	report, err := Scan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	var overflow *ModelUsage
	for i := range report.Models {
		if report.Models[i].Model == overflowModel {
			overflow = &report.Models[i]
		}
	}
	if overflow == nil {
		t.Fatal("overflow model bucket missing")
	}
	if len(report.Models) != maxModelBuckets+1 {
		t.Fatalf("models len = %d; want %d", len(report.Models), maxModelBuckets+1)
	}
	if overflow.Requests != 2 {
		t.Fatalf("overflow requests = %d; want 2", overflow.Requests)
	}
	if !report.LimitsReached {
		t.Fatal("limits_reached should be true")
	}
}

// capped_reader_discards_large_non_usage_lines_without_growing_output
func TestCappedReaderDiscardsLargeLines(t *testing.T) {
	large := bytes.Repeat([]byte{'x'}, maxUsageRecordBytes+32)
	input := append(append([]byte{}, large...), '\n')
	input = append(input, []byte("small\n")...)
	reader := bufio.NewReader(bytes.NewReader(input))
	var output []byte

	oversized, ok, err := readCappedLine(reader, &output, maxUsageRecordBytes)
	if err != nil || !ok {
		t.Fatalf("first line: ok=%v err=%v", ok, err)
	}
	if !oversized {
		t.Fatal("first line should be oversized")
	}
	if len(output) > maxUsageRecordBytes {
		t.Fatalf("output too large: %d", len(output))
	}
	oversized, ok, err = readCappedLine(reader, &output, maxUsageRecordBytes)
	if err != nil || !ok {
		t.Fatalf("second line: ok=%v err=%v", ok, err)
	}
	if oversized {
		t.Fatal("second line should not be oversized")
	}
	if string(output) != "small" {
		t.Fatalf("second line = %q; want small", output)
	}
}

// time_range_filter_includes_closed_bounds
func TestTimeRangeFilterIncludesClosedBounds(t *testing.T) {
	home := t.TempDir()
	base := int64(1_784_500_000_000)
	writeRecords(t, home, "session-1", []rec{
		{"kimi-k3", base + 999, "turn", [4]uint64{100, 20, 80, 0}},
		{"kimi-k3", base + 1_000, "turn", [4]uint64{100, 20, 80, 0}},
		{"kimi-k3", base + 2_000, "turn", [4]uint64{100, 20, 80, 0}},
		{"kimi-k3", base + 2_001, "turn", [4]uint64{100, 20, 80, 0}},
	})

	start := base + 1_000
	end := base + 2_000
	report, err := Scan(home, &ScanFilter{StartMs: &start, EndMs: &end})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 2 {
		t.Fatalf("requests = %d; want 2", report.Requests)
	}
	if report.Tokens.Total != 400 {
		t.Fatalf("total = %d; want 400", report.Tokens.Total)
	}
	if report.Comparison == nil {
		t.Fatal("comparison should be present")
	}
	if report.Comparison.Requests != 1 {
		t.Fatalf("comparison requests = %d; want 1", report.Comparison.Requests)
	}
}

// bucket_start_aligns_to_local_boundaries
func TestBucketStartAlignsToLocalBoundaries(t *testing.T) {
	ts := int64(1_784_500_123_456)

	minute := bucketStart(ts, granMinute)
	if minute > ts || ts-minute >= 60_000 || minute%60_000 != 0 {
		t.Fatalf("minute bucket = %d", minute)
	}

	hour := bucketStart(ts, granHour)
	hourLocal := time.UnixMilli(hour).In(time.Local)
	if hourLocal.Minute() != 0 || hourLocal.Second() != 0 {
		t.Fatalf("hour bucket not aligned: %v", hourLocal)
	}
	if hour > ts || ts-hour >= 3_600_000 {
		t.Fatalf("hour bucket = %d", hour)
	}

	day := bucketStart(ts, granDay)
	dayLocal := time.UnixMilli(day).In(time.Local)
	if dayLocal.Format("15:04:05") != "00:00:00" {
		t.Fatalf("day bucket = %v", dayLocal)
	}
	if day > ts || ts-day >= 2*86_400_000 {
		t.Fatalf("day bucket = %d", day)
	}

	week := bucketStart(ts, granWeek)
	weekLocal := time.UnixMilli(week).In(time.Local)
	if weekLocal.Weekday() != time.Monday || weekLocal.Format("15:04:05") != "00:00:00" {
		t.Fatalf("week bucket = %v", weekLocal)
	}
	if week > ts || ts-week >= 8*86_400_000 {
		t.Fatalf("week bucket = %d", week)
	}

	month := bucketStart(ts, granMonth)
	monthLocal := time.UnixMilli(month).In(time.Local)
	if monthLocal.Day() != 1 || monthLocal.Format("15:04:05") != "00:00:00" {
		t.Fatalf("month bucket = %v", monthLocal)
	}
	if month > ts || ts-month >= 32*86_400_000 {
		t.Fatalf("month bucket = %d", month)
	}

	for _, g := range []granularity{granMinute, granHour, granDay, granWeek, granMonth} {
		b := bucketStart(ts, g)
		if bucketStart(b, g) != b {
			t.Fatalf("bucket_start idempotency failed for %v", g)
		}
	}
}

// auto_granularity_selects_by_span
func TestAutoGranularitySelectsBySpan(t *testing.T) {
	const (
		hour = int64(3_600_000)
		day  = int64(86_400_000)
	)
	if autoGranularity(0) != granMinute ||
		autoGranularity(6*hour) != granMinute ||
		autoGranularity(6*hour+1) != granHour ||
		autoGranularity(72*hour) != granHour ||
		autoGranularity(72*hour+1) != granDay ||
		autoGranularity(120*day) != granDay ||
		autoGranularity(120*day+1) != granWeek ||
		autoGranularity(730*day) != granWeek ||
		autoGranularity(730*day+1) != granMonth {
		t.Fatal("auto_granularity mismatch")
	}
}

// auto_granularity_is_reported_from_filter_span
func TestAutoGranularityIsReportedFromFilterSpan(t *testing.T) {
	home := t.TempDir()
	base := int64(1_784_500_000_000)
	writeRecords(t, home, "session-1", []rec{{"kimi-k3", base, "turn", [4]uint64{100, 20, 80, 0}}})

	start, end := base-7_200_000, base
	report, err := Scan(home, &ScanFilter{StartMs: &start, EndMs: &end})
	if err != nil {
		t.Fatal(err)
	}
	if report.Granularity != "minute" {
		t.Fatalf("granularity = %q; want minute", report.Granularity)
	}

	start, end = base-10*86_400_000, base
	report, err = Scan(home, &ScanFilter{StartMs: &start, EndMs: &end})
	if err != nil {
		t.Fatal(err)
	}
	if report.Granularity != "day" {
		t.Fatalf("granularity = %q; want day", report.Granularity)
	}
}

// series_buckets_follow_requested_granularity
func TestSeriesBucketsFollowRequestedGranularity(t *testing.T) {
	home := t.TempDir()
	hourStart := bucketStart(1_784_500_000_000, granHour)
	writeRecords(t, home, "session-1", []rec{
		{"kimi-k3", hourStart, "turn", [4]uint64{100, 20, 80, 0}},
		{"kimi-k3", hourStart + 60_000, "turn", [4]uint64{100, 20, 80, 0}},
	})

	scanWith := func(gran string) *UsageReport {
		report, err := Scan(home, &ScanFilter{Granularity: gran})
		if err != nil {
			t.Fatal(err)
		}
		return report
	}

	r := scanWith("minute")
	if r.Granularity != "minute" || len(r.Series) != 2 || r.Series[0].StartMs >= r.Series[1].StartMs {
		t.Fatalf("minute: gran=%s series=%d", r.Granularity, len(r.Series))
	}

	r = scanWith("hour")
	if len(r.Series) != 1 || r.Series[0].StartMs != hourStart || r.Series[0].Requests != 2 {
		t.Fatalf("hour: series=%+v", r.Series)
	}

	r = scanWith("day")
	if len(r.Series) != 1 || r.Series[0].StartMs != bucketStart(hourStart, granDay) {
		t.Fatalf("day: series=%+v", r.Series)
	}

	r = scanWith("week")
	if len(r.Series) != 1 || time.UnixMilli(r.Series[0].StartMs).In(time.Local).Weekday() != time.Monday {
		t.Fatalf("week: series=%+v", r.Series)
	}

	r = scanWith("month")
	if len(r.Series) != 1 || time.UnixMilli(r.Series[0].StartMs).In(time.Local).Day() != 1 {
		t.Fatalf("month: series=%+v", r.Series)
	}
}

// series_granularity_upgrades_when_buckets_exceed_cap
func TestSeriesGranularityUpgradesWhenBucketsExceedCap(t *testing.T) {
	state := newScanState(granMinute, false)
	usage := rawTokenUsage{InputOther: 1}
	total := int64(maxSeriesBuckets) + 10
	for i := int64(0); i < total; i++ {
		insertSeriesBucket(state, 1_784_500_000_000+i*60_000, &usage, nil)
	}
	if state.seriesGranularity != granHour {
		t.Fatalf("granularity = %v; want hour", state.seriesGranularity)
	}
	if len(state.series) > maxSeriesBuckets {
		t.Fatalf("series len = %d; want <= %d", len(state.series), maxSeriesBuckets)
	}
	var sum uint64
	for _, agg := range state.series {
		sum = satAdd(sum, agg.requests)
	}
	if sum != uint64(total) {
		t.Fatalf("sum = %d; want %d", sum, total)
	}
}

// comparison_summarizes_previous_window_only
func TestComparisonSummarizesPreviousWindowOnly(t *testing.T) {
	home := t.TempDir()
	start := int64(1_784_500_001_000)
	writeRecords(t, home, "session-1", []rec{
		{"kimi-k3", start, "turn", [4]uint64{100, 20, 80, 0}},
		{"kimi-k3", start - 1, "turn", [4]uint64{200, 40, 160, 0}},
		{"kimi-k3", start - 1_001, "turn", [4]uint64{400, 0, 0, 0}},
	})

	end := start + 999
	report, err := Scan(home, &ScanFilter{StartMs: &start, EndMs: &end})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 || report.Tokens.InputOther != 100 || len(report.Recent) != 1 {
		t.Fatalf("main: req=%d total=%d recent=%d", report.Requests, report.Tokens.InputOther, len(report.Recent))
	}
	var dimSum uint64
	for _, v := range report.Dimensions.Models {
		dimSum = satAdd(dimSum, v.Requests)
	}
	if dimSum != 1 {
		t.Fatalf("dimension requests = %d; want 1", dimSum)
	}

	c := report.Comparison
	if c == nil {
		t.Fatal("comparison missing")
	}
	if c.Requests != 1 || c.Sessions != 1 || c.Tokens.InputOther != 200 || c.Tokens.InputCacheRead != 160 {
		t.Fatalf("comparison = %+v", c)
	}
	if diff := absF(c.CacheHitRate - 160.0/360.0); diff > 1e-9 {
		t.Fatalf("comparison cache_hit_rate = %f", c.CacheHitRate)
	}
}

// billing_filter_selects_priced_or_unpriced
func TestBillingFilterSelectsPricedOrUnpriced(t *testing.T) {
	home := t.TempDir()
	writeRecords(t, home, "session-1", []rec{
		{"kimi-k3", 1_784_500_000_000, "turn", [4]uint64{100, 20, 80, 0}},
		{"kimi-for-coding", 1_784_500_000_000, "turn", [4]uint64{100, 20, 80, 0}},
	})

	report, err := Scan(home, &ScanFilter{Billing: "priced"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 || len(report.Models) != 1 || report.Models[0].Model != "kimi-k3" {
		t.Fatalf("priced: req=%d models=%d", report.Requests, len(report.Models))
	}

	report, err = Scan(home, &ScanFilter{Billing: "unpriced"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 || report.UnpricedRequests != 1 ||
		len(report.Models) != 1 || report.Models[0].Model != "kimi-for-coding" {
		t.Fatalf("unpriced: req=%d unpriced=%d models=%d", report.Requests, report.UnpricedRequests, len(report.Models))
	}
}

// scopes_filter_and_group_unspecified_scope
func TestScopesFilterAndGroupUnspecifiedScope(t *testing.T) {
	home := t.TempDir()
	writeRecords(t, home, "session-1", []rec{
		{"kimi-k3", 1_784_500_000_000, "turn", [4]uint64{100, 20, 80, 0}},
		{"kimi-k3", 1_784_500_000_001, "", [4]uint64{100, 20, 80, 0}},
	})

	report, err := Scan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 2 {
		t.Fatalf("requests = %d", report.Requests)
	}
	var scopes []string
	for _, v := range report.Dimensions.Scopes {
		scopes = append(scopes, v.Value)
	}
	if !containsStr(scopes, "turn") || !containsStr(scopes, unspecifiedScope) {
		t.Fatalf("scopes = %v", scopes)
	}
	var unscoped *RecentUsage
	for i := range report.Recent {
		if report.Recent[i].Scope == unspecifiedScope {
			unscoped = &report.Recent[i]
		}
	}
	if unscoped == nil || unscoped.Time != 1_784_500_000_001 {
		t.Fatalf("unscoped recent = %+v", unscoped)
	}

	report, err = Scan(home, &ScanFilter{Scopes: []string{"turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 || report.Recent[0].Scope != "turn" {
		t.Fatalf("filtered: req=%d recent=%+v", report.Requests, report.Recent[0])
	}
}

// dimensions_cover_models_beyond_the_model_whitelist
func TestDimensionsCoverModelsBeyondTheModelWhitelist(t *testing.T) {
	home := t.TempDir()
	writeRecords(t, home, "session-1", []rec{
		{"kimi-k3", 1_784_500_000_000, "turn", [4]uint64{100, 20, 80, 0}},
		{"custom-model-x", 1_784_500_000_001, "turn", [4]uint64{100, 20, 80, 0}},
	})

	report, err := Scan(home, &ScanFilter{Models: []string{"kimi-k3"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 || len(report.Models) != 1 || report.Models[0].Model != "kimi-k3" {
		t.Fatalf("filtered: req=%d models=%d", report.Requests, len(report.Models))
	}
	foundK3, foundCustom := false, false
	for _, v := range report.Dimensions.Models {
		if v.Value == "kimi-k3" && v.Requests == 1 {
			foundK3 = true
		}
		if v.Value == "custom-model-x" && v.Requests == 1 {
			foundCustom = true
		}
	}
	if !foundK3 || !foundCustom {
		t.Fatalf("dimension models = %+v", report.Dimensions.Models)
	}
}

// groups_usage_by_provider_and_filters_by_provider_then_model
func TestProviderGroupingAndFilters(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), `
[providers."managed:kimi-code"]
type = "kimi"

[providers.custom]
type = "openai"

[models."kimi-code/k3"]
provider = "managed:kimi-code"
model = "kimi-k3"

[models."kimi-code/k3-256k"]
provider = "managed:kimi-code"
model = "kimi-k3"

[models."custom/glm"]
provider = "custom"
model = "glm-5.3"
`)
	writeRecords(t, home, "session-1", []rec{
		{"kimi-code/k3", 1_784_500_000_000, "turn", [4]uint64{100, 20, 80, 0}},
		{"kimi-code/k3-256k", 1_784_500_000_001, "turn", [4]uint64{50, 10, 30, 0}},
		{"custom/glm", 1_784_500_000_002, "turn", [4]uint64{70, 5, 0, 0}},
	})

	report, err := Scan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 厂家维度计数：managed:kimi-code 2 条、custom 1 条。
	provCount := map[string]uint64{}
	for _, v := range report.Dimensions.Providers {
		provCount[v.Value] = v.Requests
	}
	if provCount["managed:kimi-code"] != 2 || provCount["custom"] != 1 {
		t.Fatalf("providers = %v", provCount)
	}
	// 模型→厂家映射（级联下拉的数据源）。
	if got := report.Dimensions.ModelProviders["kimi-k3"]; got != "managed:kimi-code" {
		t.Fatalf("modelProviders[kimi-k3] = %q", got)
	}
	if got := report.Dimensions.ModelProviders["glm-5.3"]; got != "custom" {
		t.Fatalf("modelProviders[glm-5.3] = %q", got)
	}
	// 模型统计行带厂家，且同厂家两个别名解析出的同名模型 kimi-k3 合并。
	for _, m := range report.Models {
		if m.Model == "kimi-k3" && m.Provider != "managed:kimi-code" {
			t.Fatalf("kimi-k3 provider = %q", m.Provider)
		}
	}
	if n := len(report.Models); n != 2 {
		t.Fatalf("models = %d; want 2", n)
	}

	// 厂家单条件筛选：只剩 managed:kimi-code 的 2 条。
	report, err = Scan(home, &ScanFilter{Providers: []string{"managed:kimi-code"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 2 {
		t.Fatalf("provider filter requests = %d; want 2", report.Requests)
	}

	// 厂家+模型双条件：managed:kimi-code 下只看 k3-256k 别名解析出的 kimi-k3。
	// 注意：别名 kimi-code/k3 与 kimi-code/k3-256k 解析后同名（都是 kimi-k3），
	// 此处用不重名的模型验证双条件正交性。
	writeRecords(t, home, "session-1", []rec{
		{"kimi-code/k3", 1_784_500_000_000, "turn", [4]uint64{100, 20, 80, 0}},
		{"custom/glm", 1_784_500_000_002, "turn", [4]uint64{70, 5, 0, 0}},
	})
	report, err = Scan(home, &ScanFilter{
		Providers: []string{"custom"},
		Models:    []string{"glm-5.3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 || len(report.Models) != 1 || report.Models[0].Model != "glm-5.3" {
		t.Fatalf("dual filter: req=%d models=%+v", report.Requests, report.Models)
	}

	// 双条件冲突组合（该厂家下不存在此模型）：空结果而非错误。
	report, err = Scan(home, &ScanFilter{
		Providers: []string{"managed:kimi-code"},
		Models:    []string{"glm-5.3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 0 {
		t.Fatalf("conflicting filter requests = %d; want 0", report.Requests)
	}
}

// provider_prefix_fallback_when_config_missing
func TestProviderPrefixFallback(t *testing.T) {
	home := t.TempDir()
	// config.toml 缺失：记录模型名 "zhipuai-coding-plan/glm" 应按 "provider/model"
	// 命名惯例归入厂家 zhipuai-coding-plan；裸模型名归入"未标注"。
	writeRecords(t, home, "session-1", []rec{
		{"zhipuai-coding-plan/glm", 1_784_500_000_000, "turn", [4]uint64{100, 20, 80, 0}},
		{"bare-model", 1_784_500_000_001, "turn", [4]uint64{70, 5, 0, 0}},
	})

	report, err := Scan(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	provCount := map[string]uint64{}
	for _, v := range report.Dimensions.Providers {
		provCount[v.Value] = v.Requests
	}
	if provCount["zhipuai-coding-plan"] != 1 || provCount[unspecifiedScope] != 1 {
		t.Fatalf("providers = %v", provCount)
	}

	// 按前缀厂家筛选。
	report, err = Scan(home, &ScanFilter{Providers: []string{"zhipuai-coding-plan"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 || len(report.Models) != 1 || report.Models[0].Model != "zhipuai-coding-plan/glm" {
		t.Fatalf("prefix filter: req=%d models=%+v", report.Requests, report.Models)
	}
}

// ---- 测试辅助 ----

type rec struct {
	model string
	time  int64
	scope string
	usage [4]uint64
}

func writeUsageRecord(t *testing.T, home, model string) {
	t.Helper()
	agent := filepath.Join(home, "sessions", "wd_demo", "session-1", "agents", "main")
	mkdirAll(t, agent)
	writeFile(t, filepath.Join(agent, "wire.jsonl"),
		`{"type":"usage.record","model":"`+model+`","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":1784500000000}`+"\n",
	)
}

func writeRecords(t *testing.T, home, session string, records []rec) {
	t.Helper()
	agent := filepath.Join(home, "sessions", "wd_demo", session, "agents", "main")
	mkdirAll(t, agent)
	var sb strings.Builder
	for _, r := range records {
		scope := `"` + r.scope + `"`
		if r.scope == "" {
			scope = `null`
		}
		sb.WriteString(`{"type":"usage.record","model":"` + r.model + `","usage":{"inputOther":` +
			itoa(int64(r.usage[0])) + `,"output":` + itoa(int64(r.usage[1])) +
			`,"inputCacheRead":` + itoa(int64(r.usage[2])) +
			`,"inputCacheCreation":` + itoa(int64(r.usage[3])) + `}` +
			`,"usageScope":` + scope + `,"time":` + itoa(r.time) + `}` + "\n")
	}
	writeFile(t, filepath.Join(agent, "wire.jsonl"), sb.String())
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
