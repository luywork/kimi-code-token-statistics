// kimi-hud 是 Kimi Code token 统计桌面服务：系统托盘常驻，显示生成速度、
// 缓存命中率与订阅额度。无主窗口，纯 Win32 托盘，低内存。
// 托盘"查看详细用量…"按需打开 WebView2 详情窗口（M2），关窗回托盘。
package main

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"kimi-hud/internal/display"
	"kimi-hud/internal/modelcfg"
	"kimi-hud/internal/paths"
	"kimi-hud/internal/pricing"
	"kimi-hud/internal/quota"
	"kimi-hud/internal/session"
	"kimi-hud/internal/state"
	"kimi-hud/internal/today"
	"kimi-hud/internal/tray"
	"kimi-hud/internal/webview"
)

func main() {
	// 锁定 OS 线程：WebView2 COM apartment 绑定创建线程，托盘消息循环与
	// 详情窗口共用同一 UI 线程（方案 §6.2 线程归属）。
	runtime.LockOSThread()

	// 单实例控制。
	if handle, alreadyRunning := acquireSingleInstance(); alreadyRunning {
		if len(os.Args) > 1 && os.Args[1] == "--quit" {
			found := tray.FindHudWindow()
			fmt.Printf("FindHudWindow = 0x%X\n", found)
			ok := tray.RequestQuit()
			fmt.Println("RequestQuit =", ok)
			return
		}
		fmt.Println("kimi-hud 已在运行")
		return
	} else {
		defer windows.CloseHandle(handle)
	}

	p := paths.New()
	if err := p.EnsureDirs(); err != nil {
		fmt.Fprintln(os.Stderr, "初始化目录失败:", err)
		os.Exit(1)
	}

	store := state.NewStore(p.StateDir)
	sess := session.New(p.SessionsRoot, store)

	qc := quota.NewClient(p.Credentials, p.QuotaCache)
	mc := modelcfg.Load(p.ConfigToml)

	// 用户成本配置：kimi-hud 自己的 config.toml [pricing]（模型单价/订阅月费），
	// 随文件变更热加载。放在 HudConfig（~/.kimi-code-hud/config.toml）而非 Kimi
	// Code 的 ~/.kimi-code/config.toml——不碰 Kimi 的配置文件（其更新可能覆盖我们的段）。
	pl := pricing.NewOverrideLoader(p.HudConfig)

	// 长期 API key（同文件 [quota].api_key）：配置后订阅额度查询与 kimi-code CLI
	// 运行态解耦（access_token 15min 过期靠 CLI 懒刷新，CLI 不运行即 401；
	// sk-kimi key 长期有效，同 /usages 端点直接放行，对齐 cc-switch query_kimi）。
	qc.SetAPIKeyLoader(quota.NewAPIKeyLoader(p.HudConfig))

	// 详情窗口（WebView2 三层壳）：Runtime 缺失时禁用菜单项（降级）。
	detailWv := webview.New()
	detailEnabled := webview.RuntimeAvailable()

	// 保护 sess 内状态（写：pollLoop；读：buildMenu/heartbeat）。
	var stMu sync.Mutex

	// 后台轮询：wire 增量 + 配额刷新。
	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pollLoop(&stMu, sess, qc, mc, pl, stopCh)
	}()

	// 今日用量：独立 goroutine 低频刷新（5min），与配额拉取错峰（方案 §5.2.3）。
	// 传 KimiHome（~/.kimi-code），ScanToday 内部按 home/sessions 定位数据目录。
	todayMgr := today.NewManager(p.KimiHome)
	wg.Add(1)
	go func() {
		defer wg.Done()
		todayMgr.Run(stopCh)
	}()

	var t *tray.Tray
	// 托盘图标直接从 exe 自身加载嵌入的图标资源（LoadImage 支持 .exe），
	// 与任务管理器/资源管理器里显示的进程图标同一资源，保证两处完全一致，
	// 不再依赖外部 ~/.kimi-code-hud/tray.ico 文件。
	exePath, err := os.Executable()
	if err != nil {
		exePath = ""
	}
	// 详情窗口回调接线（WebMessage 桥接 + 首推实时数据）。
	detailWv.OnMessage = func(json string) {
		// liveFn 在 stMu 锁内组装实时快照（R1：metrics.State 无内部锁，锁外读会与
		// pollLoop 锁内写并发 → concurrent map read and write fatal）。
		liveFn := func() liveData {
			stMu.Lock()
			defer stMu.Unlock()
			return buildLive(sess.State(), qc, mc, todayMgr, time.Now())
		}
		handleWebMessage(json, detailWv, liveFn, qc, mc, todayMgr, p.KimiHome)
	}
	detailWv.OnLoaded = func() {
		// 页面导航完成：推一次实时快照（含今日用量）。
		stMu.Lock()
		d := buildLive(sess.State(), qc, mc, todayMgr, time.Now())
		stMu.Unlock()
		pushLive(detailWv, d)
	}

	t = tray.New(&tray.Handler{
		BuildMenu: func() []tray.MenuItem {
			stMu.Lock()
			defer stMu.Unlock()
			// today 缓存有自锁，独立于 stMu（today 刷新在自身 goroutine，不写 sess 状态）。
			return display.BuildMenu(sess.State(), qc, mc, todayMgr.Get(), todayMgr.EverSucceeded(), detailEnabled)
		},
		OnCommand: func(id int) {
			switch id {
			case display.CmdOpenDetail:
				if detailEnabled {
					if err := detailWv.Open(); err != nil {
						fmt.Fprintln(os.Stderr, "打开详情窗口失败:", err)
					}
				}
			case display.CmdRefresh:
				qc.RefreshNow()
			case display.CmdExit:
				// 只停托盘，close(stopCh) 统一在 Run 返回后执行一次，避免二次 close panic。
				t.Stop()
			}
		},
	}, exePath)

	// 心跳：更新托盘图标颜色 + 向详情窗口推送实时数据（窗口打开期间才推，关窗零开销）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		heartbeat(t, sess, &stMu, qc, detailWv, todayMgr, mc, stopCh)
	}()

	if err := t.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "托盘运行失败:", err)
	}
	close(stopCh)
	wg.Wait()
}

// pollLoop 周期读取 wire 日志并刷新配额。
func pollLoop(stMu *sync.Mutex, sess *session.Manager, qc *quota.Client, mc *modelcfg.Config, pl *pricing.OverrideLoader, stop <-chan struct{}) {
	wireTick := time.NewTicker(500 * time.Millisecond)
	defer wireTick.Stop()

	quotaTick := time.NewTicker(5 * time.Second)
	defer quotaTick.Stop()

	for {
		select {
		case <-stop:
			return
		case <-wireTick.C:
			// Relocate 拆分为锁外扫描 + 锁内应用：全目录遍历（历史会话多/慢盘
			// 时可达百毫秒级）在锁外执行，避免阻塞同锁上的 BuildMenu/heartbeat
			//（R2-1 评审修复）。ApplyPending 仅切换已定位的会话，含少量 IO。
			sess.RelocateScan()
			stMu.Lock()
			sess.ApplyPending()
			sess.Poll(time.Now())
			stMu.Unlock()
		case <-quotaTick.C:
			// config.toml 变更时重读（方案 §3.4 任务③ + 用户成本覆盖热加载）。
			mc.ReloadIfChanged()
			pl.ReloadIfChanged()
			// [quota].api_key 热加载（P1-1 评审修复）：改 key 免重启。热加载
			// ≤5s；额度恢复还需等配额缓存过期（TTL 5min）或手动「刷新配额」
			// ——缓存 TTL 内 Get 命中不触发网络请求（第二轮评审新2 更正表述）。
			qc.APIKeyReloadIfChanged()
			if qc.ShouldRefresh() {
				qc.RefreshNow()
			}
		}
	}
}

// heartbeat 更新托盘图标颜色；详情窗口打开时同步推送实时数据（D3c WebMessage，
// 复用心跳 500ms 节奏，但 Push 只在窗口打开期间发生，关窗零开销）。
// 对齐方案 §5.5"值变化才推"防抖（R5）：仅当实时指标或今日用量变化时才序列化推送，
// 空闲（无新请求）时零序列化开销——比无条件每 500ms push 更贴合方案约定。
func heartbeat(t *tray.Tray, sess *session.Manager, stMu *sync.Mutex, qc *quota.Client,
	wv *webview.Window, tm *today.Manager, mc *modelcfg.Config, stop <-chan struct{}) {
	var last uint32
	var lastLive liveData
	hasLast := false
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			stMu.Lock()
			c := display.IconColor(sess.State(), qc)
			stMu.Unlock()
			if c != last {
				t.SetIconColor(c)
				last = c
			}
			if wv.IsOpen() {
				stMu.Lock()
				d := buildLive(sess.State(), qc, mc, tm, time.Now())
				stMu.Unlock()
				if !hasLast || liveChanged(lastLive, d) {
					pushLive(wv, d)
					lastLive = d
					hasLast = true
				}
			}
		}
	}
}

// liveChanged 判断实时快照是否发生"值得推送"的变化（防抖，R5）。
// 参与比较：TPS/TTFT/Cache/今日总 token/agents 数/配额窗口的 Used/UsedRatio/
// Remaining/Limit/Label/ResetTime 与认证状态——均为前端实时卡展示项。Today
// 四维明细不单独比较：Total 即四维之和（单调累计，明细变则 Total 必变）；
// Swarm/ModelAlias 前端不渲染，无需比较。Remaining 是渲染项（配额卡"余 N"，
// P3-4 修复后前端优先消费服务端值，第二轮评审新1），bonus/overflow 场景它与
// used/limit 解耦变化，必须独立参与比较。
func liveChanged(a, b liveData) bool {
	if !numEq(a.TPS, b.TPS) || !numEq(a.TTFTMs, b.TTFTMs) || !numEq(a.Cache, b.Cache) {
		return true
	}
	if a.Agents != b.Agents {
		return true
	}
	at, bt := a.Today, b.Today
	if (at == nil) != (bt == nil) {
		return true
	}
	if at != nil && at.Tokens.Total != bt.Tokens.Total {
		return true
	}
	aq, bq := a.Quota, b.Quota
	if (aq == nil) != (bq == nil) {
		return true
	}
	if aq != nil {
		if aq.Status != bq.Status || len(aq.Windows) != len(bq.Windows) {
			return true
		}
	}
	if aq != nil {
		for i := range aq.Windows {
			aw, bw := &aq.Windows[i], &bq.Windows[i]
			// UsedRatio/Remaining 参与比较（P2-2/第二轮新1 评审修复）：服务端
			// 比率回填、bonus/overflow 的剩余量变化都可能独立于 used/limit，
			// 防抖不能漏推（前端"余 N"与百分比直接消费这两个字段）。
			if aw.Used != bw.Used || aw.Label != bw.Label || aw.Limit != bw.Limit ||
				aw.UsedRatio != bw.UsedRatio || aw.Remaining != bw.Remaining {
				return true
			}
			// ResetTime 是前端倒计时基准（data-reset）：窗口轮转后翻新，空闲用户
			// Used 0→0 不推送会让倒计时停在 ~reset（R3-新6）。Equal 按时间点比较，
			// 不受 API 时间戳时区写法差异影响；同一窗口内该值恒定，不影响推送频率。
			if !aw.ResetTime.Equal(bw.ResetTime) {
				return true
			}
		}
	}
	return false
}

func numEq(a, b *float64) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	return *a == *b
}

// acquireSingleInstance 命名 Mutex 单实例；返回句柄与是否已存在。
func acquireSingleInstance() (windows.Handle, bool) {
	name, _ := windows.UTF16PtrFromString("Local\\KimiTokenHudSingleInstance")
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		return 0, true
	}
	return h, false
}
