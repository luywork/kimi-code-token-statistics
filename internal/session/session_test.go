package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kimi-hud/internal/state"
)

// stepEvent 构造一条可产生 TPS 样本的 step.end 事件行。
func stepEvent(ts int64) string {
	row := map[string]any{
		"type": "context.append_loop_event",
		"time": ts,
		"event": map[string]any{
			"type":                   "step.end",
			"llmStreamDurationMs":    float64(1000),
			"llmFirstTokenLatencyMs": float64(200),
			"usage": map[string]any{
				"output":             float64(100),
				"inputOther":         float64(10),
				"inputCacheRead":     float64(20),
				"inputCacheCreation": float64(0),
			},
		},
	}
	b, _ := json.Marshal(row)
	return string(b)
}

func writeWire(t *testing.T, path, line string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// TestDynamicSubAgentDiscovered 验证会话运行期间动态新建的子 agent wire
// 是否会被纳入增量读取（对齐参考实现 enumerateWires 每帧重扫 agents/）。
func TestDynamicSubAgentDiscovered(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	sess := filepath.Join(root, "wd_demo", "session_1")
	mainWire := filepath.Join(sess, "agents", "main", "wire.jsonl")
	now := time.Now().UnixMilli()

	writeWire(t, mainWire, stepEvent(now))
	store := state.NewStore(filepath.Join(t.TempDir(), "state"))
	m := New(root, store)
	m.Relocate()
	m.Poll(time.Now())

	if ag := m.state.Agents["main"]; ag == nil || len(ag.Samples) == 0 {
		t.Fatalf("main 应被读取并产生样本, agents=%v", m.state.Agents)
	}

	// 模拟会话进行中 spawn 子 agent（wire 在运行期新建）。
	subWire := filepath.Join(sess, "agents", "agent-1", "wire.jsonl")
	writeWire(t, subWire, stepEvent(now+1000))

	// 触发一次 Relocate（30s 节奏）后再 Poll。
	m.Relocate()
	m.Poll(time.Now())

	ag := m.state.Agents["agent-1"]
	if ag == nil || len(ag.Samples) == 0 {
		t.Fatalf("动态新建的子 agent-1 未被纳入统计（wires=%v）", m.wires)
	}
}
