package quota

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// realPayload 是 2026-08-18 从 /usages 实际抓取的响应结构（数字为字符串）。
func realPayload() map[string]any {
	return map[string]any{
		"usage": map[string]any{
			"limit":     "100",
			"used":      "36",
			"remaining": "64",
			"resetTime": "2026-08-20T11:11:02.023152Z",
		},
		"limits": []any{
			map[string]any{
				"window": map[string]any{"duration": float64(300), "timeUnit": "TIME_UNIT_MINUTE"},
				"detail": map[string]any{
					"limit":     "100",
					"remaining": "100",
					"resetTime": "2026-08-18T12:11:02.023152Z",
				},
			},
		},
	}
}

func TestParseRealPayload(t *testing.T) {
	windows := parsePayload(realPayload())
	if len(windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(windows))
	}
	// 7d
	if windows[0].Label != "7d" || windows[0].Used != 36 || windows[0].Limit != 100 {
		t.Fatalf("7d window = %+v", windows[0])
	}
	// 5h：detail 无 used，需用 limit - remaining 推导 = 0，clamp 到 0。
	if windows[1].Label != "5h" || windows[1].Used != 0 || windows[1].Limit != 100 {
		t.Fatalf("5h window = %+v", windows[1])
	}
}

func TestDeriveWindowLabel(t *testing.T) {
	cases := []struct {
		dur  float64
		unit string
		want string
	}{
		{300, "TIME_UNIT_MINUTE", "5h"},
		{1440, "TIME_UNIT_MINUTE", "1d"},
		{60, "TIME_UNIT_MINUTE", "1h"},
		{2, "TIME_UNIT_HOUR", "2h"},
		{48, "TIME_UNIT_HOUR", "2d"},
		{7, "TIME_UNIT_DAY", "7d"},
	}
	for _, c := range cases {
		got := deriveWindowLabel(map[string]any{"duration": c.dur, "timeUnit": c.unit})
		if got != c.want {
			t.Errorf("deriveWindowLabel(%v,%s) = %q, want %q", c.dur, c.unit, got, c.want)
		}
	}
}

func TestCountdown(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		reset time.Time
		want  string
	}{
		{now.Add(2*time.Hour + 18*time.Minute), "~2h18m"},
		{now.Add(3*24*time.Hour + 2*time.Hour), "~3d2h"},
		{now.Add(-time.Hour), "~reset"},
		{now.Add(5 * time.Minute), "~5m"},
	}
	for _, c := range cases {
		got := Countdown(c.reset, now)
		if got != c.want {
			t.Errorf("Countdown(%v) = %q, want %q", c.reset, got, c.want)
		}
	}
}

func TestBar(t *testing.T) {
	// 空块用 U+2593「▓」（与 █ 同宽同高同顶，微软雅黑下只有它不窄不矮）。
	if b := Bar(0.36); utf8.RuneCountInString(b) != BarWidth || !strings.Contains(b, "█") || !strings.Contains(b, "▓") {
		t.Fatalf("bar = %q", b)
	}
	if b := Bar(1.0); b != "██████████" {
		t.Fatalf("bar full = %q", b)
	}
	if b := Bar(0); b != "▓▓▓▓▓▓▓▓▓▓" {
		t.Fatalf("bar empty = %q", b)
	}
}

func TestBestWindow(t *testing.T) {
	q := &Quota{Windows: []Window{{Label: "7d", Used: 36, Limit: 100}, {Label: "5h", Used: 90, Limit: 100}}}
	w, ok := q.BestWindow()
	if !ok || w.Label != "5h" {
		t.Fatalf("best = %+v", w)
	}
}

func TestBarFloor(t *testing.T) {
	// 对齐参考 Math.floor：36% * 10 = 3.6 → 3 格（而非 round 的 4 格）。
	if b := Bar(0.36); utf8.RuneCountInString(b) != BarWidth {
		t.Fatalf("bar length = %d", utf8.RuneCountInString(b))
	}
	if b := Bar(0.36); strings.Count(b, "█") != 3 {
		t.Fatalf("bar(0.36) filled = %d, want 3", strings.Count(b, "█"))
	}
	// 空块须为 U+2593「▓」：与 █ 同宽同高同顶（微软雅黑下其他空心字符都窄/矮），
	// 保证托盘菜单中柱条总长与高度恒定。
	if b := Bar(0.36); strings.Count(b, "▓") != 7 {
		t.Fatalf("bar(0.36) empty = %d, want 7", strings.Count(b, "▓"))
	}
	if b := Bar(0.999); strings.Count(b, "█") != 9 {
		t.Fatalf("bar(0.999) filled = %d, want 9", strings.Count(b, "█"))
	}
	if b := Bar(1.0); strings.Count(b, "█") != 10 {
		t.Fatalf("bar(1.0) filled = %d, want 10", strings.Count(b, "█"))
	}
}

func TestCountdownZeroMinute(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	// 不足 1 分钟对齐参考 ~0m。
	if got := Countdown(now.Add(30*time.Second), now); got != "~0m" {
		t.Fatalf("Countdown(<1m) = %q, want ~0m", got)
	}
}

func TestParsePayloadDetailFallback(t *testing.T) {
	// limits[] 缺 detail 时回退到 item 顶层（对齐 `detail = item.detail ?? item`）。
	payload := map[string]any{
		"limits": []any{
			map[string]any{
				"window":    map[string]any{"duration": float64(300), "timeUnit": "TIME_UNIT_MINUTE"},
				"limit":     "100",
				"used":      "20",
				"resetTime": "2026-08-18T12:00:00Z",
			},
		},
	}
	windows := parsePayload(payload)
	if len(windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(windows))
	}
	if windows[0].Used != 20 || windows[0].Limit != 100 {
		t.Fatalf("window = %+v", windows[0])
	}
}

// newQuotaClient 构造测试用 Client（凭据内容为空表示文件不存在）。
func newQuotaClient(t *testing.T, credContent, cacheContent string) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	credPath := filepath.Join(dir, "kimi-code.json")
	cachePath := filepath.Join(dir, "quota.json")
	if credContent != "" {
		if err := os.WriteFile(credPath, []byte(credContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if cacheContent != "" {
		if err := os.WriteFile(cachePath, []byte(cacheContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewClient(credPath, cachePath), cachePath
}

func TestRefreshNoCredentialsDeletesCache(t *testing.T) {
	c, cachePath := newQuotaClient(t, "", `{"fetchedAt":"2026-08-18T00:00:00Z","windows":[]}`)
	err := c.Refresh(time.Now())
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatal("cache should be deleted when credentials missing")
	}
	if c.Get(time.Now()) != nil {
		t.Fatal("in-memory cache should be cleared")
	}
}

func TestRefreshEmptyTokenDeletesCache(t *testing.T) {
	c, cachePath := newQuotaClient(t, `{"access_token":""}`, `{"fetchedAt":"2026-08-18T00:00:00Z","windows":[]}`)
	if err := c.Refresh(time.Now()); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatal("cache should be deleted when access_token empty")
	}
}

func TestRefreshUnauthorizedLogoutDeletesCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c, cachePath := newQuotaClient(t, `{"access_token":"tok"}`, `{"fetchedAt":"2026-08-18T00:00:00Z","windows":[]}`)
	c.usagesURL = srv.URL
	err := c.Refresh(time.Now())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	// 401 且无 refresh_token → 登出，删缓存。
	if _, err := os.Stat(cachePath); !os.IsNotExist(err) {
		t.Fatal("cache should be deleted on logout (401 without refresh_token)")
	}
}

func TestRefreshUnauthorizedKeepsCacheWithRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c, cachePath := newQuotaClient(t, `{"access_token":"tok","refresh_token":"rt"}`, `{"fetchedAt":"2026-08-18T00:00:00Z","windows":[]}`)
	c.usagesURL = srv.URL
	if err := c.Refresh(time.Now()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	// 401 但有 refresh_token → 仅 access_token 过期，保留旧缓存。
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatal("cache should be kept when refresh_token present")
	}
}

func TestCredentialsState(t *testing.T) {
	c, _ := newQuotaClient(t, "", "")
	if status, _ := c.CredentialsState(); status != CredentialsMissing {
		t.Fatalf("status = %v, want Missing", status)
	}
	c, _ = newQuotaClient(t, `{"access_token":""}`, "")
	if status, _ := c.CredentialsState(); status != CredentialsNoToken {
		t.Fatalf("status = %v, want NoToken", status)
	}
	c, _ = newQuotaClient(t, `{"access_token":"tok"}`, "")
	if status, hasRefresh := c.CredentialsState(); status != CredentialsOK || hasRefresh {
		t.Fatalf("status = %v hasRefresh = %v", status, hasRefresh)
	}
	c, _ = newQuotaClient(t, `{"access_token":"tok","refresh_token":"rt"}`, "")
	if status, hasRefresh := c.CredentialsState(); status != CredentialsOK || !hasRefresh {
		t.Fatalf("status = %v hasRefresh = %v", status, hasRefresh)
	}
}

// TestCredentialsStateCached 文件未变化时状态缓存命中（mtime/size 相同不重读）；
// 内容变化（mtime/size 变）后重读出新状态。
func TestCredentialsStateCached(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kimi-code.json")
	c := NewClient(path, filepath.Join(dir, "quota.json"))

	if err := os.WriteFile(path, []byte(`{"access_token":"tok"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, hasRefresh := c.CredentialsState(); status != CredentialsOK || hasRefresh {
		t.Fatalf("first: status=%v hasRefresh=%v", status, hasRefresh)
	}

	// 记录首次状态后，确保 mtime/size 不变化的第二次调用命中缓存。
	firstState := c.credsState
	firstMtime := c.credsMtime
	if status, _ := c.CredentialsState(); status != firstState {
		t.Fatalf("cached status = %v; want %v", status, firstState)
	}
	if c.credsMtime != firstMtime {
		t.Fatal("mtime 缓存应保持不变（未命中重读）")
	}

	// 改内容（refresh_token 追加，mtime/size 变化）→ 重读。
	if err := os.WriteFile(path, []byte(`{"access_token":"tok","refresh_token":"rt"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, hasRefresh := c.CredentialsState(); status != CredentialsOK || !hasRefresh {
		t.Fatalf("after change: status=%v hasRefresh=%v; want OK,true", status, hasRefresh)
	}
}

// TestCredentialsStateFileAppears 文件从缺失→出现触发重读（mtime 占位 -1 失效缓存）。
func TestCredentialsStateFileAppears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kimi-code.json")
	c := NewClient(path, filepath.Join(dir, "quota.json"))

	if status, _ := c.CredentialsState(); status != CredentialsMissing {
		t.Fatalf("status = %v, want Missing", status)
	}
	if err := os.WriteFile(path, []byte(`{"access_token":"tok"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, _ := c.CredentialsState(); status != CredentialsOK {
		t.Fatalf("after file appears status = %v, want OK", status)
	}
}
