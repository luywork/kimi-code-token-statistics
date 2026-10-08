package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"kimi-hud/internal/metrics"
	"kimi-hud/internal/modelcfg"
	"kimi-hud/internal/quota"
	"kimi-hud/internal/scan"
	"kimi-hud/internal/today"
)

// logBridge 追加桥接诊断日志到 %USERPROFILE%\.kimi-code-hud\webview.log
// （与 internal/webview 的日志同文件，便于一次性查看通道两侧）。
func webviewLog(format string, args ...any) {
	dir := filepath.Join(os.Getenv("USERPROFILE"), ".kimi-code-hud")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "webview.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(f, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

// liveData 实时推送负载（前端 internal/webview/frontend.html 的 renderLive 消费）。
type liveData struct {
	TPS        *float64      `json:"tps"`
	TTFTMs     *float64      `json:"ttft"`
	Cache      *float64      `json:"cacheRate"`
	Agents     int           `json:"agents"`
	Today      *today.Totals `json:"today"`
	Quota      *liveQuota    `json:"quota"`
	Swarm      bool          `json:"swarm"`
	ModelAlias string        `json:"modelAlias"`
}

type liveQuota struct {
	Windows []liveWindow `json:"windows"`
	Status  string       `json:"status"`
}

// liveWindow 配额窗口实时快照：除已用/上限外，附带剩余量与重置时间，
// 供前端渲染进度条/剩余量/重置倒计时（对齐托盘菜单 formatWindow 的信息维度）。
// UsedRatio 服务端精确比率（-1=未知），前端优先用它渲染百分比。
type liveWindow struct {
	Label     string    `json:"label"`
	Used      float64   `json:"used"`
	Limit     float64   `json:"limit"`
	Remaining float64   `json:"remaining"`
	ResetTime time.Time `json:"resetTime"`
	UsedRatio float64   `json:"usedRatio"`
}

// buildLive 组装实时数据快照（复用 metrics/quota/today 现有能力）。
// 注意：st 来自 metrics.State，内部无锁，调用方必须在 stMu 锁内调用本函数
// （对齐 display.BuildMenu 的读模式，R1 数据竞争修复）。
func buildLive(st *metrics.State, qc *quota.Client, mc *modelcfg.Config, tm *today.Manager, now time.Time) liveData {
	sum := st.Summarize(now.UnixMilli())
	data := liveData{Swarm: sum.SwarmMode, ModelAlias: sum.ModelAlias, Agents: sum.ActiveAgents}
	if sum.HasTPS {
		v := sum.TPS
		data.TPS = &v
	}
	if sum.HasTTFT {
		v := sum.TTFTMs
		data.TTFTMs = &v
	}
	if rate, ok := st.CacheHitRate(); ok {
		v := rate
		data.Cache = &v
	}
	t := tm.Get()
	if t.Available {
		data.Today = &t
	}
	q := qc.Get(now)
	if q != nil && len(q.Windows) > 0 {
		lq := &liveQuota{Windows: make([]liveWindow, 0, len(q.Windows))}
		for _, w := range q.Windows {
			lq.Windows = append(lq.Windows, liveWindow{
				Label: w.Label, Used: w.Used, Limit: w.Limit,
				Remaining: w.Remaining, ResetTime: w.ResetTime,
				UsedRatio: w.UsedRatio,
			})
		}
		// CredentialsState 第二返回值是"是否携带 refresh_token"，与"状态是否
		// 有效"无关（有 access_token 但无 refresh_token 时 ok=false 却已认证）；
		// 状态恒有值，忽略第二返回值（R1-1 评审修复）。
		status, _ := qc.CredentialsState()
		lq.Status = quotaStatusString(status)
		data.Quota = lq
	}
	return data
}

func quotaStatusString(s quota.CredentialsStatus) string {
	if s == quota.CredentialsOK {
		return "ok"
	}
	return "not-logged-in"
}

// pushLive 序列化 liveData 并推送给页面（可在 stMu 锁外调用；数据已由调用方在锁内组装）。
func pushLive(wv interface{ Push(string) }, d liveData) {
	payload := map[string]any{
		"type": "live",
		"data": d,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	wv.Push(string(b))
}

// 全量扫描并发门控：同一时间最多 1 个扫描（避免快速切档叠加 CPU/内存峰值，R6）；
// scanSeq 用于丢弃过期结果——回调只回投最新请求，旧结果作废（前端不会显示过期档）。
var (
	scanGate = make(chan struct{}, 1)
	scanSeq  atomic.Int64
)

// handleWebMessage 处理前端 WebMessage RPC（scan_usage / request_live / default_home）。
// liveFn 返回实时快照：调用方在 stMu 锁内组装 liveData（R1，避免锁外读 metrics.State）。
func handleWebMessage(msg string, wv interface {
	Push(string)
	PushReply(string)
}, liveFn func() liveData, qc *quota.Client, mc *modelcfg.Config, tm *today.Manager, home string) {

	var req struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
		Params *struct {
			StartMs   *int64   `json:"startMs"`
			EndMs     *int64   `json:"endMs"`
			Range     string   `json:"range"`
			Providers []string `json:"providers"`
			Models    []string `json:"models"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(msg), &req); err != nil {
		return
	}

	reply := func(data any, errMsg string) {
		resp := map[string]any{"id": req.ID, "ok": errMsg == ""}
		if errMsg != "" {
			resp["error"] = errMsg
		} else {
			resp["data"] = data
		}
		b, _ := json.Marshal(resp)
		webviewLog("reply: id=%v ok=%v method=%s", req.ID, errMsg == "", req.Method)
		wv.PushReply(string(b))
	}

	switch req.Method {
	case "request_live":
		reply(liveFn(), "")
	case "default_home":
		reply(home, "")
	case "refresh_today":
		// 刷新按钮在非"今日"档时同步今日缓存：后台 ScanToday + 回填缓存
		// （"今日"档由 scan_usage 分支回填，此 RPC 只供非今日档场景）。
		// tm.Refresh() 内部 ScanToday + 写缓存，自带锁，可独立执行。
		go func() {
			start := time.Now()
			tm.Refresh()
			webviewLog("refresh_today: 耗时=%v", time.Since(start))
		}()
		reply(nil, "")
	case "scan_usage":
		// 统一 seq：今日档与全量档共享 scanSeq，过期回复一律丢弃——避免快速切换
		// 时间档时旧扫描的回复后到达覆盖新档结果（前端"今日显示别档数据/多次点击
		// 结果不一致"的 Go 侧来源，与前端 loadSeq 双保险）。
		seq := scanSeq.Add(1)
		// 厂家/模型筛选条件：两条管线共用（与时间档条件正交）。
		var provFilter *scan.ScanFilter
		filtered := false
		if req.Params != nil && (len(req.Params.Providers) > 0 || len(req.Params.Models) > 0) {
			provFilter = &scan.ScanFilter{Providers: req.Params.Providers, Models: req.Params.Models}
			filtered = true
		}
		if req.Params != nil && req.Params.Range == "today" {
			// "今日"档复用 today-only 扫描路径与收紧参数（256K 行/5k 文件，对齐方案
			// §5.2.3/P2-新1），与托盘 today 严格一致；仅其他档走全量 scan_usage（R3）。
			var startMs, endMs int64
			if req.Params.StartMs != nil {
				startMs = *req.Params.StartMs
			}
			if req.Params.EndMs != nil {
				endMs = *req.Params.EndMs
			}
			go func() {
				start := time.Now()
				report, err := scan.ScanToday(home, startMs, endMs, provFilter)
				webviewLog("ScanToday: home=%s err=%v 耗时=%v providers=%v models=%v", home, err, time.Since(start), req.Params.Providers, req.Params.Models)
				if scanSeq.Load() != seq {
					return // 已有更新请求，丢弃过期结果
				}
				if err != nil {
					reply(nil, err.Error())
					return
				}
				// 用本次实时扫描回填今日缓存：顶部"今日总 token"实时卡与"今日"档
				// 同源同步（用户要求两处更新时间一致；订阅额度独立，更新时间不变）。
				// 带厂家/模型筛选的扫描是全量的子集，回填会污染缓存——只在无筛选时回填。
				if !filtered {
					tm.UpdateFromScan(report, startMs)
				}
				// 统一为前端可渲染的 report 结构；今日档为同口径 today-only 聚合
				// （模型/最近请求与全量档同口径，256K 行上限）。cacheHitRate 复用
				// ScanToday 计算结果（与全量档同口径 cacheHitRate），前端渲染无需分支。
				merged := struct {
					GeneratedAt   int64                  `json:"generatedAt"`
					Tokens        scan.TokenTotals       `json:"tokens"`
					Requests      uint64                 `json:"requests"`
					CacheHitRate  float64                `json:"cacheHitRate"`
					PricedCostCny string                 `json:"pricedCostCny"`
					LimitsReached bool                   `json:"limitsReached"`
					TodayMode     bool                   `json:"todayMode"`
					Models        []scan.ModelUsage      `json:"models"`
					Recent        []scan.RecentUsage     `json:"recent"`
					Dimensions    *scan.FilterDimensions `json:"dimensions"`
				}{
					GeneratedAt:   report.GeneratedAt,
					Tokens:        report.Tokens,
					Requests:      report.Requests,
					CacheHitRate:  report.CacheHitRate,
					PricedCostCny: report.PricedCostCny,
					LimitsReached: report.LimitsReached,
					TodayMode:     true,
					Models:        report.Models,
					Recent:        report.Recent,
					Dimensions:    report.Dimensions,
				}
				reply(merged, "")
			}()
			return
		}

		var filter *scan.ScanFilter
		if req.Params != nil {
			filter = &scan.ScanFilter{
				StartMs:   req.Params.StartMs,
				EndMs:     req.Params.EndMs,
				Providers: req.Params.Providers,
				Models:    req.Params.Models,
			}
		}
		// 扫描较重，在独立 goroutine 执行，避免阻塞 UI 线程；完成后经 Push 回投。
		// 并发门控：同一时间最多 1 个全量扫描，且只回投最新请求（R6）。
		go func() {
			// 进门前的代次检查：若排队期间 scanSeq 已被更新请求推进，本扫描已过期，
			// 直接放弃，把门让给最新请求（R2-3 评审修复——否则过期扫描完成后其回复
			// 才被 scanSeq 丢弃，白扫且最新档被排队阻塞）。
			if scanSeq.Load() != seq {
				return
			}
			scanGate <- struct{}{}
			defer func() { <-scanGate }()
			start := time.Now()
			report, err := scan.Scan(home, filter)
			webviewLog("Scan: 耗时=%v providers=%v models=%v", time.Since(start), filter.Providers, filter.Models)
			if err != nil {
				if scanSeq.Load() == seq {
					reply(nil, err.Error())
				}
				return
			}
			if scanSeq.Load() != seq {
				return // 已有更新请求，丢弃过期结果
			}
			reply(report, "")
		}()
	default:
		reply(nil, "unknown method: "+req.Method)
	}
}
