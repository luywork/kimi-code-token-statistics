package tray

// winapi.go 定义托盘实现所需的 Win32 API（user32/gdi32/shell32）与结构体。
// 不引入 CGO，通过系统 DLL 懒加载调用。

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")
	shell  = syscall.NewLazyDLL("shell32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procPostMessage         = user32.NewProc("PostMessageW")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procLoadImageW          = user32.NewProc("LoadImageW")
	procGetLastError        = syscall.NewLazyDLL("kernel32.dll").NewProc("GetLastError")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procKeybdEvent          = user32.NewProc("keybd_event")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetTimer            = user32.NewProc("SetTimer")
	procKillTimer           = user32.NewProc("KillTimer")
	procTrackMouseEvent     = user32.NewProc("TrackMouseEvent")
	procCreateIconIndirect  = user32.NewProc("CreateIconIndirect")
	procDestroyIcon         = user32.NewProc("DestroyIcon")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procLoadCursorW         = user32.NewProc("LoadCursorW")

	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procSelectObject       = gdi32.NewProc("SelectObject")

	// owner-draw 菜单绘制（GDI + FillRect 在 user32）。
	procCreateFontIndirectW = gdi32.NewProc("CreateFontIndirectW")
	procCreateSolidBrush    = gdi32.NewProc("CreateSolidBrush")
	procFillRect            = user32.NewProc("FillRect")
	procRoundRect           = gdi32.NewProc("RoundRect")
	procSetTextColor        = gdi32.NewProc("SetTextColor")
	procSetBkMode           = gdi32.NewProc("SetBkMode")
	procDrawTextW           = user32.NewProc("DrawTextW")
	procGetTextExtentPointW = gdi32.NewProc("GetTextExtentPoint32W")
	procGetDeviceCaps       = gdi32.NewProc("GetDeviceCaps")
	procCreatePen           = gdi32.NewProc("CreatePen")
	procEllipse             = gdi32.NewProc("Ellipse")
	procSaveDC              = gdi32.NewProc("SaveDC")
	procRestoreDC           = gdi32.NewProc("RestoreDC")
	procIntersectClipRect   = gdi32.NewProc("IntersectClipRect")

	procRegGetValueW = syscall.NewLazyDLL("advapi32.dll").NewProc("RegGetValueW")

	procShellNotifyIcon   = shell.NewProc("Shell_NotifyIconW")
	procNotifyIconGetRect = shell.NewProc("Shell_NotifyIconGetRect")
	procExtractIconExW    = shell.NewProc("ExtractIconExW")
)

// ---- 常量 ----
const (
	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010

	wmDestroy    = 0x0002
	wmClose      = 0x0010
	wmCancelMode = 0x001F
	wmCommand    = 0x0111
	wmRButtonUp  = 0x0205
	wmLButtonUp  = 0x0202
	wmMouseMove  = 0x0200
	wmMouseLeave = 0x02A3
	wmTimer      = 0x0113
	wmNull       = 0x0000
	wmApp        = 0x8000

	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfGrayed    = 0x00000001
	mfByCommand = 0x00000000

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmNonotify    = 0x0080

	idcArrow = 32512

	// 自定义消息
	wmAppIconUpdate = wmApp + 1
	wmAppShowMenu   = wmApp + 2
	wmAppCloseMenu  = wmApp + 3
)

// ---- 结构体 ----
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

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type point struct {
	x int32
	y int32
}

// trackMouseEvent 结构体（TME_LEAVE 跟踪）。
type trackMouseEvent struct {
	cbSize      uint32
	dwFlags     uint32
	hwndTrack   uintptr
	dwHoverTime uint32
}

// TME_LEAVE：鼠标离开窗口时发送 WM_MOUSELEAVE。
const tmeLeave = 0x00000002

type notifyIconData struct {
	cbSize           uint32
	pad0             uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	pad1             uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uTimeout         uint32
	pad2             uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	pad3             uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

type bitmapInfoHeader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type bitmapInfo struct {
	bmiHeader bitmapInfoHeader
}

type iconInfo struct {
	fIcon    uint32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

// ---- API 封装 ----

func registerClassEx(wc *wndClassEx) (uint16, error) {
	r, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(wc)))
	if r == 0 {
		return 0, e
	}
	return uint16(r), nil
}

func createWindowEx(className *uint16, hInstance uintptr, style uint32, x, y, w, h int32) (uintptr, error) {
	r, _, e := procCreateWindowExW.Call(
		0, // dwExStyle
		uintptr(unsafe.Pointer(className)),
		0, // lpWindowName
		uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		0, // hWndParent
		0, // hMenu
		hInstance,
		0, // lpParam
	)
	if r == 0 {
		return 0, e
	}
	return r, nil
}

func defWindowProc(hwnd uintptr, msgID uint32, wParam, lParam uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msgID), wParam, lParam)
	return r
}

func getMessage(m *msg) (int32, error) {
	r, _, e := procGetMessageW.Call(
		uintptr(unsafe.Pointer(m)),
		0, 0, 0,
	)
	if r == 0xFFFFFFFF { // -1
		return -1, e
	}
	return int32(r), nil
}

func translateMessage(m *msg) {
	procTranslateMessage.Call(uintptr(unsafe.Pointer(m)))
}

func dispatchMessage(m *msg) uintptr {
	r, _, _ := procDispatchMessageW.Call(uintptr(unsafe.Pointer(m)))
	return r
}

func postQuitMessage(exitCode int32) {
	procPostQuitMessage.Call(uintptr(exitCode))
}

func postMessage(hwnd uintptr, msgID uint32, wParam, lParam uintptr) {
	procPostMessage.Call(hwnd, uintptr(msgID), wParam, lParam)
}

// 加载图标常量
const (
	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	lrDefaultSize  = 0x00000040
)

// loadImageIconFromFile 从 .ico/.exe/.dll 文件加载 HICON。
// cx/cy 为 0 时配合 lrDefaultSize 使用系统默认尺寸（16）。
// 先从文件 LoadImage（兼容 .ico）；失败时用 ExtractIconEx 提取 exe 图标
// （任务管理器/Explorer 的同一机制，保证托盘与进程图标一致）。
func loadImageIconFromFile(path string) (uintptr, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	r, _, e := procLoadImageW.Call(
		0,
		uintptr(unsafe.Pointer(p)),
		imageIcon,
		0,
		0,
		lrLoadFromFile|lrDefaultSize,
	)
	if r != 0 {
		return r, nil
	}
	// LoadImage 失败：用 ExtractIconEx 提取 exe 内嵌图标。任务管理器进程列表
	// 与托盘标准尺寸同为 small（16px），返回 small 保证两处图标完全一致。
	var big, small uintptr
	n, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1)
	if n == 0 {
		// 文件不可加载也不可提取，返回 LoadImage 的错误。
		return 0, e
	}
	if small != 0 {
		if big != 0 {
			destroyIcon(big)
		}
		return small, nil
	}
	if big != 0 {
		return big, nil
	}
	return 0, e
}

// findWindow 按类名查找隐藏窗口句柄。
func findWindow(className *uint16) uintptr {
	r, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(className)), 0)
	return r
}

func getModuleHandle(name *uint16) uintptr {
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(uintptr(unsafe.Pointer(name)))
	return r
}

func createPopupMenu() (uintptr, error) {
	r, _, e := procCreatePopupMenu.Call()
	if r == 0 {
		return 0, e
	}
	return r, nil
}

func appendMenu(menu uintptr, flags uint32, id uintptr, text *uint16) error {
	r, _, e := procAppendMenuW.Call(menu, uintptr(flags), id, uintptr(unsafe.Pointer(text)))
	if r == 0 {
		return e
	}
	return nil
}

// appendMenuData 以 item data 追加菜单项（MF_OWNERDRAW 时 lpNewItem 为 app 数据指针）。
func appendMenuData(menu uintptr, flags uint32, id uintptr, data uintptr) error {
	r, _, e := procAppendMenuW.Call(menu, uintptr(flags), id, data)
	if r == 0 {
		return e
	}
	return nil
}

func trackPopupMenu(menu uintptr, flags uint32, x, y int32, hwnd uintptr) uint32 {
	r, _, _ := procTrackPopupMenu.Call(menu, uintptr(flags), uintptr(x), uintptr(y), 0, hwnd, 0)
	return uint32(r)
}

func destroyMenu(menu uintptr) {
	procDestroyMenu.Call(menu)
}

func setForegroundWindow(hwnd uintptr) {
	procSetForegroundWindow.Call(hwnd)
}

// simulateAltKey 模拟一次 Alt 键按下+松开，给系统一个"用户输入"信号以解除
// Windows 前台锁定（foreground lock），使隐藏窗口的 SetForegroundWindow 生效。
// 托盘应用标准做法，不显示/移动任何窗口。
func simulateAltKey() {
	procKeybdEvent.Call(vkMenu, 0, 0, 0)              // Alt 按下
	procKeybdEvent.Call(vkMenu, 0, keyEventFKeyUp, 0) // Alt 松开
}

// 虚拟键与按键标志。
const (
	vkMenu         = 0x12 // VK_MENU
	keyEventFKeyUp = 0x02 // KEYEVENTF_KEYUP
)

// setWindowPosTopmost 将窗口置顶但不显示、不激活。
// 注意：绝不能带 SWP_SHOWWINDOW，否则会把隐藏的消息窗口显示在屏幕左上角。
func setWindowPosTopmost(hwnd uintptr) {
	const (
		hwndTopmost   = ^uintptr(0) // -1
		swpNoActivate = 0x0010
		swpNoMove     = 0x0002
		swpNoSize     = 0x0001
	)
	procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoActivate|swpNoMove|swpNoSize)
}

func getCursorPos(p *point) error {
	r, _, e := procGetCursorPos.Call(uintptr(unsafe.Pointer(p)))
	if r == 0 {
		return e
	}
	return nil
}

// setTimer 为窗口创建定时器（elapse 毫秒，周期触发）。
func setTimer(hwnd uintptr, id uintptr, elapse uint32) bool {
	r, _, _ := procSetTimer.Call(hwnd, id, uintptr(elapse), 0)
	return r != 0
}

func killTimer(hwnd uintptr, id uintptr) bool {
	r, _, _ := procKillTimer.Call(hwnd, id)
	return r != 0
}

// trackMouseLeave 请求系统跟踪鼠标离开，离开时向 hwnd 发 WM_MOUSELEAVE。
func trackMouseLeave(hwnd uintptr) {
	tme := &trackMouseEvent{
		cbSize:    uint32(unsafe.Sizeof(trackMouseEvent{})),
		dwFlags:   tmeLeave,
		hwndTrack: hwnd,
	}
	procTrackMouseEvent.Call(uintptr(unsafe.Pointer(tme)))
}

func createIconIndirect(info *iconInfo) (uintptr, error) {
	r, _, e := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(info)))
	if r == 0 {
		return 0, e
	}
	return r, nil
}

func destroyIcon(hIcon uintptr) {
	if hIcon != 0 {
		procDestroyIcon.Call(hIcon)
	}
}

func getDC(hwnd uintptr) uintptr {
	r, _, _ := procGetDC.Call(hwnd)
	return r
}

func releaseDC(hwnd, hdc uintptr) {
	procReleaseDC.Call(hwnd, hdc)
}

func loadArrowCursor() uintptr {
	r, _, _ := procLoadCursorW.Call(0, uintptr(idcArrow))
	return r
}

func createDIBSection(hdc uintptr, bmi *bitmapInfo, bits **byte) (uintptr, error) {
	r, _, e := procCreateDIBSection.Call(hdc, uintptr(unsafe.Pointer(bmi)), 0, uintptr(unsafe.Pointer(bits)), 0, 0)
	if r == 0 {
		return 0, e
	}
	return r, nil
}

func createCompatibleDC(hdc uintptr) uintptr {
	r, _, _ := procCreateCompatibleDC.Call(hdc)
	return r
}

func deleteDC(hdc uintptr) {
	if hdc != 0 {
		procDeleteDC.Call(hdc)
	}
}

func deleteObject(obj uintptr) {
	if obj != 0 {
		procDeleteObject.Call(obj)
	}
}

func selectObject(hdc, obj uintptr) uintptr {
	r, _, _ := procSelectObject.Call(hdc, obj)
	return r
}

func shellNotifyIcon(message uint32, nid *notifyIconData) bool {
	r, _, _ := procShellNotifyIcon.Call(uintptr(message), uintptr(unsafe.Pointer(nid)))
	return r != 0
}

type notifyIconIdentifier struct {
	cbSize   uint32
	pad0     uint32
	hWnd     uintptr
	uID      uint32
	pad1     uint32
	guidItem [16]byte
}

type rect struct {
	left   int32
	top    int32
	right  int32
	bottom int32
}

// notifyIconGetRect 查询托盘图标当前屏幕矩形。
// Shell_NotifyIconGetRect 返回 HRESULT：0 = S_OK 成功，负数 = 失败。
// 注意返回值语义与大多数 Win32 BOOL API 相反，不能写成 r != 0。
func notifyIconGetRect(hwnd uintptr, uID uint32) (rect, bool) {
	id := &notifyIconIdentifier{
		cbSize: uint32(unsafe.Sizeof(notifyIconIdentifier{})),
		hWnd:   hwnd,
		uID:    uID,
	}
	var rc rect
	r, _, _ := procNotifyIconGetRect.Call(
		uintptr(unsafe.Pointer(id)),
		uintptr(unsafe.Pointer(&rc)),
	)
	return rc, r == 0
}

// getLastError 返回最近一次 API 调用错误码。
func getLastError() uint32 {
	r, _, _ := procGetLastError.Call()
	return uint32(r)
}

// lastErrText 错误码转可读文本。
func lastErrText(code uint32) string {
	switch code {
	case 0:
		return "NO_ERROR"
	case 1400:
		return "ERROR_INVALID_WINDOW_HANDLE"
	case 1406:
		return "ERROR_INVALID_MENU_HANDLE"
	case 1407:
		return "ERROR_INVALID_CURSOR_HANDLE"
	case 87:
		return "ERROR_INVALID_PARAMETER"
	default:
		return fmt.Sprintf("code=%d", code)
	}
}

// ---- owner-draw 菜单（自绘菜单）----

const (
	wmMeasureItem   = 0x002C
	wmDrawItem      = 0x002B
	wmInitMenuPopup = 0x0117

	mfOwnerDraw = 0x00000100

	odtMenu = 1

	odsSelected = 0x0001
	odsGrayed   = 0x0002
	odsHotLight = 0x0040
)

// measureItemStruct 对齐 MEASUREITEMSTRUCT（owner-draw 菜单测量）。
type measureItemStruct struct {
	ctlType    uint32
	ctlID      uint32
	itemID     uint32
	itemWidth  uint32
	itemHeight uint32
	itemData   uintptr
}

// drawItemStruct 对齐 DRAWITEMSTRUCT（owner-draw 菜单绘制）。
type drawItemStruct struct {
	ctlType    uint32
	ctlID      uint32
	itemID     uint32
	itemAction uint32
	itemState  uint32
	hwndItem   uintptr
	hDC        uintptr
	rcItem     rect
	itemData   uintptr
}

// logFontW 对齐 LOGFONTW。
type logFontW struct {
	lfHeight         int32
	lfWidth          int32
	lfEscapement     int32
	lfOrientation    int32
	lfWeight         int32
	lfItalic         byte
	lfUnderline      byte
	lfStrikeOut      byte
	lfCharSet        byte
	lfOutPrecision   byte
	lfClipPrecision  byte
	lfQuality        byte
	lfPitchAndFamily byte
	lfFaceName       [32]uint16
}

const (
	fwNormal    = 400
	fwSemibold  = 600
	defaultChar = 1 // DEFAULT_CHARSET
	outTtOnly   = 4 // OUT_TT_ONLY
	clipDefault = 0
	clearTypeQ  = 5 // CLEARTYPE_QUALITY
	fixedPitch  = 1 // FIXED_PITCH

	transparent = 1 // TRANSPARENT (SetBkMode)
	nullPen     = 0x0008

	dtLeft        = 0x00000000
	dtRight       = 0x00000002
	dtVcenter     = 0x00000004
	dtSingleLine  = 0x00000020
	dtCalcRect    = 0x00000400
	dtNoPrefix    = 0x00001000
	dtEndEllipsis = 0x00008000

	logPixelsY = 90 // LOGPIXELSY

	rrfRtRegDword   = 0x00000018 // RRF_RT_REG_DWORD
	hkeyCurrentUser = 0x80000001
)

func createFontIndirect(lf *logFontW) uintptr {
	r, _, _ := procCreateFontIndirectW.Call(uintptr(unsafe.Pointer(lf)))
	return r
}

func createSolidBrush(color uint32) uintptr {
	// COLORREF 为 0x00BBGGRR，入参 RGB 需翻转字节序。
	r, _, _ := procCreateSolidBrush.Call(uintptr(rgbToColorref(color)))
	return r
}

func fillRect(hdc uintptr, rc *rect, brush uintptr) {
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(rc)), brush)
}

// roundRectFilled 用纯色画刷填充圆角矩形（NULL_PEN 无描边）。
// 注意：RoundRect 使用 DC 当前选入的画笔/画刷，必须先 SelectObject 选入。
func roundRectFilled(hdc uintptr, rc *rect, brush uintptr, radius int32) {
	oldPen := selectObject(hdc, createNullPen())
	oldBrush := selectObject(hdc, brush)
	procRoundRect.Call(hdc, uintptr(rc.left), uintptr(rc.top), uintptr(rc.right), uintptr(rc.bottom), uintptr(radius), uintptr(radius))
	selectObject(hdc, oldBrush)
	selectObject(hdc, oldPen)
	deleteObject(oldPen)
}

func createNullPen() uintptr {
	// PS_NULL = 0x0008：不画边框，仅依赖画刷填充。
	r, _, _ := procCreatePen.Call(uintptr(nullPen), 1, 0)
	return r
}

// intersectClipRect 将裁剪区与给定矩形求交，用于进度条前景按比例截断。
func intersectClipRect(hdc uintptr, left, top, right, bottom int32) int32 {
	r, _, _ := procIntersectClipRect.Call(hdc, uintptr(left), uintptr(top), uintptr(right), uintptr(bottom))
	return int32(r)
}

// utf16Of 返回字符串的 UTF-16 序列（含结尾 NUL），供 Win32 字面量使用。
func utf16Of(s string) []uint16 {
	u, _ := syscall.UTF16FromString(s)
	return u
}

func setTextColor(hdc uintptr, color uint32) {
	procSetTextColor.Call(hdc, uintptr(rgbToColorref(color)))
}

func setBkMode(hdc uintptr, mode uint32) {
	procSetBkMode.Call(hdc, uintptr(mode))
}

func drawText(hdc uintptr, text string, rc *rect, flags uint32) int32 {
	t, _ := syscall.UTF16FromString(text)
	r, _, _ := procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&t[0])), uintptr(len(t)-1), uintptr(unsafe.Pointer(rc)), uintptr(flags))
	return int32(r)
}

func textExtent(hdc uintptr, text string) (int32, int32) {
	t, _ := syscall.UTF16FromString(text)
	var sz struct {
		cx int32
		cy int32
	}
	procGetTextExtentPointW.Call(hdc, uintptr(unsafe.Pointer(&t[0])), uintptr(len(t)-1), uintptr(unsafe.Pointer(&sz)))
	return sz.cx, sz.cy
}

func getDeviceCaps(hdc uintptr, index int32) int32 {
	r, _, _ := procGetDeviceCaps.Call(hdc, uintptr(index))
	return int32(r)
}

// ellipseFilled 用纯色画刷填充实心椭圆（NULL_PEN 无描边），参数为边界矩形。
// 注意：Ellipse 使用 DC 当前选入的画笔/画刷，必须先 SelectObject 选入。
func ellipseFilled(hdc uintptr, left, top, right, bottom int32, brush uintptr) {
	oldPen := selectObject(hdc, createNullPen())
	oldBrush := selectObject(hdc, brush)
	procEllipse.Call(hdc, uintptr(left), uintptr(top), uintptr(right), uintptr(bottom))
	selectObject(hdc, oldBrush)
	selectObject(hdc, oldPen)
	deleteObject(oldPen)
}

func saveDC(hdc uintptr) int32 {
	r, _, _ := procSaveDC.Call(hdc)
	return int32(r)
}

func restoreDC(hdc uintptr, saved int32) {
	procRestoreDC.Call(hdc, uintptr(saved))
}

// rgbToColorref 将 RGB(0xRRGGBB) 转为 GDI COLORREF(0x00BBGGRR)。
func rgbToColorref(rgb uint32) uint32 {
	return (rgb&0xFF)<<16 | (rgb & 0xFF00) | (rgb >> 16)
}

// appsUseLightTheme 读取系统深浅色偏好（AppsUseLightTheme 注册表键）。
// 返回 true=浅色，false=深色；读取失败时默认浅色。
func appsUseLightTheme() bool {
	var value uint32
	size := uint32(4)
	procRegGetValueW.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(mustUTF16Ptr(`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`))),
		uintptr(unsafe.Pointer(mustUTF16Ptr("AppsUseLightTheme"))),
		uintptr(rrfRtRegDword),
		0,
		uintptr(unsafe.Pointer(&value)),
		uintptr(unsafe.Pointer(&size)),
	)
	return value != 0
}

func mustUTF16Ptr(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s)
	return p
}
