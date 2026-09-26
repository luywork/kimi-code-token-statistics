// Package webview 封装 WebView2 三层生命周期（宿主窗口/视图/Environment），
// 供详情窗口使用。绑定库：zzl/go-webview2（完整 COM 绑定，见 M0-SPIKE 结论）。
package webview

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed WebView2Loader.dll
var loaderDLL []byte

// EnsureLoader 将内嵌的 WebView2Loader.dll 解压到临时目录并加入 DLL 搜索路径。
// zzl 的 wv2 包通过 syscall.NewLazyDLL("WebView2Loader.dll") 加载（首次调用时，
// LoadLibrary 按 exe 目录/系统目录/当前目录/PATH 搜索）——在调用任何 wv2 函数前
// 执行本函数即可让加载器找到 DLL。冷路径（每次进程仅执行一次），解压成本可忽略。
func EnsureLoader() error {
	dir := filepath.Join(os.TempDir(), "kimi-hud-wv2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "WebView2Loader.dll")
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, loaderDLL, 0o644); err != nil {
			return err
		}
	}
	// prepend 到 PATH，确保 LoadLibrary 命中（即使 exe 目录不可写也无需修改 exe 位置）。
	cur := os.Getenv("PATH")
	if !containsPathEntry(cur, dir) {
		sep := ";"
		_ = os.Setenv("PATH", dir+sep+cur)
	}
	return nil
}

func containsPathEntry(path, entry string) bool {
	for _, p := range strings.Split(path, ";") {
		if strings.TrimRight(p, `\/`) == strings.TrimRight(entry, `\/`) {
			return true
		}
	}
	return false
}

// RuntimeAvailable 检测系统 WebView2 Runtime 是否可用（版本字符串非空）。
// 复用 zzl 的 GetAvailableCoreWebView2BrowserVersionString（需先 EnsureLoader）。
func RuntimeAvailable() bool {
	if err := EnsureLoader(); err != nil {
		return false
	}
	var version string
	hr := GetBrowserVersion(&version)
	return hr == 0 && version != ""
}
