package pricing

import "testing"

// 移植自 tracker pricing.rs 测试：managed_kimi_code_has_no_fake_token_price
func TestManagedKimiCodeHasNoFakeTokenPrice(t *testing.T) {
	if Find("kimi-for-coding") != nil {
		t.Fatal("kimi-for-coding should be unpriced")
	}
}

// 移植自 tracker pricing.rs 测试：calculates_k3_components_from_official_rates
func TestCalculatesK3ComponentsFromOfficialRates(t *testing.T) {
	p := Find("moonshot/kimi-k3")
	if p == nil {
		t.Fatal("kimi-k3 pricing missing")
	}
	nanos := p.CalculateNanos(1_000_000, 1_000_000, 1_000_000, 0)
	if got := FormatCnyNanos(nanos); got != "122.00" {
		t.Fatalf("expected 122.00, got %s", got)
	}
	if p.ContextTokens != 1_048_576 {
		t.Fatalf("unexpected context tokens: %d", p.ContextTokens)
	}
}

// 四舍五入保留 2 位小数：边界与整数金额。
func TestFormatCnyNanosRounding(t *testing.T) {
	cases := []struct {
		nanos int64
		want  string
	}{
		{0, "0.00"},
		{1, "0.00"},                 // 1 纳元 ≈ 0
		{5_000_000, "0.01"},         // 半分进位 → 0.01
		{4_999_999, "0.00"},         // 不足半分舍去
		{120_000_000, "0.12"},       // 0.12 元
		{40_300_000_000, "40.30"},   // 40.3 元 → 固定 2 位
		{122_000_000_000, "122.00"}, // 122 元 → 固定 2 位
		{1_999_999_999, "2.00"},     // 1.999999 元 → 2.00
		{1_999_999_900, "2.00"},     // 接近 2.00 的四舍五入
	}
	for _, c := range cases {
		if got := FormatCnyNanos(c.nanos); got != c.want {
			t.Fatalf("FormatCnyNanos(%d) = %q, want %q", c.nanos, got, c.want)
		}
	}
}

// 移植自 tracker pricing.rs 测试：includes_current_public_platform_models
func TestIncludesCurrentPublicPlatformModels(t *testing.T) {
	for _, model := range []string{
		"kimi-k3", "kimi-k2.7-code", "kimi-k2.7-code-highspeed",
		"kimi-k2.6", "kimi-k2.5",
		"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k",
	} {
		if Find(model) == nil {
			t.Fatalf("missing official price for %s", model)
		}
	}
}

// 移植自 tracker pricing.rs 测试：resolves_common_cli_and_environment_model_aliases
func TestResolvesCommonCLIAndEnvironmentModelAliases(t *testing.T) {
	cases := []struct{ in, want string }{
		{"k3", "kimi-k3"},
		{"KIMI/K2_7_CODE", "kimi-k2.7-code"},
		{"kimi-k2-6:latest", "kimi-k2.6"},
		{"k2.7-code-high-speed", "kimi-k2.7-code-highspeed"},
	}
	for _, c := range cases {
		got, ok := CanonicalModel(c.in)
		if !ok || got != c.want {
			t.Fatalf("canonical_model(%q) = %q, %v; want %q", c.in, got, ok, c.want)
		}
	}
}
