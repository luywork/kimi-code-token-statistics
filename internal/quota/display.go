package quota

import (
	"math"
	"strings"
	"time"
)

// BarWidth 柱条宽度（对齐 kimi-code-hud BAR_WIDTH=10）。
const BarWidth = 10

// Bar 渲染 10 格 Unicode 柱条（对齐参考 `Math.floor(clamped*BAR_WIDTH)`）。
//
// 注意：空块必须用 U+2593「▓」而非参考源码的「░」。参考跑在等宽终端里
// █/░ 同宽同高；但 Windows 托盘菜单用的是微软雅黑（Microsoft YaHei UI，
// 中文系统 DEFAULT_GUI_FONT），实测 ░/▒/□/全角空格在该字体下都既窄又矮
// 且下沉（░=8px 而 █=9px，□ 字形高度不到 █ 一半），柱条会变短或变矮。
// ▓ 与 █ 同为 9px/个、字形等高同顶，任意百分比下柱条恒为 90px 等长等
// 高；实块=█ 纯实心，空块=▓ 交叉斜纹，仍可清晰区分。
func Bar(frac float64) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	full := int(math.Floor(BarWidth * frac))
	return strings.Repeat("█", full) + strings.Repeat("▓", BarWidth-full)
}

// Countdown 对齐 formatCountdown：~2h18m / ~3d2h / ~reset。
func Countdown(reset time.Time, now time.Time) string {
	remain := int64(reset.Sub(now).Seconds())
	if remain <= 0 {
		return "~reset"
	}
	switch {
	case remain >= 86400:
		return "~" + itoa(int(remain/86400)) + "d" + itoa(int((remain%86400)/3600)) + "h"
	case remain >= 3600:
		return "~" + itoa(int(remain/3600)) + "h" + itoa(int((remain%3600)/60)) + "m"
	default:
		m := remain / 60
		return "~" + itoa(int(m)) + "m"
	}
}

// BestWindow 返回使用率最高的窗口（用于图标分级的紧迫度判定）。
func (q *Quota) BestWindow() (Window, bool) {
	if q == nil || len(q.Windows) == 0 {
		return Window{}, false
	}
	best := q.Windows[0]
	bestFracValue := bestFrac(best)
	for _, w := range q.Windows {
		if f := bestFrac(w); f > bestFracValue {
			best = w
			bestFracValue = f
		}
	}
	return best, true
}

func bestFrac(w Window) float64 {
	if w.Limit <= 0 {
		return 0
	}
	return w.Used / w.Limit
}
