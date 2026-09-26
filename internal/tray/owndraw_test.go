package tray

// owndraw_test.go owner-draw 菜单冒烟测试：在内存 DC 上实际执行测量+绘制路径，
// 验证 GDI 调用不崩溃、背景按主题填充（非全黑/全白），并在内存中逐像素校验。

import (
	"testing"
	"unsafe"
)

const (
	smokeW = 320
	smokeH = 240
)

func smokeDC(t *testing.T) (uintptr, *byte) {
	t.Helper()
	hdc := getDC(0)
	if hdc == 0 {
		t.Fatal("getDC failed")
	}
	t.Cleanup(func() { releaseDC(0, hdc) })

	memDC := createCompatibleDC(hdc)
	if memDC == 0 {
		t.Fatal("createCompatibleDC failed")
	}
	t.Cleanup(func() { deleteDC(memDC) })

	bmi := &bitmapInfo{}
	bmi.bmiHeader.biSize = uint32(unsafe.Sizeof(bmi.bmiHeader))
	bmi.bmiHeader.biWidth = smokeW
	bmi.bmiHeader.biHeight = -smokeH
	bmi.bmiHeader.biPlanes = 1
	bmi.bmiHeader.biBitCount = 32
	var bits *byte
	hbm, err := createDIBSection(memDC, bmi, &bits)
	if err != nil || hbm == 0 {
		t.Fatalf("createDIBSection failed: %v", err)
	}
	t.Cleanup(func() { deleteObject(hbm) })
	selectObject(memDC, hbm)
	return memDC, bits
}

func smokeItems() []MenuItem {
	p50 := 0.5
	return []MenuItem{
		{Text: "Kimi Code HUD", Kind: KindTitle, Disabled: true},
		{Text: "生成速度  ⚡ 156 t/s", Kind: KindValue, Color: 0x2563eb, Disabled: true},
		{Text: "缓存命中  Cache 82%", Kind: KindValue, Color: 0x047857, Disabled: true},
		{Text: "今日用量（全部模型）", Kind: KindHeading, Disabled: true},
		{Text: "token 总计 11.6万", Kind: KindValue, Color: 0x2563eb, Disabled: true},
		{Text: "5h  42%", Kind: KindValue, Color: 0x43a047, Progress: &p50, Sub: "~2h18m", Disabled: true},
		{Separator: true},
		{Text: "查看详细用量…", Kind: KindAction, ID: 1, Color: 0x2563eb},
		{Text: "退出", Kind: KindAction, ID: 2, Color: 0xd33b3b},
	}
}

// pixelAt 读取 32bpp 位图像素（XRGB）。
func pixelAt(bits *byte, x, y int) uint32 {
	off := (y*smokeW + x) * 4
	p := (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(bits)) + uintptr(off)))
	return *p
}

func TestOwndrawRendersAllKinds(t *testing.T) {
	hdc, bits := smokeDC(t)

	items := smokeItems()
	st := &owndrawState{}
	st.init(items, true)
	defer st.release()

	if !st.valid() {
		t.Fatal("owndraw state invalid after init")
	}

	// 逐项测量并绘制，覆盖所有 Kind 与 hover/disabled 状态。
	y := int32(0)
	for i := range items {
		h := int32(menuItemHeight(items[i].Kind, items[i].Separator))
		rc := rect{left: 0, top: y, right: int32(st.menuWidth), bottom: y + h}
		state := uint32(0)
		if items[i].Kind == KindAction {
			state = odsSelected // 模拟 hover 高亮
		}
		dis := &drawItemStruct{ctlType: odtMenu, itemID: uint32(i + 1), itemState: state, hDC: hdc, rcItem: rc, itemData: uintptr(i)}
		drawMenuItem(dis, &items[i], st)
		y += h
	}

	// 冒烟断言：标题行右侧空白区应为主题背景色（浅色 0xFFFFFF → XRGB 0x00FFFFFF）。
	// 注意 GDI 32bpp 位图存储为 BGRX，读回需按字节组装。
	// 绘制区域宽度 = st.menuWidth（非整个位图宽），坐标须在其内。
	b := func(x, y int) uint32 {
		v := pixelAt(bits, x, y)
		return v & 0xFFFFFF
	}
	if got := b(int(st.menuWidth)-4, 6); got != 0xFFFFFF {
		t.Fatalf("title row background = 0x%06X, want 0xFFFFFF", got)
	}
	// 品牌圆点（蓝 0x2563EB）应在标题行左侧。
	if got := b(15, 17); got != 0x2563EB {
		t.Fatalf("brand dot = 0x%06X, want 0x2563EB", got)
	}
	// 底部 action 行 hover 圆角高亮区域应为 hover 色（浅色 0xEAF1FE）。
	actionH := int32(menuItemHeight(KindAction, false))
	// 最后一行的 hover 圆角中心位于 y 方向中部。
	if got := b(30, int(y)-int(actionH)/2); got != 0xEAF1FE {
		t.Fatalf("action hover highlight = 0x%06X, want 0xEAF1FE", got)
	}
}

func TestOwndrawMeasureHeights(t *testing.T) {
	items := smokeItems()
	st := &owndrawState{}
	st.init(items, true)
	defer st.release()

	tr := &Tray{}
	tr.owndraw = *st // 状态引用，模拟窗口线程持有
	defer func() { tr.owndraw = owndrawState{} }()

	// 验证 onMeasureItem 对每项返回正确高度与统一宽度。
	for i := range items {
		mis := &measureItemStruct{ctlType: odtMenu, itemID: uint32(i + 1), itemData: uintptr(i)}
		ret := tr.onMeasureItem(uintptr(unsafe.Pointer(mis)))
		if ret != 1 {
			t.Fatalf("onMeasureItem[%d] returned %d, want 1", i, ret)
		}
		want := menuItemHeight(items[i].Kind, items[i].Separator)
		if mis.itemHeight != want {
			t.Fatalf("onMeasureItem[%d] height = %d, want %d", i, mis.itemHeight, want)
		}
		if mis.itemWidth != uint32(st.menuWidth) {
			t.Fatalf("onMeasureItem[%d] width = %d, want %d", i, mis.itemWidth, st.menuWidth)
		}
	}
}

func TestOwndrawDarkTheme(t *testing.T) {
	hdc, bits := smokeDC(t)
	items := smokeItems()
	st := &owndrawState{}
	st.init(items, false) // 深色
	defer st.release()

	y := int32(0)
	for i := range items {
		h := int32(menuItemHeight(items[i].Kind, items[i].Separator))
		dis := &drawItemStruct{ctlType: odtMenu, itemID: uint32(i + 1), hDC: hdc,
			rcItem: rect{left: 0, top: y, right: int32(st.menuWidth), bottom: y + h}, itemData: uintptr(i)}
		drawMenuItem(dis, &items[i], st)
		y += h
	}
	if got := pixelAt(bits, int(st.menuWidth)-4, 6) & 0xFFFFFF; got != 0x202020 {
		t.Fatalf("dark background = 0x%06X, want 0x202020", got)
	}
}

// TestOwndrawProgressBarAligned 两条进度条行（剩余时间长度不同）进度条右边界必须对齐。
// 验证：进度条右边界不随副文本宽度漂移（固定 subColWidth 列）。
func TestOwndrawProgressBarAligned(t *testing.T) {
	hdc, bits := smokeDC(t)
	pA, pB := 0.42, 0.8
	items := []MenuItem{
		{Text: "5h  42%", Kind: KindValue, Color: 0x43a047, Progress: &pA, Sub: "~2h18m", Disabled: true},
		{Text: "7d  80%", Kind: KindValue, Color: 0x43a047, Progress: &pB, Sub: "~5d23h", Disabled: true},
	}
	st := &owndrawState{}
	st.init(items, true)
	defer st.release()

	y := int32(0)
	for i := range items {
		h := int32(menuItemHeight(items[i].Kind, items[i].Separator))
		dis := &drawItemStruct{ctlType: odtMenu, itemID: uint32(i + 1), hDC: hdc,
			rcItem: rect{left: 0, top: y, right: int32(st.menuWidth), bottom: y + h}, itemData: uintptr(i)}
		drawMenuItem(dis, &items[i], st)
		y += h
	}

	// 进度条右边界 = menuWidth - padRight - subColWidth - gap，两行应相同。
	wantRight := int(st.menuWidth) - menuPadRight - subColWidth - progressGap
	rowH := int(menuItemHeight(KindValue, false))
	// 进度条背景（浅色 0xE8EAEE）在右边界内侧 y 中线的像素。
	cy := rowH / 2
	for row := 0; row < 2; row++ {
		yCoord := row*rowH + cy
		if got := pixelAt(bits, wantRight-1, yCoord) & 0xFFFFFF; got != 0xE8EAEE {
			t.Fatalf("row %d progress bar background at right edge = 0x%06X, want 0xE8EAEE (right=%d)", row, got, wantRight)
		}
		// 进度条右边界外侧应为菜单背景（不被进度条侵占）。
		if got := pixelAt(bits, wantRight+1, yCoord) & 0xFFFFFF; got != 0xFFFFFF {
			t.Fatalf("row %d pixel right of progress bar = 0x%06X, want menu bg 0xFFFFFF", row, got)
		}
	}
}
