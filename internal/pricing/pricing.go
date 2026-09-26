// Package pricing 提供 Kimi 官方模型价格表与费用计算（移植自 kimi-usage-tracker 的 pricing.rs）。
// 费用为估算：kimi-for-coding 订阅模型不套用单价（unpriced）。
// 金额以"纳元（nanos，1e-9 CNY）"为整数单位，避免浮点累积误差。
package pricing

import (
	"strings"
)

const (
	nanosPerCNY   int64 = 1_000_000_000
	tokensPerUnit int64 = 1_000_000
)

// OfficialPricing 某官方模型的四维单价（纳元/百万 token）。
type OfficialPricing struct {
	InputNanos         int64
	OutputNanos        int64
	CacheReadNanos     int64
	CacheCreationNanos int64
	ContextTokens      uint64
	SourceURL          string
}

// PricingMetadata 前端展示用的定价元数据（camelCase 对齐 tracker UsageReport）。
type PricingMetadata struct {
	Model                  string `json:"model"`
	InputCnyPerMillion     string `json:"inputCnyPerMillion"`
	OutputCnyPerMillion    string `json:"outputCnyPerMillion"`
	CacheReadCnyPerMillion string `json:"cacheReadCnyPerMillion"`
	ContextTokens          uint64 `json:"contextTokens"`
	SourceURL              string `json:"sourceUrl"`
	VerifiedOn             string `json:"verifiedOn"`
}

// CalculateNanos 计算一次用量的总费用（纳元）。
func (p OfficialPricing) CalculateNanos(inputOther, output, cacheRead, cacheCreation uint64) int64 {
	weighted := int64(inputOther)*p.InputNanos +
		int64(output)*p.OutputNanos +
		int64(cacheRead)*p.CacheReadNanos +
		int64(cacheCreation)*p.CacheCreationNanos
	return weighted / tokensPerUnit
}

// CalculateSegmentsNanos 计算四维分项费用（顺序：inputOther/output/cacheRead/cacheCreation）。
func (p OfficialPricing) CalculateSegmentsNanos(inputOther, output, cacheRead, cacheCreation uint64) [4]int64 {
	return [4]int64{
		int64(inputOther) * p.InputNanos / tokensPerUnit,
		int64(output) * p.OutputNanos / tokensPerUnit,
		int64(cacheRead) * p.CacheReadNanos / tokensPerUnit,
		int64(cacheCreation) * p.CacheCreationNanos / tokensPerUnit,
	}
}

// Find 返回模型对应的定价：优先用户配置覆盖（config.toml [pricing]），
// 其次官方价格表；订阅/未知模型且未配置覆盖时返回 nil。
func Find(model string) *OfficialPricing {
	if m := userOverrides.Load(); m != nil {
		if p, ok := (*m)[normalizeKey(model)]; ok {
			return &p
		}
		if i := strings.LastIndexByte(model, '/'); i >= 0 {
			if p, ok := (*m)[normalizeKey(model[i+1:])]; ok {
				return &p
			}
		}
	}
	canonical, ok := CanonicalModel(model)
	if !ok {
		return nil
	}
	switch canonical {
	case "kimi-k3":
		return &OfficialPricing{cnyCents(2000), cnyCents(10000), cnyCents(200), cnyCents(2000), 1_048_576, "https://platform.kimi.com/docs/pricing/chat-k3"}
	case "kimi-k2.7-code":
		return &OfficialPricing{cnyCents(650), cnyCents(2700), cnyCents(130), cnyCents(650), 262_144, "https://platform.kimi.com/docs/pricing/chat-k27-code"}
	case "kimi-k2.7-code-highspeed":
		return &OfficialPricing{cnyCents(1300), cnyCents(5400), cnyCents(260), cnyCents(1300), 262_144, "https://platform.kimi.com/docs/pricing/chat-k27-code"}
	case "kimi-k2.6":
		return &OfficialPricing{cnyCents(650), cnyCents(2700), cnyCents(110), cnyCents(650), 262_144, "https://platform.kimi.com/docs/pricing/chat-k26"}
	case "kimi-k2.5":
		return &OfficialPricing{cnyCents(400), cnyCents(2100), cnyCents(70), cnyCents(400), 262_144, "https://platform.kimi.com/docs/pricing/chat-k25"}
	case "moonshot-v1-8k", "moonshot-v1-8k-vision-preview":
		return &OfficialPricing{cnyCents(200), cnyCents(1000), cnyCents(200), cnyCents(200), 8_192, "https://platform.kimi.com/docs/pricing/chat-v1"}
	case "moonshot-v1-32k", "moonshot-v1-32k-vision-preview":
		return &OfficialPricing{cnyCents(500), cnyCents(2000), cnyCents(500), cnyCents(500), 32_768, "https://platform.kimi.com/docs/pricing/chat-v1"}
	case "moonshot-v1-128k", "moonshot-v1-128k-vision-preview":
		return &OfficialPricing{cnyCents(1000), cnyCents(3000), cnyCents(1000), cnyCents(1000), 131_072, "https://platform.kimi.com/docs/pricing/chat-v1"}
	}
	return nil
}

// Metadata 返回定价元数据（用于模型统计表）。
func Metadata(model string) *PricingMetadata {
	price := Find(model)
	if price == nil {
		return nil
	}
	canonical, _ := CanonicalModel(model)
	return &PricingMetadata{
		Model:                  canonical,
		InputCnyPerMillion:     formatRate(price.InputNanos),
		OutputCnyPerMillion:    formatRate(price.OutputNanos),
		CacheReadCnyPerMillion: formatRate(price.CacheReadNanos),
		ContextTokens:          price.ContextTokens,
		SourceURL:              price.SourceURL,
		VerifiedOn:             "2026-07-20",
	}
}

// FormatCnyNanos 格式化纳元金额为"元.分"字符串，四舍五入保留 2 位小数
// （如 "0.12"、"40.30"、"122.00"）。全链路费用展示统一口径：窗口汇总卡/
// 模型费用列/最近请求费用列/托盘菜单"费用(元)"行，均走此函数，避免各处以
// 不同精度显示同一金额。
func FormatCnyNanos(value int64) string {
	if value < 0 {
		value = 0
	}
	// 四舍五入到分：半分 = 5_000_000 纳元（nanosPerCNY/200）。
	totalCents := (value + nanosPerCNY/200) / (nanosPerCNY / 100)
	whole := totalCents / 100
	frac := totalCents % 100
	return formatInt(whole) + "." + pad2(frac)
}

func formatRate(value int64) string {
	whole := value / nanosPerCNY
	fraction := (value % nanosPerCNY) / 10_000_000
	if fraction == 0 {
		return formatInt(whole)
	}
	s := formatInt(whole) + "." + pad2(fraction)
	s = strings.TrimRight(s, "0")
	return s
}

// CanonicalModel 归一化模型名到官方模型名（大小写/下划线/斜杠/冒号后缀）。
func CanonicalModel(model string) (string, bool) {
	normalized := model
	if i := strings.LastIndexByte(normalized, '/'); i >= 0 {
		normalized = normalized[i+1:]
	}
	if i := strings.IndexByte(normalized, ':'); i >= 0 {
		normalized = normalized[:i]
	}
	normalized = strings.TrimSpace(normalized)
	normalized = strings.ToLower(normalized)
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "k3", "kimi3", "kimi-k3":
		return "kimi-k3", true
	case "k2.7-code", "k2-7-code", "kimi-k2-7-code", "kimi-k2.7-code":
		return "kimi-k2.7-code", true
	case "k2.7-code-highspeed", "k2.7-code-high-speed", "k2-7-code-highspeed", "k2-7-code-high-speed",
		"kimi-k2-7-code-highspeed", "kimi-k2-7-code-high-speed", "kimi-k2.7-code-high-speed", "kimi-k2.7-code-highspeed":
		return "kimi-k2.7-code-highspeed", true
	case "k2.6", "k2-6", "kimi-k2-6", "kimi-k2.6":
		return "kimi-k2.6", true
	case "k2.5", "k2-5", "kimi-k2-5", "kimi-k2.5":
		return "kimi-k2.5", true
	case "moonshot-v1-8k":
		return "moonshot-v1-8k", true
	case "moonshot-v1-32k":
		return "moonshot-v1-32k", true
	case "moonshot-v1-128k":
		return "moonshot-v1-128k", true
	case "moonshot-v1-8k-vision-preview":
		return "moonshot-v1-8k-vision-preview", true
	case "moonshot-v1-32k-vision-preview":
		return "moonshot-v1-32k-vision-preview", true
	case "moonshot-v1-128k-vision-preview":
		return "moonshot-v1-128k-vision-preview", true
	}
	return "", false
}

func cnyCents(value int64) int64 {
	return value * (nanosPerCNY / 100)
}

func formatInt(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func pad2(v int64) string {
	return pad(v, 2)
}

func pad(v int64, width int) string {
	s := formatInt(v)
	for len(s) < width {
		s = "0" + s
	}
	return s
}
