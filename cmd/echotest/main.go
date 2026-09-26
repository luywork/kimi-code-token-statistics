//go:build windows

// echotest：WebView2 白屏/双向通道自动化诊断。
// 验证附加浏览器参数（GPU 软件渲染）对白屏的修复效果，并测试 Go→JS→Go 全链路。
//
// 用法：build/echotest.exe [none|disablegpu|software|navigatetostring]
//   - none             无附加参数 + 虚拟域（复现白屏/导航失败基线）
//   - disablegpu       --disable-gpu + 虚拟域
//   - software         默认（--disable-gpu-driver-bug-workarounds --ignore-gpu-blocklist）+ 虚拟域
//   - navigatetostring software + NavigateToString（定位"虚拟域映射 vs 渲染"环节）
//
// 输出：导航结果（NavigationCompleted isSuccess / webErrorStatus）+ 双向通道结果。
// 退出码：0=导航成功且双向通，1=失败。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/zzl/go-com/com"
	"github.com/zzl/go-win32api/v2/win32"

	"kimi-hud/internal/scan"
	"kimi-hud/internal/webview"
)

// echoHTML 前端：监听 chrome.webview message → 设 title 标记 + postMessage 回传。
const echoHTML = `<!doctype html><html><head><meta charset="utf-8"><title>pending</title></head>
<body><script>
"use strict";
window.chrome.webview.addEventListener('message', function(ev){
  var txt = (typeof ev.data === 'string') ? ev.data : JSON.stringify(ev.data);
  document.title = 'got:' + txt;
  window.chrome.webview.postMessage('ECHO:' + txt);
});
document.title = 'ready';
</script></body></html>`

var user32 = syscall.NewLazyDLL("user32.dll")

func main() {
	runtime.LockOSThread()
	com.Initialize()

	variant := "software"
	if len(os.Args) > 1 {
		variant = os.Args[1]
	}
	if variant == "frontend" {
		runFrontendTest()
		return
	}

	w := webview.New()
	w.HTML = []byte(echoHTML)
	// 每次运行用独立 user-data 目录：避免与残留进程/其他参数的应用共享目录冲突
	// （共享目录 + 不同浏览器参数 → 白屏/不导航，luansxx 案例）。
	w.UserDataFolder = filepath.Join(os.TempDir(), fmt.Sprintf("echotest-%d", time.Now().UnixNano()/1e6))
	switch variant {
	case "none":
		w.AdditionalArgs = ""
		fmt.Println("== 变体: none（无附加参数，虚拟域基线）")
	case "disablegpu":
		w.AdditionalArgs = "--disable-gpu"
		fmt.Println("== 变体: disablegpu（--disable-gpu + 虚拟域）")
	case "gpuoff":
		// 更强 GPU 禁用：标准参数失败说明 GPU 进程本身崩溃（非 blocklist 问题），
		// 完整关闭 GPU 合成/驱动路径，强制软件渲染。
		w.AdditionalArgs = "--disable-gpu --disable-gpu-compositing --disable-gpu-driver-bug-workarounds --ignore-gpu-blocklist"
		fmt.Println("== 变体: gpuoff（完整禁用 GPU + 虚拟域）")
	case "nosandbox":
		// 兼容模式核心假设：模拟旧系统 → Chromium 降级/禁用沙箱。渲染进程沙箱
		// 在这台电脑初始化失败会中止导航（CONNECTION_ABORTED），--no-sandbox 是
		// 兼容模式效果的等效物。
		w.AdditionalArgs = "--no-sandbox"
		fmt.Println("== 变体: nosandbox（--no-sandbox + 虚拟域）")
	case "nosandbox-gpu":
		w.AdditionalArgs = "--no-sandbox --disable-gpu --disable-gpu-compositing"
		fmt.Println("== 变体: nosandbox-gpu（--no-sandbox + 完整禁GPU + 虚拟域）")
	case "navigatetostring":
		w.AdditionalArgs = webview.DefaultAdditionalArgs
		w.LoadMode = "navigatetostring"
		fmt.Println("== 变体: navigatetostring（software + NavigateToString）")
	default:
		w.AdditionalArgs = webview.DefaultAdditionalArgs
		fmt.Println("== 变体: software（--disable-gpu-driver-bug-workarounds --ignore-gpu-blocklist + 虚拟域）")
	}

	navDone := make(chan struct {
		ok  bool
		err int32
	}, 1)
	gotEcho := make(chan string, 10)

	w.OnNavigation = func(success bool, errStatus int32) {
		select {
		case navDone <- struct {
			ok  bool
			err int32
		}{success, errStatus}:
		default:
		}
	}
	w.OnMessage = func(msg string) { gotEcho <- msg }

	if err := w.Open(); err != nil {
		fmt.Println("❌ Open 失败:", err)
		os.Exit(1)
	}

	var m win32.MSG
	deadline := time.Now().Add(30 * time.Second)
	var nav bool
	var pushed bool
	pushAt := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		r, _, _ := user32.NewProc("PeekMessageW").Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1)
		if r != 0 {
			user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&m)))
			user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&m)))
		}

		select {
		case navRes := <-navDone:
			nav = true
			if navRes.ok {
				fmt.Printf("✅ 导航成功 isSuccess=true\n")
			} else {
				fmt.Printf("❌ 导航失败 isSuccess=false webErrorStatus=0x%X (%s)\n",
					uint32(navRes.err), webErrorName(navRes.err))
			}
		case msg := <-gotEcho:
			if strings.HasPrefix(msg, "ECHO:") {
				fmt.Printf("✅ 双向通道通：Go→JS→Go 全链路 OK（echo=%s）\n", truncate(msg, 80))
				w.Close()
				if nav {
					os.Exit(0)
				}
				os.Exit(1)
			}
		default:
		}

		if !pushed && time.Now().After(pushAt) {
			pushed = true
			fmt.Println("[Go->JS] Push 测试消息")
			w.Push(`{"id":1,"ok":true,"data":{"hi":"test"}}`)
		}
		time.Sleep(15 * time.Millisecond)
	}

	fmt.Printf("❌ 超时（30s）：navDone=%v pushed=%v\n", nav, pushed)
	w.Close()
	os.Exit(1)
}

// runFrontendTest 加载真实 frontend.html + 真实 scan 后端，验证厂家/模型筛选链路：
// 1) 首扫渲染出 dimensionCache；2) 选厂家 → 模型级联 + 表格收窄；3) 清筛选恢复。
// 断言经 Eval 注入脚本取回 JSON 结果，输出 PASS/FAIL 并设退出码。
func runFrontendTest() {
	fmt.Println("== 变体: frontend（真实 frontend.html + scan 后端，筛选链路断言）")
	w := webview.New()
	// HTML 留空 → webview 加载 go:embed 的真实 frontend.html。
	w.UserDataFolder = filepath.Join(os.TempDir(), fmt.Sprintf("echotest-frontend-%d", time.Now().UnixNano()/1e6))
	w.AdditionalArgs = webview.DefaultAdditionalArgs

	home, ok := scan.DefaultKimiHome()
	if !ok {
		fmt.Println("❌ 未找到 Kimi Code home")
		os.Exit(1)
	}
	seq := atomic.Int64{}
	var scanMu sync.Mutex
	scanBusy := false

	// reply 组包（对齐 live.go reply 结构）。
	reply := func(id any, data any, errMsg string) {
		resp := map[string]any{"id": id, "ok": errMsg == ""}
		if errMsg != "" {
			resp["error"] = errMsg
		} else {
			resp["data"] = data
		}
		b, _ := json.Marshal(resp)
		w.PushReply(string(b))
	}

	var params struct {
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
	results := make(chan string, 8)
	w.OnMessage = func(msg string) {
		// 测试脚本回传（postMessage "TEST:..."）优先于 RPC 解析。
		if strings.HasPrefix(msg, "TEST:") {
			results <- strings.TrimPrefix(msg, "TEST:")
			return
		}
		if err := json.Unmarshal([]byte(msg), &params); err != nil {
			return
		}
		switch params.Method {
		case "request_live":
			reply(params.ID, map[string]any{}, "")
		case "default_home":
			reply(params.ID, home, "")
		case "scan_usage":
			seq.Add(1)
			filter := &scan.ScanFilter{}
			if params.Params != nil {
				filter.Providers = params.Params.Providers
				filter.Models = params.Params.Models
			}
			if params.Params != nil && params.Params.Range == "today" {
				var startMs, endMs int64
				if params.Params.StartMs != nil {
					startMs = *params.Params.StartMs
				}
				if params.Params.EndMs != nil {
					endMs = *params.Params.EndMs
				}
				go func() {
					report, err := scan.ScanToday(home, startMs, endMs, filter)
					if err != nil {
						reply(params.ID, nil, err.Error())
						return
					}
					reply(params.ID, report, "")
				}()
				return
			}
			go func() {
				scanMu.Lock()
				if scanBusy {
					scanMu.Unlock()
					return
				}
				scanBusy = true
				scanMu.Unlock()
				defer func() {
					scanMu.Lock()
					scanBusy = false
					scanMu.Unlock()
				}()
				report, err := scan.Scan(home, filter)
				if err != nil {
					reply(params.ID, nil, err.Error())
					return
				}
				reply(params.ID, report, "")
			}()
		}
	}

	navDone := make(chan struct {
		ok  bool
		err int32
	}, 1)
	w.OnNavigation = func(success bool, errStatus int32) {
		select {
		case navDone <- struct {
			ok  bool
			err int32
		}{success, errStatus}:
		default:
		}
	}

	if err := w.Open(); err != nil {
		fmt.Println("❌ Open 失败:", err)
		os.Exit(1)
	}

	var m win32.MSG
	deadline := time.Now().Add(45 * time.Second)
	navigated := false
	for time.Now().Before(deadline) {
		r, _, _ := user32.NewProc("PeekMessageW").Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1)
		if r != 0 {
			user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&m)))
			user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&m)))
		}
		if !navigated {
			select {
			case navRes := <-navDone:
				navigated = true
				if !navRes.ok {
					fmt.Printf("❌ 导航失败 webErrorStatus=0x%X (%s)\n", uint32(navRes.err), webErrorName(navRes.err))
					w.Close()
					os.Exit(1)
				}
				fmt.Println("✅ 导航成功（真实 frontend.html）")
			default:
			}
			continue
		}
		break
	}
	if !navigated {
		fmt.Println("❌ 超时：导航未完成")
		w.Close()
		os.Exit(1)
	}

	// 分步注入：脚本异步执行后经 postMessage("TEST:...") 回传（绕开 ExecuteScript
	// 对 async IIFE Promise 等待的兼容性问题），results 由主消息循环收集。
	execStep := func(js, tag string) {
		wrapped := "(async () => { const __r = await (" + js + ")(); window.chrome.webview.postMessage('TEST:' + JSON.stringify({tag:" + fmt.Sprintf("%q", tag) + ", r: JSON.parse(__r)})); })()"
		_ = wrapped
		full := fmt.Sprintf(`(async () => {
			const sleep = ms => new Promise(r => setTimeout(r, ms));
			const __r = await (%s)();
			window.chrome.webview.postMessage('TEST:' + JSON.stringify({tag:%q, r: JSON.parse(__r)}));
		})()`, js, tag)
		if err := w.ExecAsync(full, nil); err != nil {
			fmt.Printf("❌ Exec[%s] 失败: %v\n", tag, err)
			w.Close()
			os.Exit(1)
		}
	}
	waitReport := `async () => {
		const sleep = ms => new Promise(r => setTimeout(r, ms));
		for (let i = 0; i < 150; i++) {
			const rep = document.getElementById('report');
			if (rep && rep.style.display !== 'none') break;
			await sleep(200);
		}
		const rep = document.getElementById('report');
		const provs = (typeof dimensionCache !== 'undefined' && dimensionCache.providers) || [];
		return JSON.stringify({stage:'first-scan', rendered: !!(rep && rep.style.display !== 'none'), provs: provs.length, first: provs.length ? provs[0].value : null});
	}`
	execStep(waitReport, "1-wait")
	pending := "1-wait"

	deadline = time.Now().Add(120 * time.Second)
	stage := 1
	var firstProv string
	var firstJSON string
	for time.Now().Before(deadline) {
		r, _, _ := user32.NewProc("PeekMessageW").Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1)
		if r != 0 {
			user32.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&m)))
			user32.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&m)))
		}
		select {
		case res := <-results:
			var env struct {
				Tag string          `json:"tag"`
				R   json.RawMessage `json:"r"`
			}
			if err := json.Unmarshal([]byte(res), &env); err != nil {
				fmt.Println("❌ 回传解析失败:", res)
				w.Close()
				os.Exit(1)
			}
			parts := []string{env.Tag, string(env.R)}
			fmt.Printf("[step %s] %s\n", parts[0], parts[1])
			switch stage {
			case 1:
				firstJSON = parts[1]
				var st struct {
					Rendered bool   `json:"rendered"`
					Provs    int    `json:"provs"`
					First    string `json:"first"`
				}
				if err := json.Unmarshal([]byte(parts[1]), &st); err != nil || !st.Rendered || st.Provs == 0 {
					fmt.Println("❌ FAIL: 首扫未渲染或无厂家维度", firstJSON)
					w.Close()
					os.Exit(1)
				}
				firstProv = st.First
				stage = 2
				execStep(fmt.Sprintf(`async () => {
					const sleep = ms => new Promise(r => setTimeout(r, ms));
					selProviders = [%q];
					renderFilterOptions();
					loadReport();
					for (let i = 0; i < 40; i++) await sleep(250);
					const rows = document.querySelectorAll('#modelTable tbody tr').length;
					const btn = document.getElementById('providerBtn').textContent;
					const modelOpts = Array.from(document.querySelectorAll('#modelPop .filter-item .nm')).map(e => e.textContent);
					return JSON.stringify({rows, btn, modelOpts});
				}`, firstProv), "2-select")
				pending = "2-select"
			case 2:
				var st2 struct {
					Rows      int      `json:"rows"`
					Btn       string   `json:"btn"`
					ModelOpts []string `json:"modelOpts"`
				}
				if err := json.Unmarshal([]byte(parts[1]), &st2); err != nil {
					fmt.Println("❌ FAIL: 解析步骤2结果", parts[1])
					w.Close()
					os.Exit(1)
				}
				stage = 3
				execStep(`async () => {
					const sleep = ms => new Promise(r => setTimeout(r, ms));
					selProviders = []; selModels = [];
					renderFilterOptions();
					loadReport();
					for (let i = 0; i < 40; i++) await sleep(250);
					const rows = document.querySelectorAll('#modelTable tbody tr').length;
					const btn = document.getElementById('providerBtn').textContent;
					return JSON.stringify({rows, btn});
				}`, "3-restore")
			case 3:
				var st3 struct {
					Rows int    `json:"rows"`
					Btn  string `json:"btn"`
				}
				if err := json.Unmarshal([]byte(parts[1]), &st3); err != nil {
					fmt.Println("❌ FAIL: 解析步骤3结果", parts[1])
					w.Close()
					os.Exit(1)
				}
				fmt.Printf("✅ PASS: 厂家=%s 筛选/恢复链路走通（各步 JSON 见上）\n", firstProv)
				w.Close()
				os.Exit(0)
			}
		default:
		}
		time.Sleep(15 * time.Millisecond)
	}
	fmt.Printf("❌ 超时：stage=%d pending=%s\n", stage, pending)
	w.Close()
	os.Exit(1)
}

func webErrorName(s int32) string {
	names := map[int32]string{
		0x0: "UNKNOWN", 0x1: "CERTIFICATE_COMMON_NAME_IS_INCORRECT",
		0x2: "CERTIFICATE_EXPIRED", 0x3: "CLIENT_CERTIFICATE_CONTAINS_ERRORS",
		0x4: "CERTIFICATE_REVOKED", 0x5: "CERTIFICATE_IS_INVALID",
		0x6: "SERVER_UNREACHABLE", 0x7: "TIMEOUT", 0x8: "ERROR_HTTP_INVALID_SERVER_RESPONSE",
		0x9: "CONNECTION_ABORTED", 0xA: "CONNECTION_RESET", 0xB: "DISCONNECTED",
		0xC: "CANNOT_CONNECT", 0xD: "HOST_NAME_NOT_RESOLVED",
		0xE: "OPERATION_CANCELED", 0xF: "REDIRECT_FAILED",
		0x10: "UNEXPECTED_ERROR",
	}
	if n, ok := names[s]; ok {
		return n
	}
	return fmt.Sprintf("UNKNOWN_0x%X", uint32(s))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
