package tray

import (
	"testing"
)

func TestMakeIconValid(t *testing.T) {
	hicon, err := makeIcon(0x43a047)
	if err != nil {
		t.Fatalf("makeIcon 失败: %v", err)
	}
	if hicon == 0 {
		t.Fatal("makeIcon 返回空 HICON")
	}
	t.Logf("makeIcon 成功 HICON=0x%X", hicon)
	destroyIcon(hicon)
}

func TestMakeIconMultiple(t *testing.T) {
	// 连续多次创建销毁，验证资源无泄漏/崩溃。
	for i := 0; i < 50; i++ {
		hicon, err := makeIcon(0x123456)
		if err != nil || hicon == 0 {
			t.Fatalf("第 %d 次 makeIcon 失败: %v", i, err)
		}
		destroyIcon(hicon)
	}
}
