// Package paths 解析 Kimi Code 相关的本地路径（对齐 kimi-code-hud 的 paths.mjs）。
package paths

import (
	"os"
	"path/filepath"
)

// Paths 聚合本项目用到的全部本地路径。
type Paths struct {
	KimiHome     string // ~/.kimi-code
	SessionsRoot string // ~/.kimi-code/sessions
	Credentials  string // ~/.kimi-code/credentials/kimi-code.json
	ConfigToml   string // ~/.kimi-code/config.toml（只读：模型/provider 判定，绝不写入）
	TuiToml      string // ~/.kimi-code/tui.toml
	HudDir       string // ~/.kimi-code-hud（本项目缓存目录）
	HudConfig    string // HudDir/config.toml（kimi-hud 自己的配置，与 Kimi Code 隔离）
	QuotaCache   string // HudDir/quota.json
	StateDir     string // HudDir/state（会话状态持久化）
}

// New 基于当前用户主目录解析路径；环境变量 KIMI_CODE_HOME 可覆盖 kimi 主目录
// （对齐 resolveRuntimePaths 的 env.KIMI_CODE_HOME 支持）。
func New() *Paths {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("USERPROFILE")
	}
	kimiHome := os.Getenv("KIMI_CODE_HOME")
	if kimiHome == "" {
		kimiHome = filepath.Join(home, ".kimi-code")
	}
	hudDir := filepath.Join(home, ".kimi-code-hud")
	return &Paths{
		KimiHome:     kimiHome,
		SessionsRoot: filepath.Join(kimiHome, "sessions"),
		Credentials:  filepath.Join(kimiHome, "credentials", "kimi-code.json"),
		ConfigToml:   filepath.Join(kimiHome, "config.toml"),
		TuiToml:      filepath.Join(kimiHome, "tui.toml"),
		HudDir:       hudDir,
		HudConfig:    filepath.Join(hudDir, "config.toml"),
		QuotaCache:   filepath.Join(hudDir, "quota.json"),
		StateDir:     filepath.Join(hudDir, "state"),
	}
}

// EnsureDirs 确保缓存目录存在。
func (p *Paths) EnsureDirs() error {
	for _, d := range []string{p.HudDir, p.StateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
