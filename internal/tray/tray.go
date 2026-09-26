// Package tray 实现 Windows 系统托盘常驻（纯 Win32，无 CGO）。
package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ItemKind 菜单项样式分类（owner-draw 自绘菜单）。
type ItemKind uint8

const (
	KindValue   ItemKind = iota // 指标行：语义色圆点 + 主文本，可带进度条/副文本
	KindTitle                   // 品牌标题行：左侧品牌圆点 + 粗体标题
	KindHeading                 // 分段标题行：muted 小字加粗
	KindAction                  // 可点击动作行：hover 圆角高亮 + 三角箭头
)

// MenuItem 一个菜单项。
type MenuItem struct {
	Text      string
	Disabled  bool
	Separator bool
	ID        int

	// owner-draw 样式字段（零值 = 使用默认样式）。
	Kind     ItemKind // KindValue 默认；Separator=true 的项忽略 Kind
	Color    uint32   // 语义色 RGB(0xRRGGBB)；0 表示用主题默认前景色
	Sub      string   // 右侧副文本（右对齐，muted 色）
	Progress *float64 // 0-1 进度条（KindValue 且非 nil 时在主文本右侧绘制）
}

// Handler 菜单回调。
type Handler struct {
	// BuildMenu 在每次弹出前调用，返回当前菜单项。
	BuildMenu func() []MenuItem
	// OnCommand 处理菜单命令（ID 对应 MenuItem.ID）。
	OnCommand func(id int)
}

// Tray 托盘实例。
type Tray struct {
	handler *Handler

	mu           sync.Mutex
	hwnd         uintptr
	icon         uintptr
	iconCol      uint32 // 当前图标颜色
	iconPath     string // 外部 .ico 文件路径（可选）
	externalIcon bool   // 是否成功加载了外部自定义图标
	running      bool

	// 菜单状态（窗口线程访问）。
	menuOpen bool // 当前是否有弹出菜单在显示（防止重复弹出）
	closing  bool // 收到关闭请求，退出 TrackPopupMenu 循环

	// owner-draw 自绘菜单状态（仅窗口线程访问，showMenuPersistent 期间有效）。
	owndraw owndrawState
}

// New 创建托盘实例。iconPath 为非空时优先从该文件加载托盘图标，
// 加载失败或为空时回退到运行时绘制的彩色圆点图标。
func New(handler *Handler, iconPath string) *Tray {
	return &Tray{handler: handler, iconPath: iconPath}
}

// Run 进入消息循环，阻塞直到退出。
func (t *Tray) Run() error {
	logDebug("Run: start")
	defer logDebug("Run: end")

	className, err := syscall.UTF16PtrFromString("KimiTokenHudTrayWnd")
	if err != nil {
		return err
	}

	wndProc := syscall.NewCallback(t.wndProc)

	hInstance := getModuleHandle(nil)
	logDebug("Run: hInstance=0x%X", hInstance)

	wc := &wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   wndProc,
		hInstance:     hInstance,
		hCursor:       loadArrowCursor(),
		lpszClassName: className,
	}
	atom, err := registerClassEx(wc)
	if err != nil || atom == 0 {
		logDebug("Run: RegisterClassEx 失败 err=%v lastErr=%d", err, getLastError())
		return err
	}
	logDebug("Run: RegisterClassEx ok atom=%d", atom)

	hwnd, err := createWindowEx(className, hInstance, 0, 0, 0, 0, 0)
	if err != nil {
		logDebug("Run: CreateWindowEx 失败 err=%v lastErr=%d", err, getLastError())
		return err
	}
	logDebug("Run: CreateWindowEx ok hwnd=0x%X", hwnd)
	t.mu.Lock()
	t.hwnd = hwnd
	t.mu.Unlock()

	// 图标：优先外部文件，失败回退到运行时绘制（灰）。
	icon := uintptr(0)
	external := false
	if t.iconPath != "" {
		if ic, err := loadImageIconFromFile(t.iconPath); err == nil && ic != 0 {
			icon = ic
			external = true
			logDebug("Run: LoadImage iconPath ok hicon=0x%X", ic)
		} else {
			logDebug("Run: LoadImage 失败 path=%q err=%v lastErr=%d", t.iconPath, err, getLastError())
		}
	}
	if icon == 0 {
		ic, err := makeIcon(0x9e9e9e)
		if err != nil {
			logDebug("Run: makeIcon 失败 err=%v", err)
			return err
		}
		icon = ic
		logDebug("Run: makeIcon fallback ok hicon=0x%X", ic)
	}
	t.mu.Lock()
	t.icon = icon
	t.externalIcon = external
	t.mu.Unlock()

	nid := t.buildNotifyData(icon)
	logDebug("Run: NOTIFYICONDATA cbSize=%d structSize=%d", nid.cbSize, unsafe.Sizeof(*nid))
	if !shellNotifyIcon(nimAdd, nid) {
		code := getLastError()
		logDebug("Run: Shell_NotifyIcon(NIM_ADD) 失败 lastErr=%s", lastErrText(code))
		return errShellNotify
	}
	logDebug("Run: Shell_NotifyIcon(NIM_ADD) ok")

	// 诊断：图标当前是否可见（GetRect 成功 = 有屏幕位置）。
	if rc, ok := notifyIconGetRect(hwnd, 1); ok {
		logDebug("Run: Shell_NotifyIconGetRect ok rect=%d,%d-%d,%d", rc.left, rc.top, rc.right, rc.bottom)
	} else {
		logDebug("Run: Shell_NotifyIconGetRect 失败 lastErr=%s (图标可能被折叠进托盘溢出区)", lastErrText(getLastError()))
	}

	// 启动气泡提示：明确告知用户程序已在系统托盘运行。
	t.showBalloon("kimi-hud 已在系统托盘运行", "点击图标查看生成速度 / 缓存命中率 / 订阅额度", 4000)

	t.mu.Lock()
	t.running = true
	t.mu.Unlock()

	// 消息循环。
	var m msg
	for {
		rc, err := getMessage(&m)
		if err != nil {
			break
		}
		if rc == 0 {
			break // WM_QUIT
		}
		translateMessage(&m)
		dispatchMessage(&m)
	}
	t.cleanup()
	return nil
}

// SetIconColor 从任意 goroutine 更新托盘图标颜色（RGB 0xRRGGBB）。
// 外部自定义图标存在时不替换（保持用户图标）。
func (t *Tray) SetIconColor(color uint32) {
	t.mu.Lock()
	if !t.running || t.hwnd == 0 || t.iconCol == color || t.externalIcon {
		t.mu.Unlock()
		return
	}
	t.iconCol = color
	hwnd := t.hwnd
	t.mu.Unlock()

	// 在窗口线程绘制并更新，避免跨线程 GDI 竞争。
	postMessage(hwnd, wmAppIconUpdate, 0, uintptr(color))
}

// Stop 结束消息循环（从任意 goroutine）。
func (t *Tray) Stop() {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		postMessage(hwnd, wmDestroy, 0, 0)
	}
}

var errShellNotify = syscall.Errno(0xFFFFFFFF)

func (t *Tray) wndProc(hwnd uintptr, msgID uint32, wParam, lParam uintptr) uintptr {
	switch msgID {
	case wmApp:
		// 托盘回调：lParam 为鼠标事件（左/右键点击都弹菜单）。
		switch uint32(lParam) {
		case wmRButtonUp, wmLButtonUp:
			t.showMenuPersistent(hwnd)
		}
		return 0
	case wmAppShowMenu:
		t.showMenuPersistent(hwnd)
		return 0
	case wmAppCloseMenu:
		// 轮询线程检测到鼠标移出：请求关闭当前菜单。
		// TrackPopupMenu 在模态循环中会派发本窗口消息，先置 closing 再发
		// WM_CANCELMODE 让系统取消模态菜单循环。
		t.closing = true
		postMessage(hwnd, wmCancelMode, 0, 0)
		return 0
	case wmCancelMode:
		// 交给 DefWindowProc：由系统真正取消正在进行的菜单跟踪
		// （拦截 return 0 会让 TrackPopupMenu 模态循环无法退出）。
		return defWindowProc(hwnd, msgID, wParam, lParam)
	case wmAppIconUpdate:
		// 外部自定义图标存在时保持不动（用户图标稳定显示，不做动态替换）。
		if t.hasExternalIcon() {
			return 0
		}
		color := uint32(lParam)
		icon, err := makeIcon(color)
		if err != nil || icon == 0 {
			logDebug("iconUpdate: makeIcon 失败 err=%v，保留旧图标", err)
			return 0
		}
		t.mu.Lock()
		old := t.icon
		t.icon = icon
		t.mu.Unlock()
		// 先更新成功，再销毁旧图标（失败则回滚，绝不留下空图标）。
		if shellNotifyIcon(nimModify, t.buildNotifyData(icon)) {
			if old != 0 && old != icon {
				destroyIcon(old)
			}
		} else {
			logDebug("iconUpdate: NIM_MODIFY 失败，回滚旧图标 lastErr=%s", lastErrText(getLastError()))
			t.mu.Lock()
			t.icon = old
			t.mu.Unlock()
			destroyIcon(icon)
		}
		return 0
	case wmMeasureItem:
		// owner-draw 菜单测量：返回每项的行高与统一菜单宽度。
		return t.onMeasureItem(lParam)
	case wmDrawItem:
		// owner-draw 菜单绘制：自绘自定义样式。
		return t.onDrawItem(lParam)
	case wmDestroy:
		postQuitMessage(0)
		return 0
	}
	return defWindowProc(hwnd, msgID, wParam, lParam)
}

func (t *Tray) buildNotifyData(icon uintptr) *notifyIconData {
	tip, _ := syscall.UTF16FromString("Kimi Token HUD")
	nid := &notifyIconData{
		hWnd:             t.hwnd,
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmApp,
		hIcon:            icon,
	}
	copy(nid.szTip[:], tip)
	nid.cbSize = uint32(unsafe.Sizeof(*nid))
	return nid
}

// hasExternalIcon 返回是否加载了外部自定义图标。
func (t *Tray) hasExternalIcon() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.externalIcon
}

// iconRect 返回托盘图标的屏幕矩形。
// showBalloon 通过 NIM_MODIFY + NIF_INFO 显示气泡提示。
func (t *Tray) showBalloon(title, text string, timeoutMS uint32) {
	t.mu.Lock()
	hwnd := t.hwnd
	icon := t.icon
	t.mu.Unlock()
	if hwnd == 0 || icon == 0 {
		return
	}
	nid := &notifyIconData{
		hWnd:             hwnd,
		uID:              1,
		uFlags:           nifInfo | nifIcon,
		uCallbackMessage: wmApp,
		hIcon:            icon,
		uTimeout:         timeoutMS,
		dwInfoFlags:      0x01, // NIIF_INFO
	}
	if timeoutMS == 0 {
		timeoutMS = 3000
	}
	nid.uTimeout = timeoutMS
	if info, err := syscall.UTF16FromString(text); err == nil {
		copy(nid.szInfo[:], info)
	}
	if ttl, err := syscall.UTF16FromString(title); err == nil {
		copy(nid.szInfoTitle[:], ttl)
	}
	nid.cbSize = uint32(unsafe.Sizeof(*nid))
	if !shellNotifyIcon(nimModify, nid) {
		logDebug("showBalloon: NIM_MODIFY 失败 lastErr=%s", lastErrText(getLastError()))
	} else {
		logDebug("showBalloon: NIM_MODIFY ok")
	}
}

// showMenuPersistent 弹出托盘菜单：
// - 点击菜单项：执行命令后关闭。
// - 鼠标移出图标（轮询线程发 wmAppCloseMenu）：菜单关闭。
// - 鼠标一直在图标上（未移出）：菜单保持显示。
// 防重入：已有菜单在显示时直接返回。
func (t *Tray) showMenuPersistent(hwnd uintptr) {
	t.mu.Lock()
	if t.menuOpen {
		t.mu.Unlock()
		return
	}
	t.menuOpen = true
	t.closing = false
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.menuOpen = false
		t.mu.Unlock()
	}()

	logDebug("showMenu: 开始构建菜单")
	items := t.handler.BuildMenu()

	menu, err := createPopupMenu()
	if err != nil || menu == 0 {
		logDebug("showMenu: createPopupMenu 失败 err=%v", err)
		return
	}
	defer destroyMenu(menu)

	// owner-draw 自绘菜单：每项以 MF_OWNERDRAW 追加，item data 指向 items 元素，
	// WM_MEASUREITEM/WM_DRAWITEM 据此自绘样式。items 在 TrackPopupMenu 期间
	// 保持存活（栈上引用），返回后用 KeepAlive 显式保活。
	t.owndraw.init(items, appsUseLightTheme())
	defer t.owndraw.release()

	idMap := map[uint32]int{}
	nextID := uint32(1)
	for i := range items {
		it := &items[i]
		flags := uint32(mfOwnerDraw)
		if it.Disabled {
			flags |= mfGrayed
		}
		// itemData 传下标索引（非 Go 指针），WM_DRAWITEM 回调经 owndraw.items 查表。
		appendMenuData(menu, flags, uintptr(nextID), uintptr(i))
		idMap[nextID] = it.ID
		nextID++
	}
	logDebug("showMenu: 已添加 %d 项，调用 TrackPopupMenu", len(items))

	// 托盘菜单标准哄骗序列：隐藏窗口的 SetForegroundWindow 受 Windows 前台锁定
	// 限制，否则 TrackPopupMenu 弹出的菜单不会获得前台激活——表现为"第一次点击
	// 外部只激活菜单而不关闭，第二次点击才关"。
	// 关键：模拟一次 Alt 键（simulateAltKey）给系统"用户输入"信号解除前台锁定，
	// 再 SetForegroundWindow 即成功。绝不能显示/移动隐藏窗口（SW_SHOWNA 或
	// SWP_SHOWWINDOW 会把 0x0 窗口显示在屏幕左上角，见 git 历史）。
	// 置顶 → Alt 模拟 → SetForegroundWindow → 给自己 PostMessage(WM_NULL) 唤醒
	// 登记交互 → 第二次 SetForegroundWindow（此时通常成功）→ TrackPopupMenu。
	// 必须带 TPM_NONOTIFY|TPM_RETURNCMD：让 TrackPopupMenu 返回所选项 ID 而非
	// 走 WM_COMMAND 通知（通知路径依赖窗口激活，隐藏窗口下会弹不出并阻塞窗口线程）。
	setWindowPosTopmost(hwnd)
	simulateAltKey()
	setForegroundWindow(hwnd)
	postMessage(hwnd, wmNull, 0, 0)
	setForegroundWindow(hwnd)

	var pt point
	if err := getCursorPos(&pt); err != nil {
		return
	}

	// 模态菜单循环：TrackPopupMenu 内部会派发本窗口消息（含 wmAppCloseMenu），
	// 因此轮询线程发来的关闭请求可在此被处理。
	cmd := trackPopupMenu(menu, tpmRightButton|tpmReturnCmd|tpmNonotify, pt.x, pt.y, hwnd)
	logDebug("showMenu: TrackPopupMenu 返回 cmd=%d", cmd)

	t.mu.Lock()
	closing := t.closing
	t.mu.Unlock()

	// 关闭请求来自"鼠标移出"：直接退出（不执行任何命令）。
	if closing {
		logDebug("showMenu: 收到关闭请求，关闭菜单")
		setForegroundWindow(hwnd)
		return
	}

	// 用户点击了菜单项：执行命令。
	if cmd != 0 {
		setForegroundWindow(hwnd)
		if id, ok := idMap[cmd]; ok {
			t.handler.OnCommand(id)
		}
		return
	}

	// cmd==0 且无关闭请求：用户按 Esc 或点击外部自行关闭，直接退出。
	setForegroundWindow(hwnd)
}

func (t *Tray) cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hwnd != 0 {
		shellNotifyIcon(nimDelete, t.buildNotifyData(0))
	}
	if t.icon != 0 {
		destroyIcon(t.icon)
		t.icon = 0
	}
	t.running = false
}

// WindowClassName 隐藏窗口的类名（供 FindHudWindow / 单实例查找）。
const WindowClassName = "KimiTokenHudTrayWnd"

// 包级标记：logDebug 首次调用时截断日志文件，之后追加。
var logOnce sync.Once

// logDebug 追加调试日志到 %USERPROFILE%\.kimi-code-hud\debug.log（定位问题用）。
func logDebug(format string, args ...any) {
	dir := filepath.Join(os.Getenv("USERPROFILE"), ".kimi-code-hud")
	_ = os.MkdirAll(dir, 0o755)
	logOnce.Do(func() {
		_ = os.WriteFile(filepath.Join(dir, "debug.log"), nil, 0o644)
	})
	f, err := os.OpenFile(filepath.Join(dir, "debug.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	fmt.Fprintf(f, "[%s] %s\n", ts, fmt.Sprintf(format, args...))
}

// FindHudWindow 查找已运行实例的窗口句柄；无实例返回 0。
func FindHudWindow() uintptr {
	cls, _ := syscall.UTF16PtrFromString(WindowClassName)
	return findWindow(cls)
}

// RequestQuit 通知已运行实例退出（与菜单「退出」一致，投递 wmDestroy）。
// 注意不能用 WM_CLOSE：本窗口以 style=0 创建，DefWindowProc 对非
// overlapped/popup 窗口会直接忽略 WM_CLOSE，导致消息循环永不退出。
func RequestQuit() bool {
	hwnd := FindHudWindow()
	if hwnd == 0 {
		return false
	}
	postMessage(hwnd, wmDestroy, 0, 0)
	return true
}
