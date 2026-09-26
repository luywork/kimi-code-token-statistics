package webview

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/zzl/go-com/com"
	"github.com/zzl/go-webview2/wv2"
	"github.com/zzl/go-win32api/v2/win32"
)

//go:embed frontend.html
var frontendHTML []byte

// 虚拟主机域（前端加载方式）：WebView2 官方标准虚拟域加载，比 NavigateToString
// 稳定（实测本机 NavigateToString 偶发 NavigationCompleted CONNECTION_ABORTED、
// 且受限页面 WebMessage 支持不完整）。前端运行时解压到 %TEMP% 落盘映射。
const virtualHost = "hud.local"
const frontendDirName = "kimi-hud-web"

// frontendDir 确保前端 index.html 落盘到 %TEMP%/kimi-hud-web（Open 时一次，冷路径），
// 返回目录路径。html 为空时用内嵌 frontendHTML。
func frontendDir(html []byte) (string, error) {
	dir := filepath.Join(os.TempDir(), frontendDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	content := html
	if len(content) == 0 {
		content = frontendHTML
	}
	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// webviewLog 追加诊断日志到 %USERPROFILE%\.kimi-code-hud\webview.log（GUI 无 stderr，
// 定位 WebView2 创建/导航问题用；与托盘 debug.log 区分）。
var wvLogOnce sync.Once

func webviewLog(format string, args ...any) {
	dir := filepath.Join(os.Getenv("USERPROFILE"), ".kimi-code-hud")
	_ = os.MkdirAll(dir, 0o755)
	wvLogOnce.Do(func() {
		_ = os.WriteFile(filepath.Join(dir, "webview.log"), nil, 0o644)
	})
	f, err := os.OpenFile(filepath.Join(dir, "webview.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(f, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

// 不做进程级 DPI awareness 声明（移除 PER_MONITOR_AWARE_V2）。
// 实测（2026-08-20）：PER_MONITOR_AWARE_V2 下 WebView2 详情窗口白屏/导航失败
// （NavigationCompleted CONNECTION_ABORTED），而 DPI unaware（兼容模式/系统默认
// 位图缩放）正常。稳定优先 → 保持系统默认 DPI unaware，放弃 per-monitor 精确缩放
// （详情窗口位图缩放的可接受模糊换取 WebView2 稳定渲染，对齐"性能优先于复用"破例）。
// 注意：若改回 DPI aware，必须同时处理 WM_DPICHANGED 与 SetBounds 的 DPI 换算。

// DefaultAdditionalArgs 默认传给 WebView2 浏览器进程的附加参数。
// 实测根因（2026-08-20，自动化定位）：本机渲染进程的 Chromium 沙箱初始化失败 →
// 导航中止（NavigationCompleted CONNECTION_ABORTED，白屏）；`--no-sandbox` 修复
// （导航成功 + WebMessage 双向通道全通）。用户此前"设兼容 Win8/7 才正常"的机制
// 即：兼容模式模拟旧系统 → Chromium 降级/禁用沙箱。现直接注入 --no-sandbox，
// 等效于兼容模式，无需用户手动设兼容。
// 安全性权衡：详情窗口仅加载 go:embed 内嵌静态 HTML（无远程内容）、WebMessage
// 只与本进程通信、不导航外部 URL——禁用沙箱风险可控，属"稳定性优先于安全默认"
// 的取舍（对齐 AGENT.md"性能优先于复用"破例注释要求）。
const DefaultAdditionalArgs = "--no-sandbox"

// 宿主窗口私有消息：UI 线程驱动 push 队列刷新。
const (
	wmHudPush = 0x0400 + 0x100 // WM_APP + 0x100
	// pushQueueCap live 心跳队列容量上限：heartbeat 每 500ms 一条，UI 线程
	// 短暂繁忙时缓冲几条即被 flush 消费，8 条足够覆盖导航早期缓冲。
	pushQueueCap = 8
)

// Window WebView2 详情窗口（三层生命周期：宿主窗口/视图/Environment）。
// 所有 COM 相关方法必须在同一线程调用（Open/Close/由 UI 消息循环线程驱动）；
// 调用方应确保创建窗口的 goroutine LockOSThread（tray 消息循环所在线程）。
type Window struct {
	// OnMessage 前端→Go 消息（原始 JSON 字符串）。
	OnMessage func(json string)
	// OnLoaded 页面导航完成回调（UI 线程），用于首推实时数据。
	OnLoaded func()
	// OnNavigation 导航完成回调（success=isSuccess, errStatus=WebErrorStatus），
	// 自动化测试/诊断用（白屏定位）。
	OnNavigation func(success bool, errStatus int32)

	// HTML 自定义页面内容（nil 时用内嵌 frontend.html；自动化测试用）。
	HTML []byte
	// AdditionalArgs 附加浏览器参数（空=不设置环境变量，New 默认 DefaultAdditionalArgs，测试可覆盖）。
	AdditionalArgs string
	// LoadMode 加载方式："virtualhost"（默认，虚拟域+Navigate）/"navigatetostring"（诊断用）。
	LoadMode string
	// UserDataFolder WebView2 用户数据目录（空=默认，在 exe 旁创建 .WebView2）。
	// 不同参数/版本的应用必须用独立目录，否则共享目录 + 不同浏览器参数冲突导致白屏
	// （子代理搜索确认：luansxx/128330604 案例）。
	UserDataFolder string

	mu    sync.Mutex
	hwnd  uintptr
	class uintptr
	scope *com.Scope
	env   *wv2.ICoreWebView2Environment
	ctl   *wv2.ICoreWebView2Controller
	wv    *wv2.ICoreWebView2
	open  bool
	// ready 导航是否已完成。PostWebMessageAsJson 在导航完成前投递会被 WebView2
	// 丢弃（页面脚本可在导航早期执行并发起请求，Go 回复早于 NavigationCompleted）——
	// 实测导致"页面可见但一直扫描中"。ready 前消息缓冲在 pushCh，导航完成后补发。
	ready bool
	// R4：异步创建期间的状态位。opening 防重入；closing 标记"关闭请求早于
	// Environment 回调"——延迟到回调完成后统一清理，避免在空 scope 上 Leave
	// 后回调再 AddComPtr 导致 COM 引用泄漏（每次快速开→关累积，浏览器进程残留）。
	opening bool
	closing bool

	// pushQ 跨线程推送队列：有界 FIFO（live cap 8）。RPC 回复不可丢（覆盖写会吞掉
	// 导航前缓冲的 scan 回复导致前端永远"扫描中"，见 PushReply），live 数据可接受丢旧。
	// 满队列时只丢最旧的 live，保留全部 RPC 回复（R1-2 评审修复）。每条由一次
	// wmHudPush 驱动 flush 发出。
	pushMu sync.Mutex
	pushQ  []pushItem
}

// pushItem 队列元素；isLive 区分"可丢的 live 心跳"与"不可丢的 RPC 回复"。
type pushItem struct {
	json   string
	isLive bool
}

// New 创建窗口控制器（不立即开窗）。默认启用 GPU 软件渲染参数（白屏修复，
// 见 DefaultAdditionalArgs）；测试可覆盖 w.AdditionalArgs。
func New() *Window {
	return &Window{AdditionalArgs: DefaultAdditionalArgs}
}

// Open 打开窗口（UI 线程调用）：复用宿主窗口，重建 Environment/Controller/WebView。
// R4 修复：① 检查 CreateCoreWebView2EnvironmentWithOptions 同步返回值（异步回调结果
// 无法被 Open 同步感知，调用方在回调完成前拿不到 envErr）；② opening 防重入；
// ③ 快速 Open→Close 时由回调末尾检查 closing 做延迟清理（见 Close）。
func (w *Window) Open() error {
	w.mu.Lock()
	if w.open {
		w.mu.Unlock()
		w.focus()
		return nil
	}
	if w.opening {
		w.mu.Unlock()
		return nil // 已在打开流程中（回调未完成），防重入
	}
	w.opening = true
	w.closing = false
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.opening = false
		w.mu.Unlock()
	}()

	runtime.LockOSThread() // 幂等：COM apartment 绑定当前 OS 线程
	if err := EnsureLoader(); err != nil {
		webviewLog("Open: EnsureLoader 失败: %v", err)
		return err
	}
	com.Initialize()

	if w.hwnd == 0 {
		hwnd, class, err := createHostWindow()
		if err != nil {
			webviewLog("Open: createHostWindow 失败: %v", err)
			return err
		}
		w.hwnd = hwnd
		w.class = class
		hostWindows.Store(hwnd, w)
	}
	// 先显示宿主窗口再创建 WebView2（对齐 SPIKE demo：可见窗口上创建 controller，
	// 视图初始尺寸立即生效；隐藏窗口上创建已知会导致视图不可见/白屏）。
	// 若复用已隐藏的宿主（重开），同样先 show 再重建 Environment。
	w.show()
	webviewLog("Open: host window visible hwnd=0x%X", w.hwnd)

	scope := com.NewScope()
	w.mu.Lock()
	w.scope = scope
	w.mu.Unlock()

	// 附加浏览器参数：在 CreateEnvironment 前通过官方环境变量生效
	// （WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS，CreateEnvironment 时读取）。
	// 默认禁用 GPU 驱动 bug 规避，强制软件渲染（白屏修复，见 DefaultAdditionalArgs）。
	if w.AdditionalArgs != "" {
		_ = os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", w.AdditionalArgs)
		webviewLog("Open: WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=%s", w.AdditionalArgs)
	}

	hr := wv2.CreateCoreWebView2EnvironmentWithOptions("", w.UserDataFolder, nil,
		wv2.NewICoreWebView2CreateCoreWebView2EnvironmentCompletedHandlerByFunc(
			func(errorCode com.Error, env *wv2.ICoreWebView2Environment) com.Error {
				webviewLog("Open: environment callback errorCode=0x%X env=%v", uint32(errorCode), env != nil)
				if errorCode != 0 {
					// 环境创建失败（Runtime 缺失之外的原因）：释放空 scope，复位状态。
					scope.Leave()
					w.mu.Lock()
					if w.scope == scope {
						w.scope = nil
					}
					w.open = false
					w.closing = false
					w.mu.Unlock()
					return com.Error(win32.S_OK)
				}
				w.mu.Lock()
				w.env = env
				w.mu.Unlock()
				scope.AddComPtr(env, true)
				env.CreateCoreWebView2Controller(win32.HWND(w.hwnd),
					wv2.NewICoreWebView2CreateCoreWebView2ControllerCompletedHandlerByFunc(
						func(errorCode com.Error, ctl *wv2.ICoreWebView2Controller) com.Error {
							webviewLog("Open: controller callback errorCode=0x%X ctl=%v", uint32(errorCode), ctl != nil)
							if errorCode != 0 {
								scope.Leave()
								w.mu.Lock()
								if w.scope == scope {
									w.scope = nil
								}
								w.open = false
								w.closing = false
								w.mu.Unlock()
								return com.Error(win32.S_OK)
							}
							w.ctl = ctl
							scope.AddComPtr(ctl, true)
							var wv *wv2.ICoreWebView2
							ctl.GetCoreWebView2(&wv)
							w.wv = wv
							scope.AddComPtr(wv, true)
							webviewLog("Open: GetCoreWebView2 ok wv=%v", wv != nil)

							var settings *wv2.ICoreWebView2Settings
							wv.GetSettings(&settings)
							settings.SetIsScriptEnabled(win32.TRUE)
							settings.SetIsWebMessageEnabled(win32.TRUE)
							settings.SetAreDefaultContextMenusEnabled(win32.TRUE)
							settings.SetAreDevToolsEnabled(win32.FALSE)

							// 前端→Go：WebMessage 通道。
							var msgToken wv2.EventRegistrationToken
							wv.Add_WebMessageReceived(
								wv2.NewICoreWebView2WebMessageReceivedEventHandlerByFunc(
									func(sender *wv2.ICoreWebView2, args *wv2.ICoreWebView2WebMessageReceivedEventArgs) com.Error {
										var m win32.PWSTR
										args.TryGetWebMessageAsString(&m)
										msg := win32.PwstrToStr(m)
										win32.CoTaskMemFree(unsafe.Pointer(m))
										if len(msg) > 200 {
											webviewLog("WebMessageReceived: %s…", msg[:200])
										} else {
											webviewLog("WebMessageReceived: %s", msg)
										}
										if w.OnMessage != nil {
											w.OnMessage(msg)
										}
										return com.Error(win32.S_OK)
									}, true), &msgToken)

							// 导航完成：置 ready（补发缓冲消息）+ 调整尺寸 + 触发 OnLoaded（首推实时数据）。
							var navToken wv2.EventRegistrationToken
							wv.Add_NavigationCompleted(
								wv2.NewICoreWebView2NavigationCompletedEventHandlerByFunc(
									func(sender *wv2.ICoreWebView2, args *wv2.ICoreWebView2NavigationCompletedEventArgs) com.Error {
										var ok int32
										var errStatus int32
										args.GetIsSuccess(&ok)
										args.GetWebErrorStatus(&errStatus)
										webviewLog("Open: NavigationCompleted isSuccess=%d webErrorStatus=0x%X", ok, uint32(errStatus))
										if w.OnNavigation != nil {
											w.OnNavigation(ok != 0, errStatus)
										}
										w.mu.Lock()
										w.ready = true
										w.mu.Unlock()
										// 补发导航前缓冲的回复（否则被 WebView2 丢弃，前端一直"扫描中"）。
										w.flush()
										resizeWebView(w, win32.HWND(w.hwnd))
										if w.OnLoaded != nil {
											w.OnLoaded()
										}
										return com.Error(win32.S_OK)
									}, true), &navToken)

							// 立即设置视图 bounds（对齐 SPIKE）：controller 默认 0×0，
							// 若等 NavigationCompleted 再 resize，导航前窗口显示灰底空白。
							resizeWebView(w, win32.HWND(w.hwnd))

							if w.LoadMode == "navigatetostring" {
								// 诊断/对比模式：直接 NavigateToString（不建虚拟域），
								// 用于定位"虚拟域映射 vs 渲染"哪个环节失败。
								html := frontendHTML
								if len(w.HTML) > 0 {
									html = w.HTML
								}
								webviewLog("Open: [navigatetostring] html=%d bytes", len(html))
								wv.NavigateToString(string(html))
							} else {
								// 虚拟域 + Navigate 标准加载。ICoreWebView2_3 通过
								// QueryInterface 获取（不能直接 cast：GetCoreWebView2 返回
								// ICoreWebView2 接口指针，cast 后用其 vtable 调 _3 槽位会
								// 调错函数，映射可能"假成功"→ Navigate 到虚拟域 CONNECTION_ABORTED）。
								dir, err := frontendDir(w.HTML)
								if err != nil {
									webviewLog("Open: frontendDir 失败: %v", err)
									return com.Error(win32.S_OK)
								}
								var pv unsafe.Pointer
								if hrQI := wv.IUnknown.QueryInterface(&wv2.IID_ICoreWebView2_3, unsafe.Pointer(&pv)); hrQI == 0 && pv != nil {
									wv3 := (*wv2.ICoreWebView2_3)(pv)
									hrM := wv3.SetVirtualHostNameToFolderMapping(virtualHost, dir,
										wv2.COREWEBVIEW2_HOST_RESOURCE_ACCESS_KIND.COREWEBVIEW2_HOST_RESOURCE_ACCESS_KIND_ALLOW)
									webviewLog("Open: SetVirtualHostNameToFolderMapping(QI) %s hr=0x%X", virtualHost, uint32(hrM))
									// 释放 QueryInterface 获得的引用（用原生 Release）。
									wv3.IUnknown.Release()
								} else {
									webviewLog("Open: QueryInterface ICoreWebView2_3 失败 hr=0x%X", uint32(hrQI))
								}
								webviewLog("Open: Navigate https://%s/index.html", virtualHost)
								wv.Navigate("https://" + virtualHost + "/index.html")
							}
							webviewLog("Open: Navigate 已调用，open=true")
							w.mu.Lock()
							w.open = true
							shouldClose := w.closing // 回调期间被 Close 请求 → 延迟清理
							w.mu.Unlock()
							if shouldClose {
								w.cleanup()
								return com.Error(win32.S_OK)
							}
							w.show()
							return com.Error(win32.S_OK)
						}, true))
				return com.Error(win32.S_OK)
			}, true))

	if hr != 0 {
		// 同步返回失败（异步回调不会触发）：立即释放。
		scope.Leave()
		w.mu.Lock()
		if w.scope == scope {
			w.scope = nil
		}
		w.mu.Unlock()
		return comErrToError(com.Error(hr))
	}
	return nil
}

// Close 关闭窗口（UI 线程调用）：销毁视图 + 释放 Environment（浏览器进程退出）+ 宿主隐藏。
// 三层处置对齐方案 §6.1：宿主窗口隐藏保留（重开复用），视图/Environment 释放。
// R4 修复：若 Environment 异步回调尚未完成（w.env==nil），只标记 closing，
// 由回调末尾统一清理——避免空 scope.Leave() 后回调再 AddComPtr 造成 COM 泄漏。
func (w *Window) Close() {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return
	}
	if !w.open && w.env == nil {
		w.mu.Unlock()
		return
	}
	w.closing = true
	if w.env == nil {
		w.mu.Unlock()
		return // 回调未完成：延迟到回调末尾 cleanup()
	}
	w.mu.Unlock()
	w.cleanup()
}

// cleanup 释放 COM scope（视图/Environment → 浏览器进程退出）并隐藏宿主窗口。
func (w *Window) cleanup() {
	w.mu.Lock()
	scope := w.scope
	w.scope = nil
	w.wv, w.ctl, w.env = nil, nil, nil
	w.open = false
	w.closing = false
	// 重开时 WebView2 全新重建：ready 必须复位——否则新导航完成前 flush 就会投递
	// PostWebMessageAsJson，被 WebView2 丢弃（页面未加载），且队列被清空后
	// NavigationCompleted 无补发 → 前端永远"扫描中"。pushQ 一并清空：页面已销毁，
	// 未发送的旧回复无意义，避免重开后补发过期数据。
	w.ready = false
	w.pushMu.Lock()
	w.pushQ = nil
	w.pushMu.Unlock()
	hwnd := w.hwnd
	w.mu.Unlock()
	if scope != nil {
		scope.Leave()
	}
	hideWindow(hwnd)
}

// IsOpen 窗口是否处于打开状态。
func (w *Window) IsOpen() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.open
}

// Push 向页面推送可丢弃的 live 心跳 JSON（可跨线程）：有界 FIFO（cap 8），
// 队列满时丢最旧的 live（heartbeat 每 500ms 一条，丢旧不影响最终一致——下一条
// 心跳会重推最新值）。RPC 回复请用 PushReply（不可丢）。
func (w *Window) Push(json string) {
	w.pushQPush(json, true)
}

// PushReply 向页面推送 RPC 回复 JSON（可跨线程，不可丢）：无条件入队——
// 覆盖写会吞掉导航前缓冲的 scan 回复导致前端永远"扫描中"。RPC 回复数量极少，
// 队列满时只牺牲 live，回复永不丢弃（R1-2 评审修复）。
func (w *Window) PushReply(json string) {
	w.pushQPush(json, false)
}

func (w *Window) pushQPush(json string, isLive bool) {
	w.pushMu.Lock()
	if !isLive {
		w.pushQ = append(w.pushQ, pushItem{json: json})
	} else if len(w.pushQ) < pushQueueCap {
		w.pushQ = append(w.pushQ, pushItem{json: json, isLive: true})
	} else {
		// 队列满且新消息是 live：丢最旧的 live 为新 live 腾位（保留 RPC 回复）。
		// 队列中无 live 可丢（全为 RPC 回复）时丢弃新 live 本身——live 可由
		// 下一条心跳重推，不影响正确性。
		for i, it := range w.pushQ {
			if it.isLive {
				w.pushQ = append(w.pushQ[:i], w.pushQ[i+1:]...)
				w.pushQ = append(w.pushQ, pushItem{json: json, isLive: true})
				break
			}
		}
	}
	w.pushMu.Unlock()
	// hwnd 读取走 w.mu（与 Open 唯一一次写入、cleanup 读取同锁）：生产上 Push 只在
	// open=true（写入之后）才发生，竞态实际不可达，但跨线程无锁读会让该不变式依赖
	// 隐式前提，race detector 也无法证明清白（R2-新1）。
	w.mu.Lock()
	hwnd := w.hwnd
	w.mu.Unlock()
	if hwnd != 0 {
		_, _, _ = procPostMessageW.Call(hwnd, wmHudPush, 0, 0)
	}
}

// flush 在 UI 线程消费队列（每条由一次 wmHudPush 驱动）。
// 导航未完成（!ready）时不投递：此时 PostWebMessageAsJson 会被 WebView2 丢弃，
// 消息保留在队列由 NavigationCompleted 补发（ready 门控，见 Window.ready 注释）。
// 注意：必须循环清空整个队列——导航早期页面 JS 发起的 RPC（request_live/scan_usage）
// 可能同时缓冲多条回复，若每次只发一条，导航完成补发第一条后其余无对应 wmHudPush
// 驱动（补发前的 wmHudPush 已因 !ready 被消费），前端永远"扫描中"（实测竞态）。
func (w *Window) flush() {
	w.mu.Lock()
	ready := w.ready
	wv := w.wv
	w.mu.Unlock()
	if !ready || wv == nil {
		return
	}
	for {
		w.pushMu.Lock()
		if len(w.pushQ) == 0 {
			w.pushMu.Unlock()
			return
		}
		it := w.pushQ[0]
		w.pushQ = w.pushQ[1:]
		w.pushMu.Unlock()
		json := it.json
		if len(json) > 120 {
			webviewLog("flush PostWebMessageAsJson: %s…", json[:120])
		} else {
			webviewLog("flush PostWebMessageAsJson: %s", json)
		}
		if hr := wv.PostWebMessageAsJson(json); hr != 0 {
			webviewLog("flush PostWebMessageAsJson 失败 hr=0x%X", uint32(hr))
		}
	}
}

// Eval 在页面执行 JS（异步），返回 JSON 编码的结果字符串（空页返回 ""）。
// 必须在 UI 线程调用（ExecuteScript 回调在 UI 线程触发）；timeout 内无回调则返回超时。
// 用于自动化测试/诊断（确认页面 chrome.webview 是否可用、消息是否送达）。
func (w *Window) Eval(js string, timeout time.Duration) (string, error) {
	w.mu.Lock()
	wv := w.wv
	w.mu.Unlock()
	if wv == nil {
		return "", fmt.Errorf("webview 未打开")
	}
	done := make(chan struct{})
	var result string
	var evalErr com.Error
	handler := wv2.NewICoreWebView2ExecuteScriptCompletedHandlerByFunc(
		func(errCode com.Error, resultObjectJson string) com.Error {
			evalErr = errCode
			result = resultObjectJson
			select {
			case <-done:
			default:
				close(done)
			}
			return com.Error(win32.S_OK)
		}, false)
	if hr := wv.ExecuteScript(js, handler); hr != 0 {
		return "", comErrToError(hr)
	}
	select {
	case <-done:
	case <-time.After(timeout):
		return "", fmt.Errorf("ExecuteScript 超时")
	}
	if evalErr != 0 {
		return "", comErrToError(evalErr)
	}
	return result, nil
}

// focus 置前聚焦（窗口已开时重开点击）。
func (w *Window) focus() {
	if w.hwnd != 0 {
		_, _, _ = procShowWindow.Call(w.hwnd, swShow)
		_, _, _ = procSetForegroundWindow.Call(w.hwnd)
	}
}

// ExecAsync 在页面里执行 JS，立即返回（结果经 cb 在 UI 线程消息回调中送达）。
// 与 Eval 的区别：不阻塞等待——Eval 阻塞在 UI 线程会锁死消息泵导致回调永不触发
// （自动化测试/诊断在主消息循环中调用本方法收集结果）。
func (w *Window) ExecAsync(js string, cb func(resultJSON string)) error {
	w.mu.Lock()
	wv := w.wv
	w.mu.Unlock()
	if wv == nil {
		return fmt.Errorf("webview 未打开")
	}
	handler := wv2.NewICoreWebView2ExecuteScriptCompletedHandlerByFunc(
		func(errCode com.Error, resultObjectJson string) com.Error {
			if cb != nil {
				cb(resultObjectJson)
			}
			return com.Error(win32.S_OK)
		}, false)
	if hr := wv.ExecuteScript(js, handler); hr != 0 {
		return comErrToError(hr)
	}
	return nil
}

func (w *Window) show() {
	// SW_RESTORE 而非 SW_SHOW：宿主窗口 Close 时只隐藏保留（hwnd 复用），
	// SW_SHOW 会恢复上次的显示状态——若用户上次最大化过，重开即全屏黑窗
	// （实测 rect=(-8,-8,1928,1058)）。SW_RESTORE 还原最大化/最小化到正常尺寸。
	_, _, _ = procShowWindow.Call(w.hwnd, swShowRestore)
	procUpdateWindow.Call(w.hwnd)
	_, _, _ = procSetForegroundWindow.Call(w.hwnd)
}

// wndProc 宿主窗口过程（运行于 UI 消息循环线程）。
func (w *Window) wndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmHudPush:
		// 取最新一条推送给页面（Go→JS）。
		w.flush()
		return 0
	case 0x0005: // WM_SIZE
		// 必须先交给 DefWindowProc：顶层窗口的 WM_SIZE 若被吞掉（不调默认处理），
		// 最大化/还原状态与窗口框架会错乱。resizeWebView 只设 WebView 子视图尺寸。
		procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
		if w.ctl != nil {
			resizeWebView(w, win32.HWND(hwnd))
		}
		return 0
	case 0x0010: // WM_CLOSE
		// 关窗回托盘：销毁视图 + 释放 Environment，宿主隐藏。
		w.Close()
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

func resizeWebView(w *Window, hwnd win32.HWND) {
	if w.ctl == nil {
		return
	}
	var rect win32.RECT
	win32.GetClientRect(hwnd, &rect)
	var bounds wv2.TagRECT
	bounds.Left = rect.Left
	bounds.Top = rect.Top
	bounds.Right = rect.Right
	bounds.Bottom = rect.Bottom
	webviewLog("resizeWebView client=%dx%d", rect.Right-rect.Left, rect.Bottom-rect.Top)
	w.ctl.SetBounds(bounds)
}

func hideWindow(hwnd uintptr) {
	if hwnd != 0 {
		_, _, _ = procShowWindow.Call(hwnd, swHide)
	}
}

// ---- Win32 宿主窗口 ----

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

const (
	wsOverlappedWindow = 0x00CF0000
	swShow             = 5
	swShowRestore      = 9 // SW_RESTORE：激活并显示，若窗口被最大化/最小化则还原到正常尺寸位置
	swHide             = 0
	wmClose            = 0x0010
	className          = "KimiHudWebViewHost"
)

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	shell32                 = syscall.NewLazyDLL("shell32.dll")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procUpdateWindow        = user32.NewProc("UpdateWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procExtractIconExW      = shell32.NewProc("ExtractIconExW")
	procDestroyIcon         = user32.NewProc("DestroyIcon")
)

var hostWindows sync.Map // uintptr(hwnd) -> *Window（wndproc 反查）

// createHostWindow 注册窗口类并创建宿主窗口。
func createHostWindow() (uintptr, uintptr, error) {
	hinst, _, _ := procGetModuleHandleW.Call(0)
	clsName, _ := syscall.UTF16PtrFromString(className)
	// 窗口类图标从 exe 自身提取（ExtractIconExW，与托盘 loadImageIconFromFile
	// 同一机制/同一图标资源）：hIcon 用 32px、hIconSm 用 16px，保证标题栏与
	// 任务栏/Alt-Tab 图标一致。类图标随进程生命周期持有，进程退出系统回收，
	// 仅失败路径销毁（避免极端重试累积句柄）。
	var hIcon, hIconSm uintptr
	if exe, err := os.Executable(); err == nil && exe != "" {
		hIcon, hIconSm = loadClassIcons(exe)
	}
	wc := &wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   syscall.NewCallback(hostWndProc),
		hInstance:     hinst,
		hIcon:         hIcon,
		hIconSm:       hIconSm,
		hbrBackground: 1 + 5, // COLOR_WINDOW+1（白底，对齐 SPIKE；深灰会盖住短暂空白窗口）
		lpszClassName: clsName,
	}
	if ret, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(wc))); ret == 0 {
		// 已注册（重开场景）则忽略；仍失败返回错误。
		if lastErr() != 0 && lastErr() != 0x582 { // ERROR_CLASS_ALREADY_EXISTS
			destroyClassIcons(hIcon, hIconSm)
			return 0, 0, syscall.Errno(lastErr())
		}
	}

	title, _ := syscall.UTF16PtrFromString("Kimi Code 用量明细")
	hwnd, _, _ := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(clsName)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow, 100, 100, 980, 900,
		0, 0, hinst, 0,
	)
	if hwnd == 0 {
		destroyClassIcons(hIcon, hIconSm)
		return 0, 0, syscall.Errno(lastErr())
	}
	return hwnd, 0, nil
}

// loadClassIcons 从 exe 文件提取 32px/16px 图标句柄（窗口类 hIcon/hIconSm 用）。
// 用 ExtractIconExW 而非 LoadImageW(MAKEINTRESOURCE) 从模块资源加载：本项目
// .syso 的 RT_GROUP_ICON 数据为 PNG 直存（非标准 GRPICONDIR），LoadImage 报
// "资源类型找不到"（实测 2026-08-25），而 ExtractIconEx 可正常解析出品牌图标。
func loadClassIcons(exePath string) (big, small uintptr) {
	p, err := syscall.UTF16PtrFromString(exePath)
	if err != nil {
		return 0, 0
	}
	if n, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(p)), 0,
		uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1); n == 0 {
		return 0, 0
	}
	return big, small
}

// destroyClassIcons 释放窗口类图标句柄（仅 createHostWindow 失败路径调用）。
func destroyClassIcons(icons ...uintptr) {
	for _, ic := range icons {
		if ic != 0 {
			procDestroyIcon.Call(ic)
		}
	}
}

// hostWndProc 全局窗口过程：反查 Window 实例（wndproc 无 userdata，用 map 关联）。
func hostWndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	if v, ok := hostWindows.Load(hwnd); ok {
		if w := v.(*Window); w != nil {
			return w.wndProc(hwnd, message, wParam, lParam)
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

func lastErr() uintptr {
	return uintptr(syscall.Errno(syscall.GetLastError().(syscall.Errno)))
}

func comErrToError(hr com.Error) error {
	if hr == 0 {
		return nil
	}
	return syscall.Errno(uintptr(hr) & 0xFFFF)
}
