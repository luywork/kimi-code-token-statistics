// Package display 提供托盘菜单文本与图标颜色的共享展示层，
// 供 cmd/kimi-hud 与 cmd/demo 复用，避免逻辑复制漂移。
package display

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"kimi-hud/internal/metrics"
	"kimi-hud/internal/modelcfg"
	"kimi-hud/internal/pricing"
	"kimi-hud/internal/quota"
	"kimi-hud/internal/today"
	"kimi-hud/internal/tray"
)

// 菜单命令 ID。
const (
	CmdRefresh    = 2001
	CmdExit       = 2002
	CmdOpenDetail = 2003
)

// BuildMenu 组装托盘菜单（对齐 kimi-code-hud 各 render segment）。
// owner-draw 样式：标题/分段/指标/动作四类，指标带语义色圆点，配额带原生进度条。
// detailEnabled 为 WebView2 Runtime 可用性（缺失时"查看详细用量"灰置，方案 §8 降级）。
// everSucceeded 区分今日用量"跨天待刷新（更新中）"与"从未检测到数据"（R2-4 评审修复）。
func BuildMenu(st *metrics.State, qc *quota.Client, mc *modelcfg.Config, todayTotals today.Totals, everSucceeded, detailEnabled bool) []tray.MenuItem {
	var items []tray.MenuItem
	add := func(text string) {
		items = append(items, tray.MenuItem{Text: text, Disabled: true})
	}

	sum := st.Summarize(time.Now().UnixMilli())
	modelAlias := sum.ModelAlias

	// 1. 品牌标题行。
	items = append(items, tray.MenuItem{Text: "Kimi Code HUD", Kind: tray.KindTitle, Disabled: true})

	// 2. 生成速度。
	items = append(items, tray.MenuItem{
		Text:     "生成速度  " + formatSpeed(sum),
		Kind:     tray.KindValue,
		Color:    0x2563eb,
		Disabled: true,
	})

	// 3. 缓存命中率。
	if rate, ok := st.CacheHitRate(); ok {
		items = append(items, tray.MenuItem{
			Text:     fmt.Sprintf("缓存命中  Cache %d%%", int(rate*100+0.5)),
			Kind:     tray.KindValue,
			Color:    0x047857,
			Disabled: true,
		})
	} else {
		items = append(items, tray.MenuItem{Text: "缓存命中  Cache --", Kind: tray.KindValue, Disabled: true})
	}

	// 4. 今日用量（全部模型，对齐方案 §5.1）：统计全部 provider，与仅对
	// 托管模型显示的"订阅额度"段区分；跨天由 today 缓存 date key 自动重置。
	items = append(items, tray.MenuItem{Separator: true})
	items = append(items, tray.MenuItem{Text: "今日用量（全部模型）", Kind: tray.KindHeading, Disabled: true})
	if todayTotals.Available {
		total := todayTotals.Tokens.Total
		suffix := ""
		if todayTotals.LimitsReached {
			suffix = " *"
		}
		items = append(items, tray.MenuItem{
			Text:     "token 总计 " + compactNum(total) + suffix,
			Kind:     tray.KindValue,
			Color:    0x2563eb,
			Disabled: true,
		})
		items = append(items, tray.MenuItem{
			Text: fmt.Sprintf("输入 %s · 输出 %s · 缓存读 %s · 缓存写 %s",
				compactNum(todayTotals.Tokens.InputOther),
				compactNum(todayTotals.Tokens.Output),
				compactNum(todayTotals.Tokens.InputCacheRead),
				compactNum(todayTotals.Tokens.InputCacheCreation)),
			Kind:     tray.KindValue,
			Disabled: true,
		})
		// 成本：已定价记录的估算费用（用户配置 config.toml [pricing] 后才有值）。
		if todayTotals.PricedCostCny != "" {
			items = append(items, tray.MenuItem{
				Text:     "费用(元) " + todayTotals.PricedCostCny,
				Kind:     tray.KindValue,
				Color:    0x047857,
				Sub:      "估算",
				Disabled: true,
			})
		}
		if todayTotals.LimitsReached {
			add("⚠ 数据不完整：已超今日扫描上限")
		}
	} else {
		// 区分"从未扫描到数据"（程序异常/数据目录缺失）与"跨天待下次刷新"
		// （数据完好，只是新一天还没扫，≤5min 内自动更新）——后者不应误导为故障。
		if everSucceeded {
			add("更新中…（每 5 分钟自动刷新）")
		} else {
			add("暂不可用（未检测到 Kimi Code 数据）")
		}
	}

	// 5. 订阅额度：仅托管 provider 显示。
	if mc.IsManaged(modelAlias) {
		items = append(items, tray.MenuItem{Separator: true})
		items = append(items, tray.MenuItem{Text: "订阅额度 (managed:kimi-code)", Kind: tray.KindHeading, Disabled: true})
		q := qc.Get(time.Now())
		last := qc.Last()
		status, _ := qc.CredentialsState()
		switch {
		case status != quota.CredentialsOK:
			// 未登录/凭据缺失（对齐方案 §3.8）。
			add("未登录")
		case q != nil:
			for _, w := range q.Windows {
				items = append(items, quotaWindowItem(w))
			}
			if !last.SuccessAt.IsZero() {
				add("更新于 " + last.SuccessAt.Format("15:04:05") + " · 每 5 分钟自动刷新")
			}
			if last.Unauthorized {
				add("⚠ 登录态过期：运行 kimi-code 后自动恢复")
			}
		default:
			if last.Unauthorized {
				add("⚠ 登录态过期：运行 kimi-code 后自动恢复")
			} else {
				add("正在自动刷新配额…")
			}
		}
		// 订阅月费（用户配置 [pricing.subscription]，仅展示）。
		if subFee := pricing.SubscriptionMonthlyCny(); subFee != "" {
			items = append(items, tray.MenuItem{
				Text:     "订阅成本 ¥" + subFee + "/月",
				Kind:     tray.KindValue,
				Color:    0x047857,
				Disabled: true,
			})
		}
	} else if modelAlias != "" {
		items = append(items, tray.MenuItem{Separator: true})
		add("第三方 provider 不显示订阅额度")
	}

	// 6. 动作项。
	items = append(items, tray.MenuItem{Separator: true})
	if detailEnabled {
		items = append(items, tray.MenuItem{Text: "查看详细用量…", Kind: tray.KindAction, ID: CmdOpenDetail, Color: 0x2563eb})
	} else {
		items = append(items, tray.MenuItem{Text: "查看详细用量（需 WebView2 Runtime）", Kind: tray.KindAction, Disabled: true})
	}
	items = append(items, tray.MenuItem{Text: "刷新配额", Kind: tray.KindAction, ID: CmdRefresh})
	items = append(items, tray.MenuItem{Text: "退出", Kind: tray.KindAction, ID: CmdExit, Color: 0xd33b3b})
	return items
}

// quotaWindowItem 配额窗口：原生进度条 + 语义色 + 倒计时副文本。
func quotaWindowItem(w quota.Window) tray.MenuItem {
	frac := 0.0
	if w.Limit > 0 {
		frac = w.Used / w.Limit
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
	}
	return tray.MenuItem{
		Text:     fmt.Sprintf("%s  %d%%", padRight(w.Label, 3), int(frac*100+0.5)),
		Kind:     tray.KindValue,
		Color:    quotaColor(frac),
		Sub:      quota.Countdown(w.ResetTime, time.Now()),
		Progress: &frac,
		Disabled: true,
	}
}

// quotaColor 配额使用率分级色：≥85% 红、≥60% 黄、否则绿（对齐 IconColor 口径）。
func quotaColor(frac float64) uint32 {
	switch {
	case frac >= 0.85:
		return 0xe53935
	case frac >= 0.6:
		return 0xfbc02d
	}
	return 0x43a047
}

// formatSpeed 对齐 kimi-code-hud speedSegment：TPS 存在且回合进行中时
// 同时显示「t/s」与「gen」，舰队判定含 swarmMode（swarm 缩到只剩一个
// 子 agent 仍按舰队样式）。
func formatSpeed(s metrics.Summary) string {
	liveSubagents := s.ActiveAgents
	if s.MainActive {
		liveSubagents--
	}
	if s.TPS > 0 {
		multi := s.TPSAgents > 1 || (s.SwarmMode && liveSubagents >= 1 && s.TPSAgents >= 1)
		var base string
		if multi {
			base = fmt.Sprintf("⚡ %s t/s (%d agents @%s)", trim1(s.TPSTotal), s.TPSAgents, trim1(s.TPS))
		} else {
			base = "⚡ " + trim1(s.TPS) + " t/s"
		}
		if s.TPSStale {
			base += " (stale)"
		}
		if s.TurnStartedAt > 0 {
			return base + " · gen " + formatElapsed(time.Since(time.UnixMilli(s.TurnStartedAt)))
		}
		return base + tfttSuffix(s)
	}
	if s.TurnStartedAt > 0 {
		multi := s.ActiveAgents > 1 || (s.SwarmMode && liveSubagents >= 1)
		if multi {
			return fmt.Sprintf("⚡ gen %s (%d agents)", formatElapsed(time.Since(time.UnixMilli(s.TurnStartedAt))), s.ActiveAgents)
		}
		return "⚡ gen " + formatElapsed(time.Since(time.UnixMilli(s.TurnStartedAt)))
	}
	return "⚡ 等待生成…"
}

func tfttSuffix(s metrics.Summary) string {
	if s.HasTTFT && s.TTFTMs > 0 {
		return " · TTFT " + formatTTFT(s.TTFTMs)
	}
	return ""
}

// IconColor 绿/黄/红：优先按配额余量，无配额则按生成活跃度。
// 生成中用蓝色（0x1e88e5），与「配额健康=绿」区分（方案 §3.6.2 的主动改进）。
func IconColor(st *metrics.State, qc *quota.Client) uint32 {
	if q := qc.Get(time.Now()); q != nil {
		if w, ok := q.BestWindow(); ok {
			frac := 0.0
			if w.Limit > 0 {
				frac = w.Used / w.Limit
			}
			switch {
			case frac >= 0.85:
				return 0xe53935 // 红
			case frac >= 0.6:
				return 0xfbc02d // 黄
			}
			return 0x43a047 // 绿
		}
	}
	if sum := st.Summarize(time.Now().UnixMilli()); sum.TPS > 0 || sum.TurnStartedAt > 0 {
		return 0x1e88e5 // 蓝：生成中
	}
	return 0x9e9e9e // 灰：空闲
}

// trim1 对齐参考 Math.round 的整数化：恒为整数。
func trim1(v float64) string {
	return fmt.Sprintf("%.0f", math.Round(v))
}

func formatTTFT(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	return fmt.Sprintf("%dms", int(math.Round(ms)))
}

func formatElapsed(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 0 {
		secs = 0
	}
	switch {
	case secs >= 86400:
		return fmt.Sprintf("%dd%dh", secs/86400, (secs%86400)/3600)
	case secs >= 3600:
		return fmt.Sprintf("%dh%dm", secs/3600, (secs%3600)/60)
	case secs >= 60:
		return fmt.Sprintf("%dm%ds", secs/60, secs%60)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// compactNum 中文紧凑数字：≥1e8 用「亿」、≥1e4 用「万」，统一 1 位小数（去尾零）。
func compactNum(v uint64) string {
	var unit string
	var f float64
	switch {
	case v >= 1_0000_0000:
		unit, f = "亿", float64(v)/1e8
	case v >= 1_0000:
		unit, f = "万", float64(v)/1e4
	default:
		return strconv.FormatUint(v, 10)
	}
	s := strconv.FormatFloat(f, 'f', 1, 64)
	s = strings.TrimSuffix(strings.TrimSuffix(s, "0"), ".")
	return s + unit
}
