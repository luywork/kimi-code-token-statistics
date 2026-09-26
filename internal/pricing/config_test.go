package pricing

import "testing"

// TestPricingOverridePricesSubscriptionModel 用户配置 [pricing] 后，订阅模型可计价。
func TestPricingOverridePricesSubscriptionModel(t *testing.T) {
	SetOverrides(nil)
	subscriptionMonthlyNanos.Store(0)
	t.Cleanup(func() {
		SetOverrides(nil)
		subscriptionMonthlyNanos.Store(0)
	})

	data := []byte(`
[pricing."kimi-for-coding"]
input = 6.5
output = 26.0
cache_read = 1.3
cache_write = 6.5
`)
	SetOverrides(parsePricingTables(data))

	p := Find("kimi-for-coding")
	if p == nil {
		t.Fatal("kimi-for-coding should be priced after override")
	}
	nanos := p.CalculateNanos(1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if got := FormatCnyNanos(nanos); got != "40.30" {
		t.Fatalf("expected 40.30 (6.5+26+1.3+6.5), got %s", got)
	}
}

// TestPricingOverrideSupersedesBuiltin 用户覆盖优先于内置官方价格表。
func TestPricingOverrideSupersedesBuiltin(t *testing.T) {
	SetOverrides(nil)
	t.Cleanup(func() { SetOverrides(nil) })

	data := []byte(`
[pricing."kimi-k3"]
input = 1.0
output = 2.0
`)
	SetOverrides(parsePricingTables(data))

	p := Find("kimi-k3")
	if p == nil {
		t.Fatal("kimi-k3 pricing missing")
	}
	if p.InputNanos != 1_000_000_000 || p.OutputNanos != 2_000_000_000 {
		t.Fatalf("override not applied: input=%d output=%d", p.InputNanos, p.OutputNanos)
	}
}

// TestPricingOverrideKeyForms 配置键支持 provider 前缀形态（短键匹配）。
func TestPricingOverrideKeyForms(t *testing.T) {
	SetOverrides(nil)
	t.Cleanup(func() { SetOverrides(nil) })

	data := []byte(`
[pricing."anthropic/claude-sonnet-4-5"]
input = 20.0
output = 100.0
`)
	SetOverrides(parsePricingTables(data))

	// resolver 可能返回带前缀的模型名或短名，两种都应命中。
	for _, model := range []string{"anthropic/claude-sonnet-4-5", "claude-sonnet-4-5"} {
		if Find(model) == nil {
			t.Fatalf("override should match %q", model)
		}
	}
}

// TestPricingSubscriptionFee 解析 [pricing.subscription] 月费并格式化。
func TestPricingSubscriptionFee(t *testing.T) {
	setSubscription(0)
	t.Cleanup(func() { setSubscription(0) })

	data := []byte(`
[pricing.subscription]
monthly_cny = 59.5
`)
	setSubscription(parseSubscriptionFee(data))
	if got := SubscriptionMonthlyCny(); got != "59.5" {
		t.Fatalf("subscription monthly = %q, want 59.5", got)
	}

	setSubscription(0)
	if got := SubscriptionMonthlyCny(); got != "" {
		t.Fatalf("unconfigured subscription should be empty, got %q", got)
	}
}

// TestPricingOverrideIgnoresOtherTables 其他表（models 等）不影响 pricing 解析。
func TestPricingOverrideIgnoresOtherTables(t *testing.T) {
	SetOverrides(nil)
	t.Cleanup(func() { SetOverrides(nil) })

	data := []byte(`
[models."kimi-code/kimi-for-coding"]
provider = "managed:kimi-code"
model = "kimi-for-coding"

[pricing."kimi-for-coding"]
input = 6.5
`)
	SetOverrides(parsePricingTables(data))
	if Find("kimi-for-coding") == nil {
		t.Fatal("pricing table after models table should parse")
	}
	if Find("kimi-k3") == nil {
		t.Fatal("unrelated models should keep builtin pricing")
	}
}
