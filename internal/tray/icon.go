package tray

// icon.go 运行时用 CreateDIBSection 绘制 16x16 托盘图标，支持按等级变色。

import (
	"errors"
	"fmt"
	"unsafe"
)

var errDC = errors.New("gdi: device context unavailable")

const iconSize = 16

// color 为 RGB（0xRRGGBB）。
func makeIcon(color uint32) (uintptr, error) {
	hdc := getDC(0)
	if hdc == 0 {
		return 0, errDC
	}
	defer releaseDC(0, hdc)

	memDC := createCompatibleDC(hdc)
	if memDC == 0 {
		return 0, errDC
	}
	defer deleteDC(memDC)

	// 32bpp 彩色位图。
	bmi := &bitmapInfo{}
	bmi.bmiHeader.biSize = uint32(unsafe.Sizeof(bmi.bmiHeader))
	bmi.bmiHeader.biWidth = iconSize
	bmi.bmiHeader.biHeight = -iconSize // top-down
	bmi.bmiHeader.biPlanes = 1
	bmi.bmiHeader.biBitCount = 32

	var bits *byte
	hbmColor, err := createDIBSection(memDC, bmi, &bits)
	if err != nil || hbmColor == 0 {
		return 0, fmt.Errorf("makeIcon: color DIB 失败: %w", err)
	}
	drawPixels(unsafe.Slice(bits, iconSize*iconSize*4), color)

	// 1bpp 单色掩码位图（全 0 = 不屏蔽），CreateIconIndirect 要求 hbmMask 有效。
	bmi1 := &bitmapInfo{}
	bmi1.bmiHeader.biSize = uint32(unsafe.Sizeof(bmi1.bmiHeader))
	bmi1.bmiHeader.biWidth = iconSize
	bmi1.bmiHeader.biHeight = -iconSize
	bmi1.bmiHeader.biPlanes = 1
	bmi1.bmiHeader.biBitCount = 1

	var maskBits *byte
	hbmMask, err := createDIBSection(memDC, bmi1, &maskBits)
	if err != nil || hbmMask == 0 {
		deleteObject(hbmColor)
		return 0, fmt.Errorf("makeIcon: mask DIB 失败: %w", err)
	}

	info := &iconInfo{
		fIcon:    1,
		hbmMask:  hbmMask,
		hbmColor: hbmColor,
	}
	hicon, err := createIconIndirect(info)

	// CreateIconIndirect 内部复制位图数据，创建后可释放位图句柄。
	deleteObject(hbmMask)
	deleteObject(hbmColor)

	if err != nil || hicon == 0 {
		return 0, fmt.Errorf("makeIcon: CreateIconIndirect 失败: %w", err)
	}
	return hicon, nil
}

// drawPixels 向 BGRA 像素缓冲绘制：透明背景 + 实心圆 + 白色圆点。
func drawPixels(pix []byte, color uint32) {
	r := byte(color >> 16)
	g := byte(color >> 8)
	b := byte(color)

	cx, cy := 7.5, 7.5
	radius := 7.2

	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			dx := float64(x) - cx
			dy := float64(y) - cy
			dist := dx*dx + dy*dy
			i := (y*iconSize + x) * 4

			if dist <= radius*radius {
				// 圆内：主体色
				pix[i+0] = b
				pix[i+1] = g
				pix[i+2] = r
				pix[i+3] = 255
				// 中心白色圆点（半径 2.2）
				if dist <= 2.2*2.2 {
					pix[i+0] = 255
					pix[i+1] = 255
					pix[i+2] = 255
				}
			} else {
				// 圆外透明
				pix[i+0] = 0
				pix[i+1] = 0
				pix[i+2] = 0
				pix[i+3] = 0
			}
		}
	}
}
