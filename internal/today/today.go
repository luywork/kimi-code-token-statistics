// Package today 维护"今日用量"内存缓存：由 internal/scan 的 today-only 轻量模式
// 低频刷新（对齐配额 5 分钟节奏的独立 goroutine），托盘菜单与详情窗口同进程内存直读，
// 同一份 TodayTotals 来源，保证两处"今日总 token"严格一致（方案 §5.2.3 的 D2a）。
package today

import (
	"sync"
	"time"

	"kimi-hud/internal/scan"
)

// refreshInterval 刷新节奏：对齐配额 5 分钟，与配额拉取天然错峰
// （配额随 5s 轮询在 TTL 过期时刷新，today 走独立 5min ticker）。
const refreshInterval = 5 * time.Minute

// Totals 今日用量缓存快照（托盘/窗口共用展示来源）。
type Totals struct {
	Date           string           `json:"date"` // YYYY-MM-DD（本地日历日，跨天 key）
	Tokens         scan.TokenTotals `json:"tokens"`
	Requests       uint64           `json:"requests"`
	PricedRequests uint64           `json:"pricedRequests"`
	PricedCostCny  string           `json:"pricedCostCny"` // 已定价记录的估算费用（空串 = 未配置价格）
	LimitsReached  bool             `json:"limitsReached"`
	Available      bool             `json:"available"` // 成功扫描过一次后为 true
	ScannedFiles   uint64           `json:"scannedFiles"`
	ScannedLines   uint64           `json:"scannedLines"`
	UpdatedAt      time.Time        `json:"updatedAt"`
}

// Manager 今日用量缓存管理器。
type Manager struct {
	home string
	now  func() time.Time

	mu    sync.RWMutex
	cache *Totals
	// everSucceeded 是否曾成功扫过一次（跨天缓存失效后仍为 true）。Get 用它区分
	// "从未扫描到数据"（程序异常/数据目录缺失）与"跨天待下次刷新"（数据完好，
	// 只是新一天还没扫）——后者应展示"更新中"而非"未检测到数据"（R2-4 评审修复）。
	everSucceeded bool
}

// NewManager 创建管理器；home 为 Kimi 数据主目录（~/.kimi-code，KIMI_CODE_HOME 覆盖由 scan 解析）。
func NewManager(home string) *Manager {
	return &Manager{home: home, now: time.Now}
}

// Get 返回今日用量快照。跨天后缓存日期不匹配时返回今日的空值（零），
// 而不是昨日残留；下次低频刷新（≤5min）后自动填充新一天的扫描结果。
// Available 语义：该快照是否为"成功扫描出的数据"——跨天空值返回 Available=false，
// 但 everSucceeded 标记程序是否曾成功扫过，供展示层区分"更新中"与"未检测到数据"。
func (m *Manager) Get() Totals {
	todayKey := dayKey(m.now())
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cache != nil && m.cache.Date == todayKey {
		return *m.cache
	}
	return Totals{Date: todayKey}
}

// EverSucceeded 是否曾成功扫出过今日数据（跨天后仍为 true）。供展示层区分
// "跨天待刷新（数据完好，显示更新中）"与"从未检测到数据（程序异常）"。
func (m *Manager) EverSucceeded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.everSucceeded
}

// Refresh 同步刷新一次并返回当前快照（供一次性工具与测试使用）。
func (m *Manager) Refresh() Totals {
	m.refresh()
	return m.Get()
}

// Run 独立 goroutine 入口：立即刷新一次，之后按 refreshInterval 低频刷新。
// 扫描失败（如 sessions 目录缺失）时保留旧缓存，Get 的跨天兜底保证不返回过期日期。
func (m *Manager) Run(stop <-chan struct{}) {
	m.refresh()
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			m.refresh()
		}
	}
}

func (m *Manager) refresh() {
	now := m.now()
	start := dayStartMs(now)
	rpt, err := scan.ScanToday(m.home, start, now.UnixMilli(), nil)
	if err != nil {
		return
	}
	m.store(rpt, now)
}

// store 用一次扫描结果覆盖今日缓存（refresh 与 UpdateFromScan 共用）。
func (m *Manager) store(rpt *scan.TodayReport, now time.Time) {
	t := Totals{
		Date:           dayKey(now),
		Tokens:         rpt.Tokens,
		Requests:       rpt.Requests,
		PricedRequests: rpt.PricedRequests,
		PricedCostCny:  rpt.PricedCostCny,
		LimitsReached:  rpt.LimitsReached,
		Available:      true,
		ScannedFiles:   rpt.ScannedFiles,
		ScannedLines:   rpt.ScannedLines,
		UpdatedAt:      now,
	}
	m.mu.Lock()
	m.cache = &t
	m.everSucceeded = true
	m.mu.Unlock()
}

// UpdateFromScan 用详情窗口"今日"档的一次实时扫描结果回填缓存（scan_usage RPC 完成后调用），
// 使顶部"今日总 token"实时卡与下方"今日"档同源同步，消除两处数据的更新时间差
// （用户要求；订阅额度走独立数据源，更新时间保持不变）。
// 仅接受窗口起点为本日 00:00 的扫描，避免跨天或其它时间窗口的结果污染缓存。
func (m *Manager) UpdateFromScan(rpt *scan.TodayReport, startMs int64) {
	now := m.now()
	if startMs != dayStartMs(now) {
		return
	}
	m.store(rpt, now)
}

// dayKey 返回本地日历日字符串（YYYY-MM-DD）。
func dayKey(now time.Time) string {
	return now.Format("2006-01-02")
}

// dayStartMs 返回本地日历日 00:00 的 epoch 毫秒。
func dayStartMs(now time.Time) int64 {
	y, mo, d := now.Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, now.Location()).UnixMilli()
}
