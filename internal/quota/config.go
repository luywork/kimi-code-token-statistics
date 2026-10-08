// config.go 从 kimi-hud 自己的 config.toml（~/.kimi-code-hud/config.toml）的 [quota]
// 表读取长期 API key。Kimi For Coding 的 sk-kimi-... key 从 kimi.com/code 控制台获取，
// 长期有效、与 kimi-code CLI 是否运行无关——用它调 /usages 可在不跑 CLI 时也拿到
// 订阅额度（对齐 cc-switch 的 query_kimi：同端点、Bearer 换成长期 key，2026-10-08 实测
// HTTP 200）。未配置 [quota] 时回退既有 access_token 路径，行为不变。
//
// 安全边界：key 属敏感凭据，明文存 config.toml 是有意取舍——与 cc-switch（明文存
// SQLite）一致，本机单用户场景风险可接受；若需加密须引入 DPAPI，复杂度收益比差，
// 暂不做（勿当疏漏"修复"）。
package quota

import (
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

// APIKeyLoader 按 mtime 热加载 config.toml [quota] 段（对齐 pricing.OverrideLoader
// 的 mtime 重读模式；本项目无 TOML 第三方依赖，沿用行级解析）。
//
// 配置模板（写入 config.toml 即可热加载，无需重启）：
//
//	[quota]
//	api_key = "sk-kimi-..."
type APIKeyLoader struct {
	path    string
	mtime   int64 // UnixNano；0=尚未加载
	apiKey  atomic.Pointer[string]
	loadMu  sync.Mutex
}

var (
	quotaTableRe = regexp.MustCompile(`^\[\s*quota\s*\]`)
	quotaFieldRe = regexp.MustCompile(`^\s*api_key\s*=\s*"(.*)"\s*(?:#.*)?$`)
	tableStartRe = regexp.MustCompile(`^\s*\[`)
)

// NewAPIKeyLoader 创建加载器并立即加载一次。
func NewAPIKeyLoader(path string) *APIKeyLoader {
	l := &APIKeyLoader{path: path}
	l.ReloadIfChanged()
	return l
}

// ReloadIfChanged config.toml 变更时重读 [quota]。
func (l *APIKeyLoader) ReloadIfChanged() {
	l.loadMu.Lock()
	defer l.loadMu.Unlock()
	fi, err := os.Stat(l.path)
	if err != nil {
		// 文件确认不存在（用户整体删除配置文件）→ 清 key 回退 token 路径
		//（P3-3 评审修复）。仅 IsNotExist：其他 stat 错误（网络盘/权限瞬时
		// 失败）可能是抖动，保留旧 key 等下次轮询，避免 key 路径来回抖动。
		if os.IsNotExist(err) {
			l.apiKey.Store(nil)
			l.mtime = 0
		}
		return
	}
	mtime := fi.ModTime().UnixNano()
	if l.mtime != 0 && mtime == l.mtime {
		return
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	if key := parseQuotaAPIKey(data); key != "" {
		l.apiKey.Store(&key)
	} else {
		l.apiKey.Store(nil)
	}
	l.mtime = mtime
}

// Key 返回当前长期 API key（未配置为空串；并发安全）。
func (l *APIKeyLoader) Key() string {
	if p := l.apiKey.Load(); p != nil {
		return *p
	}
	return ""
}

// parseQuotaAPIKey 行级解析 [quota] 表的 api_key（值内不做转义展开，sk-kimi key
// 为 ASCII 无转义字符；写配置时保持单行字面量即可）。
func parseQuotaAPIKey(data []byte) string {
	inQuota := false
	for len(data) > 0 {
		idx := strings.IndexByte(string(data), '\n')
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
		if quotaTableRe.MatchString(ln) {
			inQuota = true
			continue
		}
		if tableStartRe.MatchString(ln) {
			inQuota = false
			continue
		}
		if !inQuota {
			continue
		}
		if m := quotaFieldRe.FindStringSubmatch(ln); m != nil {
			return m[1]
		}
	}
	return ""
}
