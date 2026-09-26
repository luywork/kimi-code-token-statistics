// Package metrics 实现 TPS/TTFT/Cache 状态机与舰队汇总（对齐 kimi-code-hud 的 metrics* 模块）。
package metrics

// StateVersion 持久化状态版本；不匹配时重置。
const StateVersion = 1

// 采样与窗口常量（对齐 metrics-constants.mjs）。
const (
	MaxSamples       = 5
	MinSamples       = 3
	MinStreamMs      = 250
	MaxTPS           = 1000
	TpsTTLMS         = 2 * 60 * 1000
	SampleWindowMS   = 10 * 60 * 1000
	ActiveWindowMS   = TpsTTLMS
	MaxStoredSamples = 20
)

// Sample 一条 TPS 采样。
type Sample struct {
	V float64 `json:"v"` // tokens/s
	T int64   `json:"t"` // 毫秒时间戳
}

// Agent 单 agent 的指标与 wire 断点状态（对齐 emptyAgent/normAgent）。
type Agent struct {
	Samples          []Sample `json:"samples,omitempty"`
	LastMedian       float64  `json:"lastMedian"`
	HasMedian        bool     `json:"hasMedian,omitempty"`
	LastTtftMs       float64  `json:"lastTtftMs"`
	LastSampleAt     int64    `json:"lastSampleAt"`
	LastRequestAt    int64    `json:"lastRequestAt"`
	LastStepEndAt    int64    `json:"lastStepEndAt"`
	LastTurnPromptAt int64    `json:"lastTurnPromptAt"`
	LastTurnEndAt    int64    `json:"lastTurnEndAt"`

	// wire 断点（session 包读写，重启续读）。
	ReaderOffset int64  `json:"readerOffset"`
	Pending      []byte `json:"pending,omitempty"`
	TailMarker   string `json:"tailMarker,omitempty"`
	Discarding   bool   `json:"discarding,omitempty"`
}

// Cache 会话累计缓存计数。
type Cache struct {
	ReadTokens  int64 `json:"readTokens"`
	InputTokens int64 `json:"inputTokens"`
}

// State 指标状态机（可 JSON 持久化）。
type State struct {
	Version       int               `json:"version"`
	Agents        map[string]*Agent `json:"agents"`
	ModelAlias    string            `json:"modelAlias,omitempty"`
	ThinkingLevel string            `json:"thinkingLevel,omitempty"`
	SwarmMode     bool              `json:"swarmMode,omitempty"`
	Cache         *Cache            `json:"cache,omitempty"`
}

// NewState 返回空状态。
func NewState() *State {
	return &State{Version: StateVersion, Agents: map[string]*Agent{}}
}

// ensureAgent 取 agent 桶，不存在则创建。
func (s *State) ensureAgent(agent string) *Agent {
	if s.Agents == nil {
		s.Agents = map[string]*Agent{}
	}
	b := s.Agents[agent]
	if b == nil {
		b = &Agent{}
		s.Agents[agent] = b
	}
	return b
}

// ensureCache 惰性创建缓存计数器。
func (s *State) ensureCache() {
	if s.Cache == nil {
		s.Cache = &Cache{}
	}
}

// ResetAgentReader 清空某 agent 的 wire 断点（文件轮转后）。
func (s *State) ResetAgentReader(agent string) {
	if b, ok := s.Agents[agent]; ok {
		b.ReaderOffset = 0
		b.Pending = nil
		b.TailMarker = ""
		b.Discarding = false
	}
}

// ApplyWireRow 折入一行事件（对齐 processWireRow 的分派顺序）。
func (s *State) ApplyWireRow(row map[string]any, agent string) {
	if row == nil {
		return
	}
	s.ensureAgent(agent)
	if agent == "main" {
		s.applyCacheRow(row)
	}
	s.applyTurnRow(row, agent)
	s.applyThroughputRow(row, agent)
	s.applySessionMetaRow(row, agent)
}

// Merge 将 reloaded 状态的指标字段合并进当前状态（保留现读取断点）。
// 当前未使用，保留供未来多会话切换。
