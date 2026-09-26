package display

import (
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

var _ = tray.MenuItem{} // 保持 tray 导入（BuildMenu 签名使用）
