package today

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kimi-hud/internal/scan"
)

// buildHome 构造含窗口内 usage.record 的临时 kimi 主目录，记录时间基于 now（便于注入时钟），
// 返回 home 路径。文件 mtime 为真实当前时间，恒 ≥ 注入日期的 00:00，mtime 粗筛不受影响。
func buildHome(t *testing.T, now time.Time) string {
	t.Helper()
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	today := now.Add(-time.Minute).UnixMilli()
	line := fmt.Sprintf(
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":%d}`+"\n",
		today)
	if err := os.WriteFile(filepath.Join(agent, "wire.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestRefreshAndGet 刷新后 Get 返回今日用量；Available 置位。
func TestRefreshAndGet(t *testing.T) {
	home := buildHome(t, time.Now())
	m := NewManager(home)
	totals := m.Refresh()
	if !totals.Available {
		t.Fatal("available = false; want true")
	}
	if totals.Date != time.Now().Format("2006-01-02") {
		t.Fatalf("date = %q", totals.Date)
	}
	if totals.Tokens.Total != 200 || totals.Requests != 1 {
		t.Fatalf("totals = %+v, requests = %d", totals.Tokens, totals.Requests)
	}
	// Get 与 Refresh 结果一致（同源）。
	if g := m.Get(); g.Tokens.Total != totals.Tokens.Total {
		t.Fatalf("Get = %+v; want %+v", g.Tokens, totals.Tokens)
	}
}

// TestGetNoCache 未刷新时 Get 返回空值且 Available=false（无残留）。
func TestGetNoCache(t *testing.T) {
	m := NewManager(t.TempDir())
	totals := m.Get()
	if totals.Available || totals.Tokens.Total != 0 {
		t.Fatalf("totals = %+v; want empty", totals)
	}
	if totals.Date == "" {
		t.Fatal("date should be today's key even without cache")
	}
}

// TestCrossDayReset 跨天后未刷新时 Get 返回空值（不泄漏昨日），刷新后新一天生效。
func TestCrossDayReset(t *testing.T) {
	day1 := time.Date(2026, 1, 5, 10, 0, 0, 0, time.Local)
	day2 := day1.Add(24 * time.Hour)
	home := buildHome(t, day1)
	m := NewManager(home)

	m.now = func() time.Time { return day1 }

	totals := m.Refresh()
	if totals.Date != "2026-01-05" || totals.Tokens.Total != 200 {
		t.Fatalf("day1 = %+v; want date 2026-01-05 total 200", totals)
	}

	// 跨天但尚未触发刷新：Get 必须返回空值，而不是昨日 200。
	m.now = func() time.Time { return day2 }
	g := m.Get()
	if g.Date != "2026-01-06" || g.Tokens.Total != 0 {
		t.Fatalf("day2 Get = %+v; want date 2026-01-06 total 0", g)
	}

	// 新一天刷新后：Available 置位、totals 归零（记录时间在昨日窗口外）。
	r := m.Refresh()
	if r.Date != "2026-01-06" || !r.Available || r.Tokens.Total != 0 {
		t.Fatalf("day2 refresh = %+v; want date 2026-01-06 available total 0", r)
	}
}

// TestEverSucceededCrossDay 跨天缓存失效后 EverSucceeded 仍为 true（区分"更新中"
// 与"从未检测到数据"，R2-4 评审修复）；从未成功过的 Manager 返回 false。
func TestEverSucceededCrossDay(t *testing.T) {
	day1 := time.Date(2026, 1, 5, 10, 0, 0, 0, time.Local)
	day2 := day1.Add(24 * time.Hour)
	home := buildHome(t, day1)
	m := NewManager(home)
	m.now = func() time.Time { return day1 }

	if m.EverSucceeded() {
		t.Fatal("从未扫描过时 EverSucceeded 应为 false")
	}
	m.Refresh()
	if !m.EverSucceeded() {
		t.Fatal("成功扫描后 EverSucceeded 应为 true")
	}
	// 跨天缓存失效：Get Available=false 但 EverSucceeded 仍 true。
	m.now = func() time.Time { return day2 }
	g := m.Get()
	if g.Available {
		t.Fatal("跨天 Get.Available 应为 false")
	}
	if !m.EverSucceeded() {
		t.Fatal("跨天后 EverSucceeded 应保持 true（数据完好，只是待刷新）")
	}
}

// TestRefreshKeepsCacheOnError 扫描失败（无 sessions 目录）时保留旧缓存。
func TestRefreshKeepsCacheOnError(t *testing.T) {
	home := buildHome(t, time.Now())
	m := NewManager(home)
	totals := m.Refresh()
	if totals.Tokens.Total != 200 {
		t.Fatalf("initial totals = %+v", totals.Tokens)
	}

	// 删除 sessions 目录模拟数据缺失，再刷新：缓存保留，Get 仍返回原值。
	if err := os.RemoveAll(filepath.Join(home, "sessions")); err != nil {
		t.Fatal(err)
	}
	m.Refresh()
	if g := m.Get(); g.Tokens.Total != 200 {
		t.Fatalf("after error Get = %+v; want cached 200", g.Tokens)
	}
}

// TestUpdateFromScan 详情窗口"今日"档实时扫描后回填缓存：顶部实时卡与"今日"档同源同步。
func TestUpdateFromScan(t *testing.T) {
	now := time.Now()
	m := NewManager(t.TempDir())
	m.now = func() time.Time { return now }

	start := dayStartMs(now)
	rpt := &scan.TodayReport{
		Tokens:   scan.TokenTotals{InputOther: 100, Output: 20, InputCacheRead: 80, Total: 200},
		Requests: 2,
	}
	m.UpdateFromScan(rpt, start)
	g := m.Get()
	if !g.Available || g.Tokens.Total != 200 || g.Requests != 2 {
		t.Fatalf("after update = %+v; want available total 200 requests 2", g)
	}
}

// TestUpdateFromScanRejectsOtherWindow 非本日 00:00 起点的扫描不污染缓存
// （跨天或其它时间窗口的结果一律拒绝）。
func TestUpdateFromScanRejectsOtherWindow(t *testing.T) {
	now := time.Now()
	m := NewManager(t.TempDir())
	m.now = func() time.Time { return now }

	// 先写入一个已知缓存，再用非法窗口起点回填，缓存应保持不变。
	rpt := &scan.TodayReport{Tokens: scan.TokenTotals{Total: 100}, Requests: 1}
	m.UpdateFromScan(rpt, dayStartMs(now))
	before := m.Get()

	rpt2 := &scan.TodayReport{Tokens: scan.TokenTotals{Total: 999}, Requests: 9}
	// 昨天 00:00 的起点（非法窗口）。
	m.UpdateFromScan(rpt2, dayStartMs(now.Add(-24*time.Hour)))
	if g := m.Get(); g.Tokens.Total != before.Tokens.Total || g.Requests != before.Requests {
		t.Fatalf("cache polluted by other window: got %+v want %+v", g, before)
	}
}
