package display

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kimi-hud/internal/metrics"
	"kimi-hud/internal/modelcfg"
	"kimi-hud/internal/quota"
	"kimi-hud/internal/today"
	"kimi-hud/internal/tray"
)

func TestCompactNum(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{9999, "9999"},
		{10000, "1万"},
		{12345, "1.2万"},
		{123456, "12.3万"},
		{1234567, "123.5万"},
		{10000000, "1000万"},
		{123456789, "1.2亿"},
		{100000000, "1亿"},
		{2000000000, "20亿"},
	}
	for _, c := range cases {
		if got := compactNum(c.in); got != c.want {
			t.Errorf("compactNum(%d) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestBuildMenuTodaySection 今日用量段出现在菜单中，含四维缩写与超限后缀。
func TestBuildMenuTodaySection(t *testing.T) {
	todayTotals := today.Totals{
		Available:     true,
		LimitsReached: true,
	}
	todayTotals.Tokens.InputOther = 12345
	todayTotals.Tokens.Output = 23456
	todayTotals.Tokens.InputCacheRead = 34567
	todayTotals.Tokens.InputCacheCreation = 45678
	todayTotals.Tokens.Total = 116046

	items := BuildMenu(&metrics.State{}, nil, &modelcfg.Config{}, todayTotals, true, true)
	var texts []string
	for _, it := range items {
		if !it.Separator {
			texts = append(texts, it.Text)
		}
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{
		"今日用量（全部模型）",
		"token 总计 11.6万 *",
		"输入 1.2万 · 输出 2.3万 · 缓存读 3.5万 · 缓存写 4.6万",
		"⚠ 数据不完整：已超今日扫描上限",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("menu missing %q\nmenu:\n%s", want, joined)
		}
	}
}

// TestBuildMenuTodayUnavailable 无数据时显示占位而非 0。
func TestBuildMenuTodayUnavailable(t *testing.T) {
	items := BuildMenu(&metrics.State{}, nil, &modelcfg.Config{}, today.Totals{}, false, true)
	var texts []string
	for _, it := range items {
		if !it.Separator {
			texts = append(texts, it.Text)
		}
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "暂不可用（未检测到 Kimi Code 数据）") {
		t.Errorf("menu missing unavailable placeholder:\n%s", joined)
	}
	if strings.Contains(joined, "token 总计 0") {
		t.Errorf("menu should not show 0 when unavailable:\n%s", joined)
	}
}

// TestBuildMenuTodayUpdating 曾成功扫过但跨天缓存失效时，显示"更新中"而非"未检测到数据"。
func TestBuildMenuTodayUpdating(t *testing.T) {
	items := BuildMenu(&metrics.State{}, nil, &modelcfg.Config{}, today.Totals{}, true, true)
	var texts []string
	for _, it := range items {
		if !it.Separator {
			texts = append(texts, it.Text)
		}
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "更新中…（每 5 分钟自动刷新）") {
		t.Errorf("menu missing updating placeholder:\n%s", joined)
	}
	if strings.Contains(joined, "未检测到 Kimi Code 数据") {
		t.Errorf("menu should not say unavailable when ever succeeded:\n%s", joined)
	}
}

// TestBuildMenuCostRow 已定价费用行出现在今日用量段；无定价时不显示。
func TestBuildMenuCostRow(t *testing.T) {
	tt := today.Totals{Available: true}
	tt.Tokens.Total = 1000
	tt.PricedCostCny = "0.12"

	items := BuildMenu(&metrics.State{}, nil, &modelcfg.Config{}, tt, true, true)
	var joined string
	var costItem *tray.MenuItem
	for i := range items {
		it := &items[i]
		if it.Separator {
			continue
		}
		joined += it.Text + "\n"
		if it.Text == "费用(元) 0.12" {
			costItem = it
		}
	}
	if costItem == nil {
		t.Fatalf("menu missing cost row:\n%s", joined)
	}
	if costItem.Sub != "估算" {
		t.Errorf("cost row sub = %q, want 估算", costItem.Sub)
	}

	// 无定价费用时不应出现费用行。
	tt.PricedCostCny = ""
	items = BuildMenu(&metrics.State{}, nil, &modelcfg.Config{}, tt, true, true)
	joined = ""
	for _, it := range items {
		if !it.Separator {
			joined += it.Text + "\n"
		}
	}
	if strings.Contains(joined, "费用(元)") {
		t.Errorf("menu should not show cost row when unpriced:\n%s", joined)
	}
}

// TestQuotaWindowItem 配额窗口项带进度条与分级色。
func TestQuotaWindowItem(t *testing.T) {
	it := quotaWindowItem(quota.Window{Label: "5h", Used: 50, Limit: 100, ResetTime: time.Now().Add(time.Hour)})
	if it.Progress == nil || *it.Progress != 0.5 {
		t.Fatalf("progress = %v, want 0.5", it.Progress)
	}
	if it.Color != 0x43a047 {
		t.Fatalf("color = 0x%06X, want green 0x43a047", it.Color)
	}
	if it.Sub == "" {
		t.Fatal("countdown sub should be set")
	}
}

// TestQuotaWindowItemServerRatio 服务端 used_ratio 优先于 Used/Limit 推导
//（P2-1 评审修复回归：同一窗口推导 50%、服务端 90% → 应按 90% 显示红色）。
func TestQuotaWindowItemServerRatio(t *testing.T) {
	w := quota.Window{Label: "5h", Used: 50, Limit: 100, UsedRatio: 0.9, ResetTime: time.Now().Add(time.Hour)}
	it := quotaWindowItem(w)
	if it.Progress == nil || *it.Progress != 0.9 {
		t.Fatalf("progress = %v, want 0.9 (server ratio)", it.Progress)
	}
	if it.Color != 0xe53935 {
		t.Fatalf("color = 0x%06X, want red 0xe53935 (90%% >= 85%%)", it.Color)
	}
}

// TestIconColorServerRatio IconColor 分级走 Ratio()（P2-1 评审修复回归）：
// BestWindow 选中的窗口在 IconColor 内部也须用服务端比率而非 Used/Limit 推导。
// 缓存经磁盘文件注入（走生产 loadDiskCache 读路径，quota 包私有字段无需暴露）。
func TestIconColorServerRatio(t *testing.T) {
	dir := t.TempDir()
	qc := quota.NewClient(filepath.Join(dir, "missing.json"), filepath.Join(dir, "quota.json"))
	writeQuotaCache(t, filepath.Join(dir, "quota.json"), `[{"label":"5h","used":50,"limit":100,"usedRatio":0.9}]`)
	if got := IconColor(&metrics.State{}, qc); got != 0xe53935 {
		t.Fatalf("IconColor = 0x%06X, want red 0xe53935 (server ratio 90%%)", got)
	}
	// 对照：无服务端比率时走推导，50% → 绿。
	qc = quota.NewClient(filepath.Join(dir, "missing.json"), filepath.Join(dir, "quota2.json"))
	writeQuotaCache(t, filepath.Join(dir, "quota2.json"), `[{"label":"5h","used":50,"limit":100}]`)
	if got := IconColor(&metrics.State{}, qc); got != 0x43a047 {
		t.Fatalf("IconColor = 0x%06X, want green 0x43a047 (derived 50%%)", got)
	}
}

// writeQuotaCache 写一份 TTL 内有效的 quota 磁盘缓存（fetchedAt=now）。
func writeQuotaCache(t *testing.T, path, windowsJSON string) {
	t.Helper()
	payload := `{"fetchedAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","windows":` + windowsJSON + `}`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
}

var _ = tray.MenuItem{} // 保持 tray 导入（BuildMenu 签名使用）
