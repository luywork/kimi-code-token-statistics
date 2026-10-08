package quota

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseQuotaAPIKey(t *testing.T) {
	cases := []struct {
		name string
		cfg  string
		want string
	}{
		{
			"quota 表在 pricing 之后",
			"[pricing.\"kimi-for-coding\"]\ninput = 6.5\n\n[quota]\napi_key = \"sk-kimi-abc\" # 注释\n",
			"sk-kimi-abc",
		},
		{
			"quota 表在最前",
			"[quota]\napi_key = \"sk-kimi-x\"\n\n[pricing.subscription]\nmonthly_cny = 60.0\n",
			"sk-kimi-x",
		},
		{"无 quota 表", "[pricing.subscription]\nmonthly_cny = 60.0\n", ""},
		{"空值", "[quota]\napi_key = \"\"\n", ""},
		{"无 api_key 字段", "[quota]\nother = 1\n", ""},
		{"quota 表后接其他表不越界", "[quota]\napi_key = \"k1\"\n[models.x]\ny = 2\n", "k1"},
	}
	for _, c := range cases {
		if got := parseQuotaAPIKey([]byte(c.cfg)); got != c.want {
			t.Errorf("%s: parseQuotaAPIKey = %q, want %q", c.name, got, c.want)
		}
	}
}

// newKeyLoader 写 config.toml 并构造 loader（已首载）。
func newKeyLoader(t *testing.T, cfgContent string) (*APIKeyLoader, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewAPIKeyLoader(cfgPath), cfgPath
}

func TestAPIKeyLoaderKey(t *testing.T) {
	l, _ := newKeyLoader(t, "[quota]\napi_key = \"sk-kimi-hot\"\n")
	if got := l.Key(); got != "sk-kimi-hot" {
		t.Fatalf("Key = %q", got)
	}
	// 未配置 → 空串（回退 access_token 路径）。
	l2, _ := newKeyLoader(t, "[pricing.subscription]\nmonthly_cny = 60.0\n")
	if got := l2.Key(); got != "" {
		t.Fatalf("Key = %q, want empty", got)
	}
	// 配置文件不存在 → 空串，不 panic。
	l3 := NewAPIKeyLoader(filepath.Join(t.TempDir(), "missing.toml"))
	if got := l3.Key(); got != "" {
		t.Fatalf("Key = %q, want empty", got)
	}
}

func TestAPIKeyLoaderHotReload(t *testing.T) {
	l, cfgPath := newKeyLoader(t, "[quota]\napi_key = \"k1\"\n")
	if got := l.Key(); got != "k1" {
		t.Fatalf("Key = %q", got)
	}
	// 改内容并强制 mtime 前移（WriteFile 同秒内 mtime 可能不变）。
	future := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(cfgPath, []byte("[quota]\napi_key = \"k2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cfgPath, future, future); err != nil {
		t.Fatal(err)
	}
	l.ReloadIfChanged()
	if got := l.Key(); got != "k2" {
		t.Fatalf("after reload Key = %q, want k2", got)
	}
	// 移除 key → 回退空。
	if err := os.WriteFile(cfgPath, []byte("[pricing]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future2 := future.Add(2 * time.Second)
	if err := os.Chtimes(cfgPath, future2, future2); err != nil {
		t.Fatal(err)
	}
	l.ReloadIfChanged()
	if got := l.Key(); got != "" {
		t.Fatalf("after removal Key = %q, want empty", got)
	}
}

// TestAPIKeyLoaderFileDeleted 整体删除 config.toml → 清 key 回退 token 路径
//（P3-3 评审修复）；不依赖 stat 错误类型以外的瞬时失败（此场景难在单测中构造，
// 收窄为 IsNotExist 语义由代码审查保证）。
func TestAPIKeyLoaderFileDeleted(t *testing.T) {
	l, cfgPath := newKeyLoader(t, "[quota]\napi_key = \"sk-kimi-tmp\"\n")
	if got := l.Key(); got != "sk-kimi-tmp" {
		t.Fatalf("Key = %q", got)
	}
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	l.ReloadIfChanged()
	if got := l.Key(); got != "" {
		t.Fatalf("after file deleted Key = %q, want empty", got)
	}
	// 重建文件恢复 key（mtime 从 0 重新起算，首次加载语义）。
	if err := os.WriteFile(cfgPath, []byte("[quota]\napi_key = \"sk-kimi-back\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l.ReloadIfChanged()
	if got := l.Key(); got != "sk-kimi-back" {
		t.Fatalf("after recreate Key = %q, want sk-kimi-back", got)
	}
}

// TestRefreshUsesAPIKey key 优先：无 credentials 文件也成功，Bearer 用 key。
func TestRefreshUsesAPIKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"usage":{"limit":"100","used":"22","remaining":"78","resetTime":"2026-10-08T11:11:02Z"},` +
			`"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"100","remaining":"100","resetTime":"2026-10-08T08:11:02Z"}}],` +
			`"usages":{"limit_5h":{"used_ratio":0,"reset_time":"2026-10-08T08:11:01Z"},"limit_7d":{"used_ratio":0.220754,"reset_time":"2026-10-08T11:11:01Z"}}}`))
	}))
	defer srv.Close()

	c, cachePath := newQuotaClient(t, "", "")
	c.usagesURL = srv.URL
	l, _ := newKeyLoader(t, "[quota]\napi_key = \"sk-kimi-test\"\n")
	c.SetAPIKeyLoader(l)

	if err := c.Refresh(time.Now()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if gotAuth != "Bearer sk-kimi-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	q := c.Get(time.Now())
	if q == nil || len(q.Windows) != 2 {
		t.Fatalf("quota = %+v", q)
	}
	// 服务端 used_ratio 已回填。
	for _, w := range q.Windows {
		switch w.Label {
		case "7d":
			if w.UsedRatio != 0.220754 {
				t.Fatalf("7d usedRatio = %v", w.UsedRatio)
			}
		case "5h":
			if w.UsedRatio != 0 {
				t.Fatalf("5h usedRatio = %v", w.UsedRatio)
			}
		}
	}
	if !c.UsingAPIKey() {
		t.Fatal("UsingAPIKey = false, want true")
	}
	// 磁盘缓存写入且含 usedRatio。
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache missing: %v", err)
	}
}

// TestAPIKeyUnauthorizedKeepsCache key 失效（401）不删缓存，仅记错误。
func TestAPIKeyUnauthorizedKeepsCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c, cachePath := newQuotaClient(t, `{"access_token":"tok","refresh_token":"rt"}`, `{"fetchedAt":"2026-08-18T00:00:00Z","windows":[]}`)
	c.usagesURL = srv.URL
	l, _ := newKeyLoader(t, "[quota]\napi_key = \"bad\"\n")
	c.SetAPIKeyLoader(l)

	err := c.Refresh(time.Now())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(cachePath); statErr != nil {
		t.Fatal("cache deleted on 401 with API key, want kept")
	}
	if last := c.Last(); !last.Unauthorized {
		t.Fatal("Last().Unauthorized = false")
	}
}

// TestCredentialsStateWithAPIKey 配置 key 后凭据状态恒 OK（credentials 文件不存在也一样）。
func TestCredentialsStateWithAPIKey(t *testing.T) {
	c, _ := newQuotaClient(t, "", "")
	l, _ := newKeyLoader(t, "[quota]\napi_key = \"sk\"\n")
	c.SetAPIKeyLoader(l)
	if status, _ := c.CredentialsState(); status != CredentialsOK {
		t.Fatalf("status = %d, want OK", status)
	}
}

// TestAPIKeyHotReloadSwitchesPath 运行中新增 key → 切 key 路径；删除 → 回退 token 路径。
func TestAPIKeyHotReloadSwitchesPath(t *testing.T) {
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		w.Write([]byte(`{"usage":{"limit":"100","used":"1","remaining":"99","resetTime":"2026-10-08T11:11:02Z"}}`))
	}))
	defer srv.Close()

	c, _ := newQuotaClient(t, `{"access_token":"tok"}`, "")
	c.usagesURL = srv.URL
	l, cfgPath := newKeyLoader(t, "")
	c.SetAPIKeyLoader(l)

	if err := c.Refresh(time.Now()); err != nil {
		t.Fatalf("refresh1: %v", err)
	}
	if auths[len(auths)-1] != "Bearer tok" {
		t.Fatalf("auth = %q, want token path", auths[len(auths)-1])
	}

	// 写入 key（mtime 前移触发热加载）→ Bearer 切 key。
	future := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(cfgPath, []byte("[quota]\napi_key = \"sk-kimi-later\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cfgPath, future, future); err != nil {
		t.Fatal(err)
	}
	c.APIKeyReloadIfChanged()
	// 缓存仍在 TTL 内，Get 命中不刷新；直接再 Refresh 强制走网络。
	if err := c.Refresh(time.Now()); err != nil {
		t.Fatalf("refresh2: %v", err)
	}
	if auths[len(auths)-1] != "Bearer sk-kimi-later" {
		t.Fatalf("auth = %q, want key path", auths[len(auths)-1])
	}
}

func TestParsePayloadUsedRatioFillback(t *testing.T) {
	payload := realPayload()
	payload["usages"] = map[string]any{
		"limit_5h": map[string]any{"used_ratio": 0.0},
		"limit_7d": map[string]any{"used_ratio": 0.220754},
	}
	windows := parsePayload(payload)
	if len(windows) != 2 {
		t.Fatalf("windows = %d", len(windows))
	}
	if windows[0].UsedRatio != 0.220754 || windows[1].UsedRatio != 0 {
		t.Fatalf("ratios = %v / %v", windows[0].UsedRatio, windows[1].UsedRatio)
	}
	// 无 usages 段 → -1（未知）。
	for _, w := range parsePayload(realPayload()) {
		if w.UsedRatio != -1 {
			t.Fatalf("%s usedRatio = %v, want -1", w.Label, w.UsedRatio)
		}
	}
	// usages 缺某个 key → 对应窗口保持 -1。
	payload["usages"] = map[string]any{"limit_7d": map[string]any{"used_ratio": 0.5}}
	windows = parsePayload(payload)
	if windows[0].UsedRatio != 0.5 || windows[1].UsedRatio != -1 {
		t.Fatalf("ratios = %v / %v", windows[0].UsedRatio, windows[1].UsedRatio)
	}
	// 服务端异常值 >1 丢弃（P3-2）→ 保持 -1 走推导，避免前端百分比 >100%。
	payload["usages"] = map[string]any{"limit_7d": map[string]any{"used_ratio": 1.5}}
	windows = parsePayload(payload)
	if windows[0].UsedRatio != -1 {
		t.Fatalf("ratio = %v, want -1 (outlier >1 dropped)", windows[0].UsedRatio)
	}
}

func TestWindowRatioFallback(t *testing.T) {
	// 服务端比率优先（>0）。
	w := Window{Label: "7d", Used: 22, Limit: 100, UsedRatio: 0.220754}
	if r := w.Ratio(); r != 0.220754 {
		t.Fatalf("Ratio = %v", r)
	}
	// 未下发（零值/-1）→ Used/Limit 推导并 clamp。
	w2 := Window{Label: "7d", Used: 36, Limit: 100}
	if r := w2.Ratio(); r != 0.36 {
		t.Fatalf("Ratio = %v", r)
	}
	// clamp：remaining > limit 的 bonus 场景 used 已在 quotaWindow clamp，
	// 但 Ratio 的推导路径仍须兜底。
	w3 := Window{Label: "7d", Used: 200, Limit: 100}
	if r := w3.Ratio(); r != 1 {
		t.Fatalf("Ratio = %v, want 1", r)
	}
	// 服务端比率为 0 → 走推导分支（结果同为 0，语义等价）。
	w4 := Window{Label: "5h", Used: 0, Limit: 100, UsedRatio: 0}
	if r := w4.Ratio(); r != 0 {
		t.Fatalf("Ratio = %v", r)
	}
}

// TestUnmarshalWindowUsedRatioDefault 旧版磁盘缓存无 usedRatio 字段 → -1。
func TestUnmarshalWindowUsedRatioDefault(t *testing.T) {
	var w Window
	if err := json.Unmarshal([]byte(`{"label":"7d","used":36,"limit":100}`), &w); err != nil {
		t.Fatal(err)
	}
	if w.UsedRatio != -1 {
		t.Fatalf("UsedRatio = %v, want -1", w.UsedRatio)
	}
	// 显式 0 是合法值，不得被覆盖。
	if err := json.Unmarshal([]byte(`{"usedRatio":0}`), &w); err != nil {
		t.Fatal(err)
	}
	if w.UsedRatio != 0 {
		t.Fatalf("UsedRatio = %v, want 0", w.UsedRatio)
	}
}
