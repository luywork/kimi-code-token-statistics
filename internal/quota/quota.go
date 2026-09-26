// Package quota 获取并缓存 Kimi 托管订阅额度（对齐 kimi-code-hud 的 quota.mjs）。
package quota

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UsagesURL 官方配额端点。
const UsagesURL = "https://api.kimi.com/coding/v1/usages"

// QuotaTTL 配额缓存有效期（5 分钟刷新一次订阅额度，无需 60s 高频轮询）。
const QuotaTTL = 5 * time.Minute

// Window 一个配额窗口（5h / 7d 等）。
type Window struct {
	Label     string    `json:"label"`
	Used      float64   `json:"used"`
	Limit     float64   `json:"limit"`
	Remaining float64   `json:"remaining"`
	ResetTime time.Time `json:"resetTime"`
}

// Quota 一次配额快照。
type Quota struct {
	FetchedAt time.Time `json:"fetchedAt"`
	Windows   []Window  `json:"windows"`
}

// ErrUnauthorized 表示凭据无效（过期或登出）。
var ErrUnauthorized = errors.New("quota unauthorized")

// ErrNoCredentials 表示凭据文件缺失、损坏或 access_token 为空（未登录）。
var ErrNoCredentials = errors.New("quota no credentials")

// CredentialsStatus 描述本地凭据文件的状态（用于菜单「未登录」语义）。
type CredentialsStatus int

const (
	CredentialsOK      CredentialsStatus = iota // 有 access_token
	CredentialsMissing                          // 凭据文件缺失/不可读
	CredentialsNoToken                          // 文件存在但无有效 access_token
)

// Client 配额客户端：带 TTL 缓存 + 原子写 + 后台刷新语义。
type Client struct {
	credentialsPath string
	cachePath       string
	usagesURL       string
	httpClient      *http.Client

	mu    sync.Mutex
	cache *Quota
	// refreshing 防止并发重复刷新。
	refreshing bool
	// 最近一次刷新结果（供菜单显示数据新鲜度与错误状态）。
	lastSuccessAt time.Time
	lastError     error
	lastErrorAt   time.Time

	// credsState* 凭据状态 mtime 缓存：CredentialsState 在详情窗打开期间每 500ms
	// 被 buildLive 调用，若每次都 os.ReadFile 属于无谓高频 IO（R2-2 评审修复）；
	// mtime 变化时才重读，状态本身只在文件变更时翻转。
	credsMu         sync.Mutex
	credsState      CredentialsStatus
	credsHasRefresh bool
	credsHasState   bool
	credsMtime      int64
	credsSize       int64
}

// LastResult 描述最近一次配额刷新结果。
type LastResult struct {
	SuccessAt    time.Time // 最近成功时间（零值=从未成功）
	Error        error     // 最近错误
	ErrorAt      time.Time
	Unauthorized bool // 是否因登录态过期（401）
}

// Last 返回最近一次刷新结果（线程安全）。
func (c *Client) Last() LastResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return LastResult{
		SuccessAt:    c.lastSuccessAt,
		Error:        c.lastError,
		ErrorAt:      c.lastErrorAt,
		Unauthorized: c.lastError == ErrUnauthorized,
	}
}

// SuccessAt 返回最近成功获取配额的时间（线程安全）。
func (c *Client) SuccessAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSuccessAt
}

// NewClient 创建配额客户端。
func NewClient(credentialsPath, cachePath string) *Client {
	return &Client{
		credentialsPath: credentialsPath,
		cachePath:       cachePath,
		usagesURL:       UsagesURL,
		httpClient:      &http.Client{Timeout: 8 * time.Second},
	}
}

// Get 返回有效缓存（fetchedAt 距今 < TTL）；缓存过期时返回 nil 并立即后台刷新。
func (c *Client) Get(now time.Time) *Quota {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache != nil && now.Sub(c.cache.FetchedAt) < QuotaTTL {
		if c.lastSuccessAt.IsZero() {
			c.lastSuccessAt = c.cache.FetchedAt
		}
		return c.cache
	}
	// 读磁盘缓存兜底（上次进程留下的）。
	if c.cache == nil {
		c.cache = c.loadDiskCache()
		if c.cache != nil && now.Sub(c.cache.FetchedAt) < QuotaTTL {
			if c.lastSuccessAt.IsZero() {
				c.lastSuccessAt = c.cache.FetchedAt
			}
			return c.cache
		}
	}
	return nil
}

// ShouldRefresh 缓存已过期（含磁盘兜底）时返回 true。
func (c *Client) ShouldRefresh() bool {
	return c.Get(time.Now()) == nil
}

// RefreshNow 后台异步刷新配额（失败静默降级，不阻塞调用方）。
func (c *Client) RefreshNow() {
	c.mu.Lock()
	if c.refreshing {
		c.mu.Unlock()
		return
	}
	c.refreshing = true
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			c.refreshing = false
			c.mu.Unlock()
		}()
		_ = c.Refresh(time.Now())
	}()
}

// Refresh 同步刷新配额；成功写缓存，失败返回错误（调用方决定降级策略）。
func (c *Client) Refresh(now time.Time) error {
	cred, err := c.readCreds()
	if err != nil {
		// 凭据缺失/损坏/无 access_token：未登录，删除磁盘缓存（对齐 refreshQuota）。
		c.recordError(now, err)
		c.deleteDiskCache()
		return err
	}
	q, err := c.fetch(cred.accessToken, now)
	if err != nil {
		// 401/403 且 refresh_token 为空 → 视为登出，删除缓存；有 refresh_token →
		// 仅 access_token 过期，保留旧缓存等 Kimi CLI 懒刷新（对齐 refreshQuota）。
		if errors.Is(err, ErrUnauthorized) && cred.refreshToken == "" {
			c.deleteDiskCache()
		}
		c.recordError(now, err)
		return err
	}
	c.mu.Lock()
	c.cache = q
	c.lastSuccessAt = now
	c.lastError = nil
	c.mu.Unlock()
	writeAtomicJSON(c.cachePath, q)
	return nil
}

// deleteDiskCache 清空内存缓存并删除磁盘配额缓存（登出/未登录时清理旧数据）。
func (c *Client) deleteDiskCache() {
	c.mu.Lock()
	c.cache = nil
	c.mu.Unlock()
	if err := os.Remove(c.cachePath); err != nil && !os.IsNotExist(err) {
		// 缓存文件不存在或删除失败均静默忽略。
	}
}

// credInfo 本地凭据中的 token 字段。
type credInfo struct {
	accessToken  string
	refreshToken string
}

// readCreds 读取凭据文件；缺失/损坏/无 access_token 时返回 ErrNoCredentials。
func (c *Client) readCreds() (credInfo, error) {
	data, err := os.ReadFile(c.credentialsPath)
	if err != nil {
		return credInfo{}, ErrNoCredentials
	}
	var cred struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &cred); err != nil {
		return credInfo{}, ErrNoCredentials
	}
	if cred.AccessToken == "" {
		return credInfo{refreshToken: cred.RefreshToken}, ErrNoCredentials
	}
	return credInfo{accessToken: cred.AccessToken, refreshToken: cred.RefreshToken}, nil
}

// CredentialsState 返回凭据文件状态与是否携带 refresh_token（供菜单「未登录」判定）。
// 第二返回值语义是"是否携带 refresh_token"，与"状态是否有效"无关——有 access_token
// 但无 refresh_token 时 ok=false 却已认证（R1-1 评审指出 display/live 曾误用）。
// 结果按文件 mtime/size 缓存：详情窗打开期间 buildLive 每 500ms 调用本函数，
// 命中缓存时仅 os.Stat（轻量）而不再读文件内容，文件变化时才真正重读（R2-2 评审修复）。
func (c *Client) CredentialsState() (CredentialsStatus, bool) {
	c.credsMu.Lock()
	defer c.credsMu.Unlock()
	mtime, size, statErr := statCredFile(c.credentialsPath)
	if statErr != nil {
		// 文件缺失/不可读：若上次也是缺失，直接复用缓存状态（缺→现由 statErr
		// 变为 nil 触发重读）；否则记录缺失态。
		if c.credsHasState && c.credsMtime == -1 {
			return c.credsState, c.credsHasRefresh
		}
		c.credsHasState = true
		c.credsMtime, c.credsSize = -1, -1
		return CredentialsMissing, false
	}
	if c.credsHasState && mtime == c.credsMtime && size == c.credsSize {
		// 文件未变化：返回缓存状态，不读文件内容。
		return c.credsState, c.credsHasRefresh
	}
	// 无缓存或文件已变化：读文件解析。
	status, hasRefresh := credsStatusFromFile(c.credentialsPath)
	c.credsState = status
	c.credsHasRefresh = hasRefresh
	c.credsHasState = true
	c.credsMtime, c.credsSize = mtime, size
	return status, hasRefresh
}

// statCredFile 仅 stat 凭据文件（不读内容），返回 mtime（ms）与 size；失败返回错误。
func statCredFile(path string) (int64, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return -1, -1, err
	}
	return fi.ModTime().UnixMilli(), fi.Size(), nil
}

// credsStatusFromFile 读凭据文件并解析状态（仅在文件变化/无缓存时调用）。
func credsStatusFromFile(path string) (CredentialsStatus, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CredentialsMissing, false
	}
	var cred struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &cred); err != nil {
		return CredentialsNoToken, cred.RefreshToken != ""
	}
	if cred.AccessToken == "" {
		return CredentialsNoToken, cred.RefreshToken != ""
	}
	return CredentialsOK, cred.RefreshToken != ""
}

func (c *Client) recordError(now time.Time, err error) {
	c.mu.Lock()
	c.lastError = err
	c.lastErrorAt = now
	c.mu.Unlock()
}

// fetch 请求 /usages 并解析。
func (c *Client) fetch(token string, now time.Time) (*Quota, error) {
	req, err := http.NewRequest(http.MethodGet, c.usagesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, errors.New("quota transient error")
	case resp.StatusCode != http.StatusOK:
		return nil, errors.New("quota unexpected status")
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	windows := parsePayload(payload)
	if len(windows) == 0 {
		return nil, errors.New("quota payload has no windows")
	}
	return &Quota{FetchedAt: now, Windows: windows}, nil
}

// parsePayload 解析 5h/7d 窗口（对齐 parseQuotaPayload + quotaValues + deriveWindowLabel）。
func parsePayload(payload map[string]any) []Window {
	var windows []Window

	// 顶层 usage -> weekly（7d）。
	if usage, ok := payload["usage"].(map[string]any); ok {
		if w, ok := quotaWindow(usage, "7d"); ok {
			windows = append(windows, w)
		}
	}

	// limits[] -> 5h 等（无 label 的窗口跳过，对齐源码）。
	if limits, ok := payload["limits"].([]any); ok {
		for _, item := range limits {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			wObj, _ := obj["window"].(map[string]any)
			// detail 缺失时回退到 item 顶层（对齐参考 `detail = item.detail ?? item`）。
			det, ok := obj["detail"].(map[string]any)
			if !ok {
				det = obj
			}
			label := deriveWindowLabel(wObj)
			if label == "" {
				continue
			}
			if w, ok := quotaWindow(det, label); ok {
				windows = append(windows, w)
			}
		}
	}
	return windows
}

// quotaWindow 从 detail 对象提取 used/limit/reset。
func quotaWindow(detail map[string]any, label string) (Window, bool) {
	limit, ok := num(detail["limit"])
	if !ok || limit <= 0 {
		return Window{}, false
	}
	used, ok := num(detail["used"])
	if !ok {
		// 用 remaining 推导（bonus/overflow 可能 remaining > limit，clamp 到 0 用量）。
		if rem, ok2 := num(detail["remaining"]); ok2 {
			used = limit - rem
		}
	}
	if used < 0 {
		used = 0
	}
	if used > limit {
		used = limit
	}
	w := Window{
		Label: label,
		Used:  used,
		Limit: limit,
	}
	if rem, ok := num(detail["remaining"]); ok {
		w.Remaining = rem
	} else {
		w.Remaining = limit - used
	}
	if rs, ok := detail["resetTime"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, rs); err == nil {
			w.ResetTime = t
		}
	}
	return w, true
}

// deriveWindowLabel 对齐 deriveWindowLabel。
func deriveWindowLabel(window map[string]any) string {
	if window == nil {
		return ""
	}
	d, ok := num(window["duration"])
	if !ok || d <= 0 {
		return ""
	}
	unit, _ := window["timeUnit"].(string)
	switch {
	case strings.Contains(unit, "MINUTE"):
		if math.Mod(d, 1440) == 0 {
			return itoa(int(d/1440)) + "d"
		}
		if math.Mod(d, 60) == 0 {
			return itoa(int(d/60)) + "h"
		}
		return itoa(int(d)) + "m"
	case strings.Contains(unit, "HOUR"):
		if math.Mod(d, 24) == 0 {
			return itoa(int(d/24)) + "d"
		}
		return itoa(int(d)) + "h"
	case strings.Contains(unit, "DAY"):
		return itoa(int(d)) + "d"
	default:
		return ""
	}
}

// num 从 JSON 值提取非负有限 float64（宽松容错：数字可为字符串）。
func num(v any) (float64, bool) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case json.Number:
		f, _ = n.Float64()
	case string:
		parsed, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, false
	}
	return f, true
}
