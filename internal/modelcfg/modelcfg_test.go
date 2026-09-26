package modelcfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `default_model = "kimi-code/k3"

[thinking]
enabled = true
effort = "high"

[models."kimi-code/k3"]
provider = "managed:kimi-code"
model = "k3"
display_name = "K3"

[models."anthropic/claude"]
provider = "anthropic"
model = "claude-sonnet-4"
display_name = "Claude"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	c := Load(path)
	if !c.IsManaged("kimi-code/k3") {
		t.Fatal("k3 should be managed")
	}
	if c.IsManaged("anthropic/claude") {
		t.Fatal("claude should not be managed")
	}
	if p, ok := c.ProviderFor("anthropic/claude"); !ok || p != "anthropic" {
		t.Fatalf("claude provider = %q, %v", p, ok)
	}
	if c.IsManaged("nonexistent") {
		t.Fatal("nonexistent should not be managed")
	}
	// P3-10：display_name 匹配回退（对齐参考 findModelTable 多路匹配）。
	if !c.IsManaged("K3") {
		t.Fatal("display_name K3 should resolve to managed provider")
	}
	if c.IsManaged("Claude") {
		t.Fatal("display_name Claude should not be managed")
	}
}

func TestReloadIfChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `[models."kimi-code/k3"]
provider = "managed:kimi-code"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Load(path)
	if !c.IsManaged("kimi-code/k3") {
		t.Fatal("k3 should be managed initially")
	}
	// 无变更时不重读。
	c.ReloadIfChanged()
	if !c.IsManaged("kimi-code/k3") {
		t.Fatal("reload without change must keep model")
	}
	// 变更后应重读：provider 改非托管。
	changed := `[models."kimi-code/k3"]
provider = "anthropic"
`
	if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	c.ReloadIfChanged()
	if c.IsManaged("kimi-code/k3") {
		t.Fatal("k3 should no longer be managed after reload")
	}
}
