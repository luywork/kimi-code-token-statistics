//go:build windows

package webview

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

// 验证 loadClassIcons 从 exe 提取的窗口类图标有效（非 0 且 GetIconInfo 可取到
// 颜色位图）。回归点：详情窗口标题栏无系统图标 = 窗口类 hIcon 为 0。
// 测试二进制自身无图标资源，因此从已构建的 build/kimi-hud.exe 提取验证；
// 该文件缺失时跳过（构建产物未生成）。
func TestLoadClassIcons(t *testing.T) {
	exe := filepath.Join("..", "..", "build", "kimi-hud.exe")
	if _, err := os.Stat(exe); err != nil {
		t.Skipf("build/kimi-hud.exe 不存在，跳过（err=%v）", err)
	}

	big, small := loadClassIcons(exe)
	if big == 0 && small == 0 {
		t.Fatalf("loadClassIcons(%s) 未提取到任何图标", exe)
	}
	defer destroyClassIcons(big, small)

	user32 := syscall.NewLazyDLL("user32.dll")
	procGetIconInfo := user32.NewProc("GetIconInfo")

	assertValid := func(label string, hIcon uintptr) {
		if hIcon == 0 {
			t.Fatalf("%s: 图标句柄为 0", label)
		}
		info := struct {
			fIcon    uint32
			xHotspot uint32
			yHotspot uint32
			hbmMask  uintptr
			hbmColor uintptr
		}{}
		ok, _, _ := procGetIconInfo.Call(hIcon, uintptr(unsafe.Pointer(&info)))
		if ok == 0 {
			t.Fatalf("%s: GetIconInfo 失败（句柄 0x%X 无效）", label, hIcon)
		}
		if info.hbmColor == 0 {
			t.Fatalf("%s: 图标无颜色位图（非有效彩色图标）", label)
		}
		t.Logf("%s: 有效图标 hIcon=0x%X", label, hIcon)
	}

	assertValid("big(32px)", big)
	assertValid("small(16px)", small)
}
