package tray

// owndraw.go 托盘菜单 owner-draw 自绘：卡片化样式、语义色指标、进度条与圆角 hover。
// 纯 GDI（LazyDLL），无 CGO；所有绘制发生在窗口线程（TrackPopupMenu 模态循环派发的
// WM_DRAWITEM），不涉及跨线程资源。

import (
	"unsafe"
)

// owndrawTheme 菜单配色（深浅两套，跟随系统 AppsUseLightTheme）。
type owndrawTheme struct {
	bg        uint32 // 菜单项背景
	text      uint32 // 主文本
	muted     uint32 // 副文本/分段标题
	hover     uint32 // hover 圆角高亮
	separator uint32 // 分隔线与进度条背景
	brand     uint32 // 品牌色（标题圆点/进度条默认）
}

var (
	themeLight = owndrawTheme{
		bg:        0xFFFFFF,
		text:      0x1F2328,
		muted:     0x6B7280,
		hover:     0xEAF1FE, // 蓝 8%
		separator: 0xE8EAEE,
		brand:     0x2563EB,
	}
	themeDark = owndrawTheme{
		bg:        0x202020,
		text:      0xE6E8EB,
		muted:     0x9A9DA3,
		hover:     0x3A3D41,
		separator: 0x3A3D41,
		brand:     0x6B9AF8,
	}
)

// 菜单布局常量（像素）。
const (
	menuMinWidth   = 300 // 菜单最小宽度
	menuPadRight   = 14  // 右内边距
	menuIconLeft   = 14  // 圆点/箭头图标中心 X（相对 rc.left）
	menuTextLeft   = 32  // 主文本左边界（相对 rc.left）
	progressWidth  = 64  // 进度条宽度
	progressGap    = 10  // 进度条与主文本/副文本间距
	progressHeight = 4   // 进度条高度
	dotDiameter    = 6   // 指标圆点直径
	// subColWidth 副文本（剩余时间）固定列宽：右对齐显示，超出省略。
	// 固定宽度保证所有行的进度条右边界一致（否则剩余时间长度不同 → 进度条错位）。
	subColWidth = 64
)

// owndrawState 一次菜单显示期间的绘制资源（字体/画刷/宽度），仅窗口线程访问。
type owndrawState struct {
	items      []MenuItem // 与 append 到菜单的项一一对应（itemData 传下标索引）
	light      bool
	menuWidth  int32
	fontNormal uintptr
	fontBold   uintptr
	fontTitle  uintptr
	brushBg    uintptr
	brushHover uintptr
	brushBrand uintptr
	brushSep   uintptr
	active     bool
}

// init 创建本次菜单显示所需的字体与画刷，并计算统一菜单宽度。
// 必须在 createPopupMenu 之后、TrackPopupMenu 之前调用，release 配对销毁。
func (s *owndrawState) init(items []MenuItem, light bool) {
	s.items = items
	s.light = light
	theme := s.theme()

	dpi := int32(96)
	if hdc := getDC(0); hdc != 0 {
		if v := getDeviceCaps(hdc, logPixelsY); v > 0 {
			dpi = v
		}
		releaseDC(0, hdc)
	}
	s.fontNormal = createSegoeFont(fwNormal, 9, dpi)
	s.fontBold = createSegoeFont(fwSemibold, 9, dpi)
	s.fontTitle = createSegoeFont(fwSemibold, 10, dpi)
	s.brushBg = createSolidBrush(theme.bg)
	s.brushHover = createSolidBrush(theme.hover)
	s.brushBrand = createSolidBrush(theme.brand)
	s.brushSep = createSolidBrush(theme.separator)
	s.menuWidth = s.computeWidth(items, dpi)
	s.active = true
}

func (s *owndrawState) release() {
	if !s.active {
		return
	}
	s.active = false
	deleteObject(s.fontNormal)
	deleteObject(s.fontBold)
	deleteObject(s.fontTitle)
	deleteObject(s.brushBg)
	deleteObject(s.brushHover)
	deleteObject(s.brushBrand)
	deleteObject(s.brushSep)
}

func (s *owndrawState) theme() owndrawTheme {
	if s.light {
		return themeLight
	}
	return themeDark
}

func (s *owndrawState) valid() bool {
	return s.active && s.fontNormal != 0 && s.brushBg != 0
}

// createSegoeFont 创建 Segoe UI 字体（pt 字号，按 dpi 缩放）。
func createSegoeFont(weight int32, pt int32, dpi int32) uintptr {
	var lf logFontW
	lf.lfHeight = -pt * dpi / 72
	lf.lfWeight = weight
	lf.lfCharSet = defaultChar
	lf.lfOutPrecision = outTtOnly
	lf.lfClipPrecision = clipDefault
	lf.lfQuality = clearTypeQ
	face := utf16Of("Segoe UI")
	copy(lf.lfFaceName[:], face)
	return createFontIndirect(&lf)
}

// computeWidth 遍历所有菜单项，按最宽行确定统一菜单宽度（等宽右对齐/进度条对齐）。
func (s *owndrawState) computeWidth(items []MenuItem, dpi int32) int32 {
	hdc := getDC(0)
	if hdc == 0 {
		return menuMinWidth
	}
	defer releaseDC(0, hdc)

	width := int32(menuMinWidth)
	for i := range items {
		it := &items[i]
		if it.Separator {
			continue
		}
		var font uintptr
		switch it.Kind {
		case KindTitle:
			font = s.fontTitle
		case KindHeading:
			font = s.fontBold
		default:
			font = s.fontNormal
		}
		old := selectObject(hdc, font)
		mw, _ := textExtent(hdc, it.Text)
		selectObject(hdc, old)

		need := mw + menuTextLeft + menuPadRight
		if it.Progress != nil {
			// 带进度条的行预留固定右侧布局：进度条 + 固定副文本列（subColWidth）。
			need += progressWidth + progressGap + subColWidth
		} else if it.Sub != "" {
			// 无进度条但有副文本：按实际文本宽度让位。
			old := selectObject(hdc, s.fontNormal)
			sw, _ := textExtent(hdc, it.Sub)
			selectObject(hdc, old)
			need += sw + progressGap
		}
		if need > width {
			width = need
		}
	}
	return width
}

// menuItemHeight 返回某类菜单项的行高（像素）。
func menuItemHeight(kind ItemKind, sep bool) uint32 {
	if sep {
		return 9
	}
	switch kind {
	case KindTitle:
		return 34
	case KindHeading:
		return 26
	case KindValue:
		return 26
	case KindAction:
		return 30
	}
	return 26
}

// onMeasureItem 处理 WM_MEASUREITEM：上报行高与统一菜单宽度。
func (t *Tray) onMeasureItem(lParam uintptr) uintptr {
	if !t.owndraw.valid() {
		return defWindowProc(t.hwnd, wmMeasureItem, 0, lParam)
	}
	mis := (*measureItemStruct)(unsafe.Pointer(lParam)) // lParam 为系统传入的 MEASUREITEMSTRUCT*，非 Go 内存往返
	if mis.ctlType != odtMenu {
		return defWindowProc(t.hwnd, wmMeasureItem, 0, lParam)
	}
	if int(mis.itemData) >= len(t.owndraw.items) {
		return defWindowProc(t.hwnd, wmMeasureItem, 0, lParam)
	}
	item := &t.owndraw.items[mis.itemData]
	mis.itemHeight = menuItemHeight(item.Kind, item.Separator)
	mis.itemWidth = uint32(t.owndraw.menuWidth)
	return 1
}

// onDrawItem 处理 WM_DRAWITEM：自绘菜单项。
func (t *Tray) onDrawItem(lParam uintptr) uintptr {
	if !t.owndraw.valid() {
		return defWindowProc(t.hwnd, wmDrawItem, 0, lParam)
	}
	dis := (*drawItemStruct)(unsafe.Pointer(lParam)) // lParam 为系统传入的 DRAWITEMSTRUCT*，非 Go 内存往返
	if dis.ctlType != odtMenu {
		return defWindowProc(t.hwnd, wmDrawItem, 0, lParam)
	}
	if int(dis.itemData) >= len(t.owndraw.items) {
		return defWindowProc(t.hwnd, wmDrawItem, 0, lParam)
	}
	item := &t.owndraw.items[dis.itemData]
	drawMenuItem(dis, item, &t.owndraw)
	return 1
}

func drawMenuItem(dis *drawItemStruct, item *MenuItem, st *owndrawState) {
	theme := st.theme()
	rc := dis.rcItem
	hdc := dis.hDC
	hovered := dis.itemState&(odsSelected|odsHotLight) != 0
	disabled := item.Disabled

	// 背景。
	fillRect(hdc, &rc, st.brushBg)

	// hover 圆角高亮（仅可点击动作项）。
	if hovered && !disabled && item.Kind == KindAction {
		hrc := rc
		hrc.left += 6
		hrc.right -= 6
		hrc.top += 3
		hrc.bottom -= 3
		roundRectFilled(hdc, &hrc, st.brushHover, 8)
	}

	setBkMode(hdc, transparent)

	if item.Separator {
		// 柔分隔线：居中 1px 细线。
		y := (rc.top + rc.bottom) / 2
		lrc := rect{left: rc.left + 12, top: y, right: rc.right - 12, bottom: y + 1}
		fillRect(hdc, &lrc, st.brushSep)
		return
	}

	switch item.Kind {
	case KindTitle:
		drawTitleRow(dis, item, st, theme)
	case KindHeading:
		drawHeadingRow(dis, item, st, theme)
	case KindValue:
		drawValueRow(dis, item, st, theme)
	case KindAction:
		drawActionRow(dis, item, st, theme, hovered, disabled)
	}
}

func drawTitleRow(dis *drawItemStruct, item *MenuItem, st *owndrawState, theme owndrawTheme) {
	rc := dis.rcItem
	hdc := dis.hDC
	cy := (rc.top + rc.bottom) / 2

	// 品牌圆点。
	ellipseFilled(hdc, rc.left+10, cy-5, rc.left+20, cy+5, st.brushBrand)

	old := selectObject(hdc, st.fontTitle)
	setTextColor(hdc, theme.text)
	tc := rect{left: rc.left + 30, top: rc.top, right: rc.right - menuPadRight, bottom: rc.bottom}
	drawText(hdc, item.Text, &tc, dtLeft|dtVcenter|dtSingleLine|dtNoPrefix)
	selectObject(hdc, old)
}

func drawHeadingRow(dis *drawItemStruct, item *MenuItem, st *owndrawState, theme owndrawTheme) {
	rc := dis.rcItem
	hdc := dis.hDC
	old := selectObject(hdc, st.fontBold)
	setTextColor(hdc, theme.muted)
	tc := rect{left: rc.left + 14, top: rc.top, right: rc.right - menuPadRight, bottom: rc.bottom}
	drawText(hdc, item.Text, &tc, dtLeft|dtVcenter|dtSingleLine|dtNoPrefix)
	selectObject(hdc, old)
}

func drawValueRow(dis *drawItemStruct, item *MenuItem, st *owndrawState, theme owndrawTheme) {
	rc := dis.rcItem
	hdc := dis.hDC
	cy := (rc.top + rc.bottom) / 2

	color := item.Color
	if color == 0 {
		color = theme.muted
	}

	// 语义色圆点。
	dotBrush := createSolidBrush(color)
	if dotBrush != 0 {
		ellipseFilled(hdc, rc.left+menuIconLeft-dotDiameter/2, cy-dotDiameter/2,
			rc.left+menuIconLeft+dotDiameter/2, cy+dotDiameter/2, dotBrush)
		deleteObject(dotBrush)
	}

	old := selectObject(hdc, st.fontNormal)

	hasProg := item.Progress != nil
	// 进度条右边界固定（不随副文本宽度漂移）：预留固定副文本列，各行进度条对齐。
	var progLeft, progRight int32
	if hasProg {
		progRight = rc.right - menuPadRight - subColWidth - progressGap
		progLeft = progRight - progressWidth
	}

	// 主文本。
	textRight := rc.right - menuPadRight
	if hasProg {
		textRight = progLeft - progressGap
	} else if item.Sub != "" {
		// 无进度条但有副文本：主文本让位副文本实际宽度。
		subW, _ := textExtent(hdc, item.Sub)
		textRight = rc.right - menuPadRight - subW - progressGap
	}
	tc := rect{left: rc.left + menuTextLeft, top: rc.top, right: textRight, bottom: rc.bottom}
	setTextColor(hdc, color)
	drawText(hdc, item.Text, &tc, dtLeft|dtVcenter|dtSingleLine|dtNoPrefix|dtEndEllipsis)

	// 进度条（muted 背景 + 语义色前景，裁剪到 frac 宽度）。
	// 用 FillRect 而非 RoundRect：4px 高圆角条的 Radius=4 时最右列属"边框"
	// 区域，NULL 画笔下不被填充会露底（实测 1px 黑线），FillRect 完整填充到右边界。
	if hasProg {
		frac := clamp01(*item.Progress)
		barRC := rect{left: progLeft, top: cy - progressHeight/2, right: progRight, bottom: cy + progressHeight/2}
		fillRect(hdc, &barRC, st.brushSep)
		if frac > 0 && item.Color != 0 {
			barBrush := createSolidBrush(item.Color)
			if barBrush != 0 {
				saved := saveDC(hdc)
				fillW := int32(float64(progressWidth) * frac)
				intersectClipRect(hdc, progLeft, cy-progressHeight/2, progLeft+fillW, cy+progressHeight/2)
				fillRect(hdc, &barRC, barBrush)
				restoreDC(hdc, saved)
				deleteObject(barBrush)
			}
		}
	}

	// 副文本：固定列内右对齐（超出省略），保证进度条右边界恒定对齐。
	if item.Sub != "" {
		subLeft := rc.right - menuPadRight - subColWidth
		tc := rect{left: subLeft, top: rc.top, right: rc.right - menuPadRight, bottom: rc.bottom}
		setTextColor(hdc, theme.muted)
		drawText(hdc, item.Sub, &tc, dtRight|dtVcenter|dtSingleLine|dtNoPrefix|dtEndEllipsis)
	}

	selectObject(hdc, old)
}

func drawActionRow(dis *drawItemStruct, item *MenuItem, st *owndrawState, theme owndrawTheme, hovered, disabled bool) {
	rc := dis.rcItem
	hdc := dis.hDC

	// 左箭头符号（disabled 灰置；hover 用品牌色）。
	arrowColor := theme.muted
	if hovered && !disabled {
		arrowColor = theme.brand
	}
	old := selectObject(hdc, st.fontNormal)
	setTextColor(hdc, arrowColor)
	arc := rect{left: rc.left + 6, top: rc.top, right: rc.left + 24, bottom: rc.bottom}
	drawText(hdc, "›", &arc, dtLeft|dtVcenter|dtSingleLine|dtNoPrefix)

	// 文本：默认前景色，item.Color 覆盖（如退出红色）；disabled 灰置。
	textColor := theme.text
	if item.Color != 0 {
		textColor = item.Color
	}
	if disabled {
		textColor = theme.muted
	}
	setTextColor(hdc, textColor)
	tc := rect{left: rc.left + menuTextLeft, top: rc.top, right: rc.right - menuPadRight, bottom: rc.bottom}
	drawText(hdc, item.Text, &tc, dtLeft|dtVcenter|dtSingleLine|dtNoPrefix)
	selectObject(hdc, old)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
