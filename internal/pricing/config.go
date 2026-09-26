package pricing

// config.go 解析 kimi-hud 自己配置的 [pricing] 表（~/.kimi-code-hud/config.toml，
// 与 Kimi Code 的 ~/.kimi-code/config.toml 隔离，绝不写入后者），提供用户自定义
// 模型价格覆盖与订阅月费。内置价格表不可覆盖时（如订阅模型 kimi-for-coding 无单价），
// 用户可自行定价，使"调用量成本"统计对第三方/订阅模型也有意义。

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// 配置模板（写入 config.toml 即可热加载，无需重启）：
//
//	[pricing."kimi-for-coding"]     # 模型名与调用量统计中的模型名一致
//	input = 6.5                     # 元/百万 token
//	output = 26.0
//	cache_read = 1.3
//	cache_write = 6.5
//
//	[pricing.subscription]
//	monthly_cny = 60.0              # 订阅月费（仅展示，不参与调用量计费）

var (
	pricingModelTableRe = regexp.MustCompile(`^\[\s*pricing\s*\.\s*"([^"]+)"\s*\]`)
	subscriptionTableRe = regexp.MustCompile(`^\[\s*pricing\s*\.\s*subscription\s*\]`)
	pricingFieldRe      = regexp.MustCompile(`^\s*(input|output|cache_read|cache_write|monthly_cny)\s*=\s*([0-9]+(?:\.[0-9]+)?)\s*(?:#.*)?$`)
	tableStartRe        = regexp.MustCompile(`^\s*\[`)
)

// userOverrides 用户自定义模型价格（key 为归一化模型名，见 registerOverride）。
var userOverrides atomic.Pointer[map[string]OfficialPricing]

// subscriptionMonthlyNanos 订阅月费（纳元），0 表示未配置。
var subscriptionMonthlyNanos atomic.Int64

// OverrideLoader 管理 config.toml [pricing] 覆盖的加载与热重载（对齐 modelcfg 的 mtime 重读）。
type OverrideLoader struct {
	path  string
	mtime time.Time
}

// NewOverrideLoader 创建加载器并立即加载一次。
func NewOverrideLoader(path string) *OverrideLoader {
	l := &OverrideLoader{path: path}
	l.ReloadIfChanged()
	return l
}

// ReloadIfChanged config.toml 变更时重读并应用覆盖。
func (l *OverrideLoader) ReloadIfChanged() {
	fi, err := os.Stat(l.path)
	if err != nil {
		return
	}
	// 首次（mtime 为零）也执行加载；之后仅在 mtime 变化时重读。
	if !fi.ModTime().After(l.mtime) {
		return
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	l.apply(data)
	l.mtime = fi.ModTime()
}

func (l *OverrideLoader) apply(data []byte) {
	SetOverrides(parsePricingTables(data))
	setSubscription(parseSubscriptionFee(data))
}

// SetOverrides 设置用户自定义模型价格表（并发安全，原子替换）。
// 配置键经归一化注册（原键 + 短键），供 Find 精确匹配。
func SetOverrides(raw map[string]OfficialPricing) {
	if raw == nil {
		userOverrides.Store(nil)
		return
	}
	m := map[string]OfficialPricing{}
	for k, v := range raw {
		registerOverride(m, k, v)
	}
	userOverrides.Store(&m)
}

// SubscriptionMonthlyCny 返回订阅月费展示字符串（未配置为空串）。
func SubscriptionMonthlyCny() string {
	n := subscriptionMonthlyNanos.Load()
	if n <= 0 {
		return ""
	}
	return formatYuan(n)
}

func setSubscription(nanos int64) {
	subscriptionMonthlyNanos.Store(nanos)
}

// parsePricingTables 解析所有 [pricing."model"] 表为模型价格覆盖表（key 为配置键原文）。
func parsePricingTables(data []byte) map[string]OfficialPricing {
	var (
		out   map[string]OfficialPricing
		cur   string
		price *OfficialPricing
	)
	for len(data) > 0 {
		idx := indexByte(data, '\n')
		var ln string
		if idx < 0 {
			ln = strings.TrimSpace(string(data))
			data = nil
		} else {
			ln = strings.TrimSpace(string(data[:idx]))
			data = data[idx+1:]
		}
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if m := pricingModelTableRe.FindStringSubmatch(ln); m != nil {
			if price != nil {
				out[cur] = *price
			}
			cur = m[1]
			p := OfficialPricing{SourceURL: "user-config"}
			price = &p
			if out == nil {
				out = map[string]OfficialPricing{}
			}
			continue
		}
		if subscriptionTableRe.MatchString(ln) {
			if price != nil {
				out[cur] = *price
			}
			cur, price = "", nil
			continue
		}
		// 进入其他表（models/providers 等）则退出 pricing 解析。
		if tableStartRe.MatchString(ln) {
			if price != nil {
				out[cur] = *price
			}
			cur, price = "", nil
			continue
		}
		if price == nil {
			continue
		}
		if m := pricingFieldRe.FindStringSubmatch(ln); m != nil {
			f, err := strconv.ParseFloat(m[2], 64)
			if err != nil || f < 0 {
				continue
			}
			v := cnyFloatToNanos(f)
			switch m[1] {
			case "input":
				price.InputNanos = v
			case "output":
				price.OutputNanos = v
			case "cache_read":
				price.CacheReadNanos = v
			case "cache_write":
				price.CacheCreationNanos = v
			}
		}
	}
	if price != nil {
		out[cur] = *price
	}
	return out
}

// parseSubscriptionFee 解析 [pricing.subscription] 的 monthly_cny，返回纳元。
func parseSubscriptionFee(data []byte) int64 {
	inSubscription := false
	for len(data) > 0 {
		idx := indexByte(data, '\n')
		var ln string
		if idx < 0 {
			ln = strings.TrimSpace(string(data))
			data = nil
		} else {
			ln = strings.TrimSpace(string(data[:idx]))
			data = data[idx+1:]
		}
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if subscriptionTableRe.MatchString(ln) {
			inSubscription = true
			continue
		}
		if tableStartRe.MatchString(ln) {
			inSubscription = false
			continue
		}
		if !inSubscription {
			continue
		}
		if m := pricingFieldRe.FindStringSubmatch(ln); m != nil && m[1] == "monthly_cny" {
			f, err := strconv.ParseFloat(m[2], 64)
			if err != nil || f < 0 {
				return 0
			}
			return cnyFloatToNanos(f)
		}
	}
	return 0
}

// registerOverride 注册配置键：原键 + 去掉 provider 前缀的短键，均小写归一，
// 覆盖 Find 收到的不同模型名形态（如 "kimi-code/kimi-for-coding" 与 "kimi-for-coding"）。
func registerOverride(m map[string]OfficialPricing, key string, price OfficialPricing) {
	register := func(k string) {
		if k != "" {
			m[k] = price
		}
	}
	register(normalizeKey(key))
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		register(normalizeKey(key[i+1:]))
	}
}

// normalizeKey 模型名归一化：小写 + 去空白（供用户覆盖表精确匹配）。
func normalizeKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// cnyFloatToNanos 元（float）转纳元（1e9 元/纳元）。
func cnyFloatToNanos(f float64) int64 {
	if f > float64(1<<63-1)/float64(nanosPerCNY) {
		return 1<<63 - 1
	}
	return int64(f * float64(nanosPerCNY))
}

// formatYuan 纳元格式化为人可读金额（去尾零，至多 2 位小数）。
func formatYuan(nanos int64) string {
	if nanos < 0 {
		nanos = 0
	}
	whole := nanos / nanosPerCNY
	frac := (nanos % nanosPerCNY) / (nanosPerCNY / 100) // 百分位
	if frac == 0 {
		return formatInt(whole)
	}
	s := formatInt(whole) + "." + pad2(frac)
	return strings.TrimRight(s, "0")
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}
