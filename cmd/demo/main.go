// demo 是一个 headless 集成验证工具：读取真实 Kimi Code sessions 目录，
// 增量灌入 wire 日志，打印指标汇总与模拟的托盘菜单文本。
// 仅用于开发验证，不随正式构建发布。
// 菜单/图标展示逻辑统一来自 internal/display，与主程序无复制漂移。
package main

import (
	"fmt"
	"time"

	"kimi-hud/internal/display"
	"kimi-hud/internal/modelcfg"
	"kimi-hud/internal/paths"
	"kimi-hud/internal/quota"
	"kimi-hud/internal/session"
	"kimi-hud/internal/state"
	"kimi-hud/internal/today"
)

func main() {
	p := paths.New()
	_ = p.EnsureDirs()

	store := state.NewStore(p.StateDir)
	sess := session.New(p.SessionsRoot, store)

	qc := quota.NewClient(p.Credentials, p.QuotaCache)
	mc := modelcfg.Load(p.ConfigToml)

	// 模拟 40 次轮询（20 秒），让增量读取追上文件尾部。
	for i := 0; i < 40; i++ {
		sess.Poll(time.Now())
		time.Sleep(500 * time.Millisecond)
	}
	st := sess.State()

	fmt.Println("== 当前会话 ==")
	if d := sess.SessionDir(); d == "" {
		fmt.Println("未定位到会话")
	} else {
		fmt.Println("session:", sess.SessionID())
		fmt.Println("dir:", d)
	}

	sum := st.Summarize(time.Now().UnixMilli())
	fmt.Printf("\n== 指标汇总 ==\n")
	fmt.Printf("modelAlias:   %q\n", sum.ModelAlias)
	fmt.Printf("swarmMode:    %v\n", sum.SwarmMode)
	fmt.Printf("TPS:          %.1f (has=%v stale=%v total=%.1f agents=%d)\n", sum.TPS, sum.HasTPS, sum.TPSStale, sum.TPSTotal, sum.TPSAgents)
	fmt.Printf("TTFT:         %.0f ms (has=%v)\n", sum.TTFTMs, sum.HasTTFT)
	fmt.Printf("gen计时锚点:   %d (TurnStartedAt, 0=无)\n", sum.TurnStartedAt)
	fmt.Printf("activeAgents: %d\n", sum.ActiveAgents)
	rate, has := st.CacheHitRate()
	fmt.Printf("Cache:        %v (has=%v)\n", rate, has)

	// 配额：真实拉取一次（验证 Go 配额客户端）。
	if err := qc.Refresh(time.Now()); err != nil {
		fmt.Printf("\n== 配额 (真实拉取失败) ==\n  %v\n", err)
	} else if q := qc.Get(time.Now()); q != nil {
		fmt.Printf("\n== 配额 (真实拉取) ==\n")
		for _, w := range q.Windows {
			frac := 0.0
			if w.Limit > 0 {
				frac = w.Used / w.Limit
			}
			fmt.Printf("  %s %s %.1f%%  reset=%s\n", w.Label, quota.Bar(frac), frac*100, w.ResetTime.Format("01-02 15:04"))
		}
	}

	// 模拟托盘菜单文本。
	fmt.Printf("\n== 模拟托盘菜单 ==\n")
	tm := today.NewManager(p.KimiHome)
	tm.Refresh()
	items := display.BuildMenu(st, qc, mc, tm.Get(), tm.EverSucceeded(), true)
	for _, it := range items {
		if it.Separator {
			fmt.Println("  ----")
			continue
		}
		fmt.Printf("  %s\n", it.Text)
	}

	// 模拟图标颜色。
	fmt.Printf("\n== 图标颜色 ==\n")
	c := display.IconColor(st, qc)
	fmt.Printf("0x%06X\n", c)
}
