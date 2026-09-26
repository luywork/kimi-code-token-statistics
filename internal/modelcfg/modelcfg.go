// Package modelcfg 解析 config.toml 的模型/provider 表，用于订阅配额门控。
package modelcfg

import (
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ManagedKimiProvider 是 /usages 配额 API 描述的唯一托管 provider（对齐 constants）。
const ManagedKimiProvider = "managed:kimi-code"

var (
	modelTableRe  = regexp.MustCompile(`\[models\."([^"]+)"\]`)
	providerRe    = regexp.MustCompile(`^\s*provider\s*=\s*"([^"]*)"`)
	displayNameRe = regexp.MustCompile(`^\s*display_name\s*=\s*"([^"]*)"`)
)

// Model 一个模型别名配置。
type Model struct {
	Alias       string
	Provider    string
	DisplayName string
}

// Config 模型配置表（并发安全：后台轮询重读、前台菜单读取）。
type Config struct {
	path  string
	mtime time.Time

	mu      sync.RWMutex
	models  map[string]Model
	display map[string]Model // display_name -> Model（P3-10 匹配回退）
}

// Load 读取并解析 config.toml。
func Load(path string) *Config {
	c := &Config{path: path, models: map[string]Model{}, display: map[string]Model{}}
	c.reloadLocked()
	return c
}

// ReloadIfChanged config.toml 变更时重读（对齐方案 §3.4 任务③）。
func (c *Config) ReloadIfChanged() {
	c.mu.RLock()
	old := c.mtime
	c.mu.RUnlock()
	fi, err := os.Stat(c.path)
	if err != nil || fi.ModTime().Equal(old) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// 写锁下复核，避免并发重复重读。
	if fi2, err := os.Stat(c.path); err == nil && !fi2.ModTime().Equal(c.mtime) {
		c.reloadLocked()
	}
}

// reloadLocked 重新读取文件并重建解析表（调用方需持写锁或尚未并发）。
func (c *Config) reloadLocked() {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	if fi, err := os.Stat(c.path); err == nil {
		c.mtime = fi.ModTime()
	}
	c.models = map[string]Model{}
	c.display = map[string]Model{}
	c.parse(string(data))
}

// parse 逐行扫描 [models."<alias>"] 表并抽取 provider/display_name。
func (c *Config) parse(text string) {
	curAlias := ""
	inTable := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			// 结束当前表。
			inTable = false
			if m := modelTableRe.FindStringSubmatch(trimmed); m != nil {
				curAlias = m[1]
				c.models[curAlias] = Model{Alias: curAlias}
				inTable = true
			}
			continue
		}
		if !inTable || curAlias == "" {
			continue
		}
		model := c.models[curAlias]
		if m := providerRe.FindStringSubmatch(trimmed); m != nil {
			model.Provider = m[1]
		} else if m := displayNameRe.FindStringSubmatch(trimmed); m != nil {
			model.DisplayName = m[1]
		}
		c.models[curAlias] = model
	}
	// 解析完成后统一构建 display_name 索引（避免字段顺序依赖）。
	for _, m := range c.models {
		if m.DisplayName != "" {
			c.display[m.DisplayName] = m
		}
	}
}

// ProviderFor 返回模型别名的 provider。
func (c *Config) ProviderFor(alias string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.providerOfLocked(alias)
}

func (c *Config) providerOfLocked(alias string) (string, bool) {
	if alias == "" {
		return "", false
	}
	m, ok := c.models[alias]
	if !ok {
		return "", false
	}
	return m.Provider, true
}

// IsManaged 判断模型是否为 Kimi 托管 provider；先按 alias 精确匹配，
// 失败时回退 display_name 匹配（对齐参考 findModelTable 多路匹配）。
func (c *Config) IsManaged(alias string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := c.providerOfLocked(alias); ok {
		return p == ManagedKimiProvider
	}
	if m, ok := c.display[alias]; ok {
		return m.Provider == ManagedKimiProvider
	}
	return false
}
