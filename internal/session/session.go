// Package session 定位活跃会话并按预算增量读取各 agent 的 wire 日志。
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kimi-hud/internal/metrics"
	"kimi-hud/internal/state"
	"kimi-hud/internal/wire"
)

// 单轮预算（对齐 readBoundedWire 的分片）：main 256KB / subagent 128KB，总预算 1MB。
const (
	mainBudget    = 256 * 1024
	agentBudget   = 128 * 1024
	totalBudget   = 1024 * 1024
	saveInterval  = 2 * time.Second
	relocateEvery = 30 * time.Second
)

// Manager 驱动指标状态机的会话读取器。
type Manager struct {
	sessionsRoot string
	state        *metrics.State
	store        *state.Store

	sessionDir  string
	sessionID   string
	wires       map[string]string // agent -> wire 路径
	readers     map[string]*wire.Reader
	agentCursor int

	// pendingSession* 是 RelocateScan 的扫描结果，由 ApplyPending 在 stMu 锁内应用
	// （锁外扫描 + 锁内切换，避免全目录 IO 阻塞 UI 锁，R2-1 评审修复）。
	pendingSessionDir string
	pendingSessionID  string

	lastSave     time.Time
	lastRelocate time.Time
}

// New 创建会话管理器（自管状态，外部通过 State() 访问）。
func New(sessionsRoot string, store *state.Store) *Manager {
	return &Manager{
		sessionsRoot: sessionsRoot,
		state:        metrics.NewState(),
		store:        store,
		wires:        map[string]string{},
		readers:      map[string]*wire.Reader{},
	}
}

// State 返回当前指标状态（外部只读，写入由 Poll 完成）。
func (m *Manager) State() *metrics.State { return m.state }

// SessionID 返回当前会话 ID（空表示未定位）。
func (m *Manager) SessionID() string { return m.sessionID }

// SessionDir 返回当前会话目录。
func (m *Manager) SessionDir() string { return m.sessionDir }

// wireEntry 一次扫描的 wire 候选。
type wireEntry struct {
	agent string
	path  string
	mtime int64
}

// Relocate 完整重定位：扫描最近活跃会话并立即应用（无节流）。
// 供一次性调用/测试（单 goroutine 无锁场景）。运行期请用 RelocateScan + ApplyPending
// 组合：锁外扫描、锁内应用，避免全目录遍历阻塞 UI 锁（R2-1 评审修复）。
func (m *Manager) Relocate() {
	m.RelocateScanNow()
	m.ApplyPending()
}

// RelocateScan 节流版只读扫描（relocateEvery 内只扫一次）：供 pollLoop 在 stMu 锁外
// 调用，结果由 ApplyPending 在锁内应用。把全目录遍历 + 逐个 os.Stat 移出 UI 锁
// （历史会话多/慢盘时可达百毫秒级，期间 display.BuildMenu 与 heartbeat 全被阻塞，
//
//	R2-1 评审修复）。
func (m *Manager) RelocateScan() {
	now := time.Now()
	if now.Sub(m.lastRelocate) < relocateEvery {
		return
	}
	m.lastRelocate = now
	m.RelocateScanNow()
}

// RelocateScanNow 执行一次只读扫描，把最近活跃会话缓存到 pending（不做任何状态切换）。
func (m *Manager) RelocateScanNow() {
	var mains []wireEntry
	root := m.sessionsRoot
	wdDirs, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, wd := range wdDirs {
		if !wd.IsDir() {
			continue
		}
		wdPath := filepath.Join(root, wd.Name())
		sessDirs, err := os.ReadDir(wdPath)
		if err != nil {
			continue
		}
		for _, sess := range sessDirs {
			if !sess.IsDir() {
				continue
			}
			// 对齐 session-locator 的 ses_/session_ 前缀候选扫描。
			name := sess.Name()
			if !strings.HasPrefix(name, "session_") && !strings.HasPrefix(name, "ses_") {
				continue
			}
			mainPath := filepath.Join(wdPath, sess.Name(), "agents", "main", "wire.jsonl")
			fi, err := os.Stat(mainPath)
			if err != nil {
				continue
			}
			mains = append(mains, wireEntry{agent: "main", path: mainPath, mtime: fi.ModTime().UnixMilli()})
		}
	}
	if len(mains) == 0 {
		return
	}
	sort.Slice(mains, func(i, j int) bool { return mains[i].mtime > mains[j].mtime })
	chosen := mains[0]
	// mainPath 形如 <session>/agents/main/wire.jsonl，向上三级得到会话目录。
	m.pendingSessionDir = filepath.Dir(filepath.Dir(filepath.Dir(chosen.path)))
	m.pendingSessionID = filepath.Base(m.pendingSessionDir)
}

// ApplyPending 应用 RelocateScan 的扫描结果（调用方须持 stMu 锁：切换 state 指针，
// 否则外部会读到半切换态）。幂等：pending 为空或会话未变化时无操作。
// 含少量 IO（装载断点 store.Load + 枚举 agents/），仅在会话实际切换时发生，
// 常量级开销，可接受在锁内执行。
func (m *Manager) ApplyPending() {
	if m.pendingSessionID == "" || m.pendingSessionID == m.sessionID {
		m.pendingSessionDir, m.pendingSessionID = "", ""
		return
	}
	m.sessionID = m.pendingSessionID
	m.sessionDir = m.pendingSessionDir
	// 切换会话：装载新会话的持久化断点，否则重置。
	m.state = m.store.Load(m.sessionID)
	m.wires = map[string]string{}
	m.readers = map[string]*wire.Reader{}
	m.agentCursor = 0
	m.discoverAgents()
	m.pendingSessionDir, m.pendingSessionID = "", ""
}

// discoverAgents 枚举当前会话 agents/ 下的全部 wire 文件。
func (m *Manager) discoverAgents() {
	agentsDir := filepath.Join(m.sessionDir, "agents")
	ents, err := os.ReadDir(agentsDir)
	if err != nil {
		return
	}
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		p := filepath.Join(agentsDir, ent.Name(), "wire.jsonl")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			m.wires[ent.Name()] = p
		}
	}
}

// Poll 增量读取全部 agent wire 并折入状态机，必要时保存断点。
func (m *Manager) Poll(now time.Time) {
	if m.sessionDir == "" {
		// 兜底：pollLoop 已锁外 RelocateScan + 锁内 ApplyPending，此处只在从未扫到
		// 会话（如数据目录缺失）时触发；RelocateScan 节流 30s，低频可接受。
		m.RelocateScan()
		m.ApplyPending()
		if m.sessionDir == "" {
			return
		}
	}
	// 增量发现会话中动态新建的子 agent（对齐参考 enumerateWires 每帧重扫
	// agents/；discoverAgents 幂等，不重置既有断点）。
	m.discoverAgents()
	nowMs := now.UnixMilli()

	budget := int64(totalBudget)
	// main 优先。
	if path, ok := m.wires["main"]; ok {
		m.readAgent("main", path, mainBudget, &budget, nowMs)
	}
	// subagent 轮询（游标 + 128KB/个）。
	subs := make([]string, 0, len(m.wires))
	for name := range m.wires {
		if name != "main" {
			subs = append(subs, name)
		}
	}
	if len(subs) > 0 {
		sort.Strings(subs)
		start := m.agentCursor % len(subs)
		for i := 0; i < len(subs) && budget > 0; i++ {
			name := subs[(start+i)%len(subs)]
			m.readAgent(name, m.wires[name], agentBudget, &budget, nowMs)
		}
		m.agentCursor = (m.agentCursor + 1) % len(subs)
	}

	// 定期保存断点。
	if now.Sub(m.lastSave) >= saveInterval {
		m.store.Save(m.sessionID, m.state)
		m.lastSave = now
	}
	// 重定位由 pollLoop 组合调用（锁外 RelocateScan + 锁内 ApplyPending），
	// Poll 不再负责触发（R2-1 评审修复）。
}

// readAgent 读取单个 agent 的增量并按行折入状态机。
func (m *Manager) readAgent(agent, path string, maxBytes int64, budget *int64, nowMs int64) {
	if *budget <= 0 {
		return
	}
	r := m.readers[agent]
	if r == nil {
		r = &wire.Reader{}
		m.readers[agent] = r
	}
	// 用持久化断点续读。
	if agentState := m.state.Agents[agent]; agentState != nil && r.Offset == 0 {
		r.Offset = agentState.ReaderOffset
		r.Pending = agentState.Pending
		r.TailMarker = agentState.TailMarker
		r.Discarding = agentState.Discarding
	}
	fileSize, err := wire.Stat(path)
	if err != nil {
		return
	}
	readBytes := maxBytes
	if readBytes > *budget {
		readBytes = *budget
	}
	res, err := r.Read(path, fileSize, readBytes)
	if err != nil {
		return
	}
	*budget -= res.BytesRead

	// 轮转：清空该 agent 的读取断点。
	if res.Replaced {
		m.state.ResetAgentReader(agent)
	}

	for _, line := range res.Lines {
		row := parseLine(line)
		if row == nil {
			continue
		}
		m.state.ApplyWireRow(row, agent)
	}
	if agentState := m.state.Agents[agent]; agentState != nil {
		agentState.ReaderOffset = r.Offset
		agentState.Pending = r.Pending
		agentState.TailMarker = r.TailMarker
		agentState.Discarding = r.Discarding
	}
	_ = nowMs
}

// parseLine 解析一行 JSON；失败返回 nil。
func parseLine(line string) map[string]any {
	var row map[string]any
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		return nil
	}
	return row
}
