package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kimi-hud/internal/metrics"
	"kimi-hud/internal/modelcfg"
	"kimi-hud/internal/quota"
	"kimi-hud/internal/scan"
	"kimi-hud/internal/today"
)

// TestBuildLiveEmpty 空状态：可选字段为 null，页面显示占位符。
func TestBuildLiveEmpty(t *testing.T) {
	st := metrics.NewState()
	qc := quota.NewClient(t.TempDir()+"/creds.json", t.TempDir()+"/quota.json")
	mc := modelcfg.Load(t.TempDir() + "/nonexistent.toml")
	tm := today.NewManager(t.TempDir())
	d := buildLive(st, qc, mc, tm, time.Now())
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"tps":null`, `"ttft":null`, `"cacheRate":null`, `"today":null`, `"quota":null`,
		`"swarm":false`, `"agents":0`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("buildLive empty missing %s\n%s", want, s)
		}
	}
}

// TestBuildLiveQuotaFields 配额窗口快照带剩余量与重置时间（前端渲染进度条/倒计时所需）。
func TestBuildLiveQuotaFields(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	credsPath := filepath.Join(dir, "creds.json")
	cachePath := filepath.Join(dir, "quota.json")
	cache := fmt.Sprintf(`{"fetchedAt":%q,"windows":[
		{"label":"7d","used":36,"limit":100,"remaining":64,"resetTime":%q},
		{"label":"5h","used":90,"limit":100,"remaining":10,"resetTime":%q}
	]}`,
		now.Format(time.RFC3339Nano),
		now.Add(3*24*time.Hour).UTC().Format(time.RFC3339),
		now.Add(2*time.Hour).UTC().Format(time.RFC3339))
	if err := os.WriteFile(cachePath, []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}
	// 有效凭据（含 refresh_token）：status 有值且为 ok。
	if err := os.WriteFile(credsPath, []byte(`{"access_token":"tok","refresh_token":"rt"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	qc := quota.NewClient(credsPath, cachePath)
	st := metrics.NewState()
	mc := modelcfg.Load(filepath.Join(dir, "nonexistent.toml"))
	tm := today.NewManager(filepath.Join(dir, "home"))
	d := buildLive(st, qc, mc, tm, now)
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Quota *struct {
			Windows []struct {
				Label     string  `json:"label"`
				Used      float64 `json:"used"`
				Limit     float64 `json:"limit"`
				Remaining float64 `json:"remaining"`
				ResetTime string  `json:"resetTime"`
			} `json:"windows"`
			Status string `json:"status"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Quota == nil || len(got.Quota.Windows) != 2 {
		t.Fatalf("quota windows = %+v", got.Quota)
	}
	if w := got.Quota.Windows[0]; w.Label != "7d" || w.Used != 36 || w.Limit != 100 || w.Remaining != 64 || w.ResetTime == "" {
		t.Fatalf("7d window = %+v", w)
	}
	if w := got.Quota.Windows[1]; w.Label != "5h" || w.Used != 90 || w.Limit != 100 || w.Remaining != 10 || w.ResetTime == "" {
		t.Fatalf("5h window = %+v", w)
	}
	if got.Quota.Status == "" {
		t.Fatal("quota status empty")
	}
}

// TestBuildLiveQuotaStatusNoRefresh 已认证但凭据无 refresh_token（Kimi CLI 常见形态）：
// status 仍应输出 "ok"。CredentialsState 第二返回值是"是否携带 refresh_token"，
// 与"状态是否有效"无关（R1-1 修复的回归用例）。
func TestBuildLiveQuotaStatusNoRefresh(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	cachePath := filepath.Join(dir, "quota.json")
	credsPath := filepath.Join(dir, "creds.json")
	cache := fmt.Sprintf(`{"fetchedAt":%q,"windows":[{"label":"7d","used":36,"limit":100,"remaining":64,"resetTime":%q}]}`,
		now.Format(time.RFC3339Nano), now.Add(3*24*time.Hour).UTC().Format(time.RFC3339))
	if err := os.WriteFile(cachePath, []byte(cache), 0o644); err != nil {
		t.Fatal(err)
	}
	// 有 access_token 但无 refresh_token：已认证，status 应为 "ok" 而非空/未登录。
	if err := os.WriteFile(credsPath, []byte(`{"access_token":"tok"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	qc := quota.NewClient(credsPath, cachePath)
	d := buildLive(metrics.NewState(), qc,
		modelcfg.Load(filepath.Join(dir, "nonexistent.toml")),
		today.NewManager(filepath.Join(dir, "home")), now)
	if d.Quota == nil {
		t.Fatal("quota nil")
	}
	if d.Quota.Status != "ok" {
		t.Fatalf("status = %q; want ok（有 access_token 即已认证，与 refresh_token 无关）", d.Quota.Status)
	}
}

// TestHandleWebMessageRPC default_home / request_live / unknown method 回复格式。
func TestHandleWebMessageRPC(t *testing.T) {
	st := metrics.NewState()
	qc := quota.NewClient(t.TempDir()+"/creds.json", t.TempDir()+"/quota.json")
	mc := modelcfg.Load(t.TempDir() + "/nonexistent.toml")
	tm := today.NewManager(t.TempDir())
	liveFn := func() liveData { return buildLive(st, qc, mc, tm, time.Now()) }

	wv := &fakePusher{}

	// default_home
	handleWebMessage(`{"id":1,"method":"default_home"}`, wv, liveFn, qc, mc, tm, "C:/home")
	// request_live
	handleWebMessage(`{"id":2,"method":"request_live"}`, wv, liveFn, qc, mc, tm, "C:/home")
	// unknown
	handleWebMessage(`{"id":3,"method":"nope"}`, wv, liveFn, qc, mc, tm, "C:/home")

	got := wv.All()
	if len(got) != 3 {
		t.Fatalf("want 3 replies, got %d: %v", len(got), got)
	}
	var r1, r2, r3 struct {
		ID    int             `json:"id"`
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	json.Unmarshal([]byte(got[0]), &r1)
	json.Unmarshal([]byte(got[1]), &r2)
	json.Unmarshal([]byte(got[2]), &r3)
	if r1.ID != 1 || !r1.OK || string(r1.Data) != `"C:/home"` {
		t.Errorf("default_home reply wrong: %s", got[0])
	}
	if r2.ID != 2 || !r2.OK || !strings.Contains(string(r2.Data), `"tps":null`) {
		t.Errorf("request_live reply wrong: %s", got[1])
	}
	if r3.ID != 3 || r3.OK || !strings.Contains(r3.Error, "unknown method") {
		t.Errorf("unknown reply wrong: %s", got[2])
	}
}

// fakePusher 带锁收集推送（测试异步 goroutine 与主测试并发读写 got，避免自身竞态）。
type fakePusher struct {
	mu  sync.Mutex
	got []string
	fn  func(string)
}

func (f *fakePusher) Push(s string) {
	f.mu.Lock()
	f.got = append(f.got, s)
	f.mu.Unlock()
	if f.fn != nil {
		f.fn(s)
	}
}

// PushReply 与 Push 同语义（测试收集即可，无需区分优先级）。
func (f *fakePusher) PushReply(s string) {
	f.Push(s)
}

func (f *fakePusher) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func (f *fakePusher) All() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

// buildTodayHome 构造含一条今日 usage.record 的测试 home（对齐 scan/today_test.go 数据模式）。
func buildTodayHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	today := start + (now.UnixMilli()-start)/2
	line := fmt.Sprintf(`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":%d}`+"\n", today)
	if err := os.WriteFile(filepath.Join(agent, "wire.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestHandleWebMessageScanTodayRPC "今日"档走 ScanToday 同口径路径（R3）：
// 回复 todayMode=true、tokens 汇总正确、模型统计按今日聚合、最近表为空（轻量模式）。
func TestHandleWebMessageScanTodayRPC(t *testing.T) {
	home := buildTodayHome(t)
	qc := quota.NewClient(t.TempDir()+"/creds.json", t.TempDir()+"/quota.json")
	mc := modelcfg.Load(t.TempDir() + "/nonexistent.toml")
	tm := today.NewManager(t.TempDir())
	liveFn := func() liveData { return buildLive(metrics.NewState(), qc, mc, tm, time.Now()) }

	wv := &fakePusher{}

	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	msg := fmt.Sprintf(`{"id":9,"method":"scan_usage","params":{"range":"today","startMs":%d,"endMs":%d}}`, start, now.UnixMilli())
	handleWebMessage(msg, wv, liveFn, qc, mc, tm, home)

	deadline := time.Now().Add(5 * time.Second)
	for wv.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := wv.All()
	if len(got) == 0 {
		t.Fatal("no reply for today scan_usage")
	}
	var r struct {
		ID   int  `json:"id"`
		OK   bool `json:"ok"`
		Data struct {
			TodayMode     bool             `json:"todayMode"`
			Tokens        scan.TokenTotals `json:"tokens"`
			Models        []any            `json:"models"`
			Recent        []any            `json:"recent"`
			LimitsReached bool             `json:"limitsReached"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(got[0]), &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != 9 || !r.OK || !r.Data.TodayMode {
		t.Fatalf("today reply wrong: %s", got[0])
	}
	if r.Data.Tokens.Total != 200 || r.Data.Tokens.InputOther != 100 ||
		r.Data.Tokens.Output != 20 || r.Data.Tokens.InputCacheRead != 80 {
		t.Fatalf("tokens = %+v; want 100/20/80 total 200", r.Data.Tokens)
	}
	// 今日档模型统计：today-only 聚合（同口径），按模型名构建（buildTodayHome 仅 kimi-k3）。
	if len(r.Data.Models) != 1 {
		t.Fatalf("today mode should have 1 model, got %d", len(r.Data.Models))
	}
	if m := r.Data.Models[0].(map[string]any); m["model"] != "kimi-k3" {
		t.Fatalf("today model = %v; want kimi-k3", m["model"])
	}
	// 今日档最近请求：与模型表一体展示（buildTodayHome 1 条 usage.record）。
	if len(r.Data.Recent) != 1 {
		t.Fatalf("today mode should have 1 recent, got %d", len(r.Data.Recent))
	}
	if r.Data.LimitsReached {
		t.Fatal("limits_reached = true; want false")
	}
}

// buildMultiProviderHome 构造双厂家 fixture：config.toml 定义
// kimi-code/k3 → managed:kimi-code/kimi-k3 与 custom/glm → custom/glm-5.3，
// 各写入一条今日记录（总量分别 200 / 140）。
func buildMultiProviderHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `[models."kimi-code/k3"]
provider = "managed:kimi-code"
model = "kimi-k3"

[models."custom/glm"]
provider = "custom"
model = "glm-5.3"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	ts := start + (now.UnixMilli()-start)/2
	line := func(model string, in int64) string {
		return fmt.Sprintf(`{"type":"usage.record","model":%q,"usage":{"inputOther":%d,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":%d}`+"\n", model, in, ts)
	}
	data := line("kimi-code/k3", 100) + line("custom/glm", 40)
	if err := os.WriteFile(filepath.Join(agent, "wire.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestScanUsageRPCProviderFilter 厂家筛选经 RPC 生效（今日档）：
// 1) 回复 models/tokens 只含所选厂家；2) dimensions.providers 仍为筛选前全量
// （选项不因筛选缩水）；3) 带筛选的扫描不回填今日缓存（缓存只反映全量今日用量）。
func TestScanUsageRPCProviderFilter(t *testing.T) {
	home := buildMultiProviderHome(t)
	qc := quota.NewClient(t.TempDir()+"/creds.json", t.TempDir()+"/quota.json")
	mc := modelcfg.Load(t.TempDir() + "/nonexistent.toml")
	tm := today.NewManager(home)
	liveFn := func() liveData { return buildLive(metrics.NewState(), qc, mc, tm, time.Now()) }

	// 初始缓存：全量今日 total = (100+20+80) + (40+20+80) = 340。
	if g := tm.Refresh(); g.Tokens.Total != 340 {
		t.Fatalf("initial cache total = %d; want 340", g.Tokens.Total)
	}

	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	wv := &fakePusher{}
	msg := fmt.Sprintf(`{"id":11,"method":"scan_usage","params":{"range":"today","startMs":%d,"endMs":%d,"providers":["managed:kimi-code"]}}`, start, now.UnixMilli())
	handleWebMessage(msg, wv, liveFn, qc, mc, tm, home)

	deadline := time.Now().Add(5 * time.Second)
	for wv.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if wv.Len() == 0 {
		t.Fatal("no reply for filtered scan_usage")
	}
	var r struct {
		OK   bool `json:"ok"`
		Data struct {
			TodayMode  bool             `json:"todayMode"`
			Tokens     scan.TokenTotals `json:"tokens"`
			Models     []map[string]any `json:"models"`
			Dimensions struct {
				Providers []struct {
					Value    string `json:"value"`
					Requests uint64 `json:"requests"`
				} `json:"providers"`
				ModelProviders map[string]string `json:"modelProviders"`
			} `json:"dimensions"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(wv.All()[0]), &r); err != nil {
		t.Fatal(err)
	}
	if !r.OK || !r.Data.TodayMode {
		t.Fatalf("reply wrong: %s", wv.All()[0])
	}
	// 只剩 managed:kimi-code（kimi-code/k3 → kimi-k3，200）。
	if r.Data.Tokens.Total != 200 {
		t.Fatalf("filtered tokens total = %d; want 200", r.Data.Tokens.Total)
	}
	if len(r.Data.Models) != 1 || r.Data.Models[0]["model"] != "kimi-k3" {
		t.Fatalf("filtered models = %+v; want only kimi-k3", r.Data.Models)
	}
	// 维度选项为筛选前全量：两个厂家都在，模型→厂家映射齐全。
	provVals := map[string]uint64{}
	for _, p := range r.Data.Dimensions.Providers {
		provVals[p.Value] = p.Requests
	}
	if provVals["managed:kimi-code"] != 1 || provVals["custom"] != 1 {
		t.Fatalf("dimensions.providers = %v; want both (pre-filter)", provVals)
	}
	if r.Data.Dimensions.ModelProviders["glm-5.3"] != "custom" {
		t.Fatalf("modelProviders = %v", r.Data.Dimensions.ModelProviders)
	}
	// 核心断言：带筛选的扫描不回填缓存（缓存仍为全量 340，不被子集污染）。
	if g := tm.Get(); g.Tokens.Total != 340 {
		t.Fatalf("today cache polluted by filtered scan: total = %d; want 340", g.Tokens.Total)
	}
}

// TestScanUsageRPCNoFilterBackfillsCache 对照组：同 fixture 无筛选今日档扫描后
// 缓存被回填为全量值（与 TestScanTodayRPCBackfillsTodayCache 一致的行为）。
func TestScanUsageRPCNoFilterBackfillsCache(t *testing.T) {
	home := buildMultiProviderHome(t)
	qc := quota.NewClient(t.TempDir()+"/creds.json", t.TempDir()+"/quota.json")
	mc := modelcfg.Load(t.TempDir() + "/nonexistent.toml")
	tm := today.NewManager(home)

	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	wv := &fakePusher{}
	liveFn := func() liveData { return buildLive(metrics.NewState(), qc, mc, tm, time.Now()) }
	msg := fmt.Sprintf(`{"id":12,"method":"scan_usage","params":{"range":"today","startMs":%d,"endMs":%d}}`, start, now.UnixMilli())
	handleWebMessage(msg, wv, liveFn, qc, mc, tm, home)

	deadline := time.Now().Add(5 * time.Second)
	for wv.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if wv.Len() == 0 {
		t.Fatal("no reply")
	}
	if g := tm.Get(); g.Tokens.Total != 340 {
		t.Fatalf("unfiltered scan cache total = %d; want 340", g.Tokens.Total)
	}
}

// TestLiveChanged 变化判定的回归：前端展示项变化必须触发推送（R2-新1 修复——
// Agents 数与 Quota.Status 此前未参与比较；R3-新6 补 ResetTime 倒计时基准与
// Limit 百分比/剩余量基准），未比较字段（Today 四维明细联动 Total、Swarm/
// ModelAlias/窗口 Remaining 前端不渲染）不触发。
func TestLiveChanged(t *testing.T) {
	todayA := &today.Totals{Tokens: scan.TokenTotals{InputOther: 100, Output: 20, Total: 120}}
	todayB := &today.Totals{Tokens: scan.TokenTotals{InputOther: 60, Output: 60, Total: 120}}
	todayMore := &today.Totals{Tokens: scan.TokenTotals{InputOther: 110, Output: 20, Total: 130}}
	quotaA := &liveQuota{Status: "ok", Windows: []liveWindow{{Label: "5h", Used: 10, Limit: 100}}}
	quotaB := &liveQuota{Status: "not-logged-in", Windows: []liveWindow{{Label: "5h", Used: 10, Limit: 100}}}
	quotaUsed := &liveQuota{Status: "ok", Windows: []liveWindow{{Label: "5h", Used: 11, Limit: 100}}}
	quotaReset := &liveQuota{Status: "ok", Windows: []liveWindow{{Label: "5h", Used: 10, Limit: 100, ResetTime: time.Now().Add(-time.Hour)}}}
	quotaLimit := &liveQuota{Status: "ok", Windows: []liveWindow{{Label: "5h", Used: 10, Limit: 120}}}
	quotaRemaining := &liveQuota{Status: "ok", Windows: []liveWindow{{Label: "5h", Used: 10, Limit: 100, Remaining: 50}}}

	base := liveData{Agents: 1, Today: todayA, Quota: quotaA}

	cases := []struct {
		name string
		a, b liveData
		want bool
	}{
		{"identical", base, base, false},
		{"agents changed", base, func() liveData { d := base; d.Agents = 3; return d }(), true},
		{"today total changed", base, func() liveData { d := base; d.Today = todayMore; return d }(), true},
		{"today nil toggle", base, func() liveData { d := base; d.Today = nil; return d }(), true},
		{"quota status changed", base, func() liveData { d := base; d.Quota = quotaB; return d }(), true},
		{"quota used changed", base, func() liveData { d := base; d.Quota = quotaUsed; return d }(), true},
		{"quota nil toggle", base, func() liveData { d := base; d.Quota = nil; return d }(), true},
		// 窗口轮转后 ResetTime 翻新而 Used 0→0：不推送会让前端倒计时停在 ~reset。
		{"quota resetTime changed", base, func() liveData { d := base; d.Quota = quotaReset; return d }(), true},
		// 前端 pct/进度条/剩余量消费 limit（frontend.html quotaPct/remain）。
		{"quota limit changed", base, func() liveData { d := base; d.Quota = quotaLimit; return d }(), true},
		// 四维明细互变（InputOther/Output 此消彼长）Total 不变：不推送。
		{"today detail swap same total", base, func() liveData { d := base; d.Today = todayB; return d }(), false},
		// 前端不渲染 Swarm/ModelAlias 与窗口 Remaining：不推送。
		{"swarm toggled", base, func() liveData { d := base; d.Swarm = !d.Swarm; return d }(), false},
		{"alias changed", base, func() liveData { d := base; d.ModelAlias = "other"; return d }(), false},
		{"quota remaining changed", base, func() liveData { d := base; d.Quota = quotaRemaining; return d }(), false},
	}
	for _, c := range cases {
		if got := liveChanged(c.a, c.b); got != c.want {
			t.Errorf("%s: liveChanged = %v; want %v", c.name, got, c.want)
		}
	}
}

// TestScanTodayRPCBackfillsTodayCache 集成验证用户诉求的同步链路：
// 先让今日缓存有旧值（模拟 5 分钟后台刷新），追加新记录后前端请求"今日"档
// （scan_usage RPC → ScanToday 实时扫描），完成后缓存被回填为新值——
// 顶部"今日总 token"实时卡（读缓存）与下方"今日"档（读扫描结果）数据同步。
func TestScanTodayRPCBackfillsTodayCache(t *testing.T) {
	home := buildTodayHome(t)
	qc := quota.NewClient(t.TempDir()+"/creds.json", t.TempDir()+"/quota.json")
	mc := modelcfg.Load(t.TempDir() + "/nonexistent.toml")
	tm := today.NewManager(home)

	// 初始缓存：buildTodayHome 仅 1 条记录 → total 200。
	if g := tm.Refresh(); g.Tokens.Total != 200 {
		t.Fatalf("initial cache total = %d; want 200", g.Tokens.Total)
	}

	// 追加一条记录 → 今日总量应变为 400（模拟两次扫描之间有新请求）。
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	today := start + (now.UnixMilli()-start)/2
	line := fmt.Sprintf(`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":%d}`+"\n", today)
	f, err := os.OpenFile(filepath.Join(home, "sessions", "s1", "agents", "main", "wire.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// 前端请求"今日"档（与真实页面 loadReport 相同调用）。
	wv := &fakePusher{}
	liveFn := func() liveData { return buildLive(metrics.NewState(), qc, mc, tm, time.Now()) }
	msg := fmt.Sprintf(`{"id":9,"method":"scan_usage","params":{"range":"today","startMs":%d,"endMs":%d}}`, start, now.UnixMilli())
	handleWebMessage(msg, wv, liveFn, qc, mc, tm, home)

	deadline := time.Now().Add(5 * time.Second)
	for wv.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if wv.Len() == 0 {
		t.Fatal("no reply for today scan_usage")
	}
	var r struct {
		OK   bool `json:"ok"`
		Data struct {
			Tokens scan.TokenTotals `json:"tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(wv.All()[0]), &r); err != nil {
		t.Fatal(err)
	}
	if !r.OK || r.Data.Tokens.Total != 400 {
		t.Fatalf("today scan reply tokens total = %d ok=%v; want 400", r.Data.Tokens.Total, r.OK)
	}
	// 核心断言：今日档扫描完成后缓存被回填为新值（顶部实时卡下一跳即读到它）。
	if g := tm.Get(); g.Tokens.Total != 400 {
		t.Fatalf("today cache NOT backfilled: total = %d; want 400", g.Tokens.Total)
	}
}

// TestRefreshTodayRPCSyncsCache 刷新按钮在非"今日"档时的同步行为：
// refresh_today RPC 触发 today 缓存刷新（后台 ScanToday + 写缓存），
// 使实时卡"今日总 token"保持最新。
func TestRefreshTodayRPCSyncsCache(t *testing.T) {
	home := buildTodayHome(t)
	qc := quota.NewClient(t.TempDir()+"/creds.json", t.TempDir()+"/quota.json")
	mc := modelcfg.Load(t.TempDir() + "/nonexistent.toml")
	tm := today.NewManager(home)

	// 初始缓存 total 200。
	if g := tm.Refresh(); g.Tokens.Total != 200 {
		t.Fatalf("initial cache total = %d; want 200", g.Tokens.Total)
	}

	// 追加一条记录 → 总量应为 400。
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	today := start + (now.UnixMilli()-start)/2
	line := fmt.Sprintf(`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":%d}`+"\n", today)
	f, err := os.OpenFile(filepath.Join(home, "sessions", "s1", "agents", "main", "wire.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// 前端触发 refresh_today（刷新按钮在非今日档时调用）。
	wv := &fakePusher{}
	liveFn := func() liveData { return buildLive(metrics.NewState(), qc, mc, tm, time.Now()) }
	handleWebMessage(`{"id":7,"method":"refresh_today"}`, wv, liveFn, qc, mc, tm, home)

	// 等待异步刷新完成（后台 goroutine 执行 ScanToday + 写缓存）。
	deadline := time.Now().Add(5 * time.Second)
	for tm.Get().Tokens.Total != 400 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if g := tm.Get(); g.Tokens.Total != 400 {
		t.Fatalf("refresh_today did not sync cache: total = %d; want 400", g.Tokens.Total)
	}
	// RPC 立即回复（不阻塞前端），ok=true。
	var r struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(wv.All()[0]), &r); err != nil {
		t.Fatal(err)
	}
	if !r.OK {
		t.Fatalf("refresh_today reply not ok: %s", wv.All()[0])
	}
}
