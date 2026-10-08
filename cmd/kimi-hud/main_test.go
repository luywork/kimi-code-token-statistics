package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureConfigTemplateCreates 缺失时生成模板：文件存在、全行注释（零配置）。
// "全行注释"是模板安全性的关键——pricing/quota 解析器跳过 # 行，若表头
// （[pricing."x"] / [quota]）未注释，空表会被解析器当成配置（pricing 会以
// 零值覆盖内置价格表）。
func TestEnsureConfigTemplateCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	ensureConfigTemplate(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		ln := strings.TrimSpace(line)
		if ln != "" && !strings.HasPrefix(ln, "#") {
			t.Fatalf("template line %d not a comment (must be zero-config): %q", i+1, ln)
		}
	}
	// 用户要抄的示例必须在场（注释态）。
	for _, want := range []string{"#[quota]", "#api_key = \"sk-kimi-", "#[pricing.\"kimi-for-coding\"]", "#input"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("template missing example %q", want)
		}
	}
}

// TestEnsureConfigTemplateNeverOverwrites 已存在的文件绝不覆盖（用户已配置的
// key/单价不能被模板冲掉）。
func TestEnsureConfigTemplateNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	mine := "[quota]\napi_key = \"sk-kimi-mine\"\n"
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureConfigTemplate(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != mine {
		t.Fatalf("existing config overwritten:\n%s", data)
	}
}
