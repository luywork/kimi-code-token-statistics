package webview

import (
	"syscall"
	"unsafe"

	"github.com/zzl/go-win32api/v2/win32"
)

// GetBrowserVersion 包装 GetAvailableCoreWebView2BrowserVersionString（空参数=默认 Runtime）。
// 返回 HRESULT；version 为空表示 Runtime 缺失。
func GetBrowserVersion(version *string) uintptr {
	mod := syscall.NewLazyDLL("WebView2Loader.dll")
	proc := mod.NewProc("GetAvailableCoreWebView2BrowserVersionString")
	var pwszVersionInfo win32.PWSTR
	ret, _, _ := syscall.SyscallN(proc.Addr(),
		uintptr(win32.StrToPointer("")),
		uintptr(unsafe.Pointer(&pwszVersionInfo)),
	)
	if pwszVersionInfo != nil {
		*version = win32.PwstrToStr(pwszVersionInfo)
		win32.CoTaskMemFree(unsafe.Pointer(pwszVersionInfo))
	} else {
		*version = ""
	}
	return ret
}
