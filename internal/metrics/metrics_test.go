package metrics

import "testing"

// stepEnd 构造一个 step.end 事件。
func stepEnd(t0 int64, output, streamMs, ttft float64, finish string, usage map[string]any) map[string]any {
	return map[string]any{
		"type": "context.append_loop_event",
		"time": float64(t0),
		"event": map[string]any{
			"type":                   "step.end",
			"finishReason":           finish,
			"llmFirstTokenLatencyMs": ttft,
			"llmStreamDurationMs":    streamMs,
			"usage":                  usage,
		},
	}
}

func TestTPSMedian(t *testing.T) {
	s := NewState()
	// 三个 step.end，stream=1000ms，output=10/20/30 -> tps 10/20/30，中位数 20。
	for i, out := range []float64{10, 20, 30} {
		s.ApplyWireRow(stepEnd(1000+int64(i*1000), out, 1000, 500, "tool_use",
			map[string]any{"inputOther": 100, "inputCacheRead": 0, "inputCacheCreation": 0, "output": out}), "main")
	}
	sum := s.Summarize(4000)
	if !sum.HasTPS {
		t.Fatal("expected TPS")
	}
	if sum.TPS != 20 {
		t.Fatalf("TPS = %v, want 20", sum.TPS)
	}
	if sum.TTFTMs != 500 {
		t.Fatalf("TTFT = %v, want 500", sum.TTFTMs)
	}
}

func TestTPSFilterInvalid(t *testing.T) {
	s := NewState()
	// stream 太短（<250ms）应被丢弃。
	s.ApplyWireRow(stepEnd(1000, 10, 100, 500, "tool_use",
		map[string]any{"output": 10}), "main")
	// output=0 应被丢弃。
	s.ApplyWireRow(stepEnd(2000, 0, 1000, 500, "tool_use",
		map[string]any{"output": 0}), "main")
	// 有效样本不足 3 个，不应有 TPS。
	sum := s.Summarize(3000)
	if sum.HasTPS {
		t.Fatal("should not have TPS with <3 valid samples")
	}
}

func TestCacheAccumulation(t *testing.T) {
	s := NewState()
	s.ApplyWireRow(stepEnd(1000, 10, 1000, 500, "tool_use",
		map[string]any{"inputOther": 100, "inputCacheRead": 50, "inputCacheCreation": 0, "output": 10}), "main")
	s.ApplyWireRow(stepEnd(2000, 10, 1000, 500, "tool_use",
		map[string]any{"inputOther": 100, "inputCacheRead": 50, "inputCacheCreation": 0, "output": 10}), "main")

	rate, ok := s.CacheHitRate()
	if !ok {
		t.Fatal("expected cache rate")
	}
	// read=100, input=300 -> 33.3%
	if rate != 100.0/300.0 {
		t.Fatalf("rate = %v, want %v", rate, 100.0/300.0)
	}
}

func TestCacheIgnoresUsageRecord(t *testing.T) {
	s := NewState()
	// usage.record 不应污染 cache（对齐 kimi-code-hud 忽略它的行为）。
	s.ApplyWireRow(map[string]any{"type": "usage.record", "model": "kimi-code/k3",
		"usage": map[string]any{"inputOther": 999, "inputCacheRead": 999, "inputCacheCreation": 999}}, "main")
	if s.Cache != nil && (s.Cache.InputTokens != 0 || s.Cache.ReadTokens != 0) {
		t.Fatal("usage.record must not touch cache")
	}
}

func TestFleetAggregation(t *testing.T) {
	s := NewState()
	// main + 3 个 subagent 都在生成，各 3 个样本。
	for _, agent := range []string{"main", "agent-0", "agent-1", "agent-2"} {
		for i, out := range []float64{10, 20, 30} {
			s.ApplyWireRow(stepEnd(1000+int64(i*1000), out, 1000, 500, "tool_use",
				map[string]any{"output": out}), agent)
		}
	}
	sum := s.Summarize(4000)
	if sum.ActiveAgents < 2 {
		t.Fatalf("expected multi-agent fleet, ActiveAgents = %d", sum.ActiveAgents)
	}
	// 每 agent 中位数 20，总速 80，平均 20。
	if sum.TPSTotal != 80 || sum.TPS != 20 || sum.TPSAgents != 4 {
		t.Fatalf("fleet = total %v agents %d avg %v", sum.TPSTotal, sum.TPSAgents, sum.TPS)
	}
}

func TestTurnClock(t *testing.T) {
	s := NewState()
	s.ApplyWireRow(map[string]any{"type": "turn.prompt", "time": float64(5000)}, "main")
	sum := s.Summarize(7000)
	if sum.TurnStartedAt != 5000 {
		t.Fatalf("TurnStartedAt = %d", sum.TurnStartedAt)
	}
	// turn.ended 结束回合。
	s.ApplyWireRow(map[string]any{"type": "turn.ended", "time": float64(6000)}, "main")
	sum = s.Summarize(7000)
	if sum.TurnStartedAt != 0 {
		t.Fatalf("turn should be ended, TurnStartedAt = %d", sum.TurnStartedAt)
	}
}

func TestModelAliasResetFleet(t *testing.T) {
	s := NewState()
	for i, out := range []float64{10, 20, 30} {
		s.ApplyWireRow(stepEnd(1000+int64(i*1000), out, 1000, 500, "tool_use",
			map[string]any{"output": out}), "main")
	}
	if sum := s.Summarize(4000); !sum.HasTPS {
		t.Fatal("expected TPS before model switch")
	}
	// 模型切换应重置速度窗口。
	s.ApplyWireRow(map[string]any{"type": "config.update", "modelAlias": "kimi-code/k3-256k"}, "main")
	if sum := s.Summarize(5000); sum.HasTPS {
		t.Fatal("model switch must reset fleet windows")
	}
	if s.ModelAlias != "kimi-code/k3-256k" {
		t.Fatalf("ModelAlias = %q", s.ModelAlias)
	}
}

func TestSwarmModeFold(t *testing.T) {
	s := NewState()
	s.ApplyWireRow(map[string]any{"type": "swarm_mode.enter"}, "main")
	if !s.SwarmMode {
		t.Fatal("swarm_mode.enter should set SwarmMode")
	}
	if sum := s.Summarize(1000); !sum.SwarmMode {
		t.Fatal("Summary should carry SwarmMode")
	}
	// 非 main agent 的 swarm 事件不应折叠。
	s2 := NewState()
	s2.ApplyWireRow(map[string]any{"type": "swarm_mode.enter"}, "agent-0")
	if s2.SwarmMode {
		t.Fatal("subagent swarm event must be ignored")
	}
	s.ApplyWireRow(map[string]any{"type": "swarm_mode.exit"}, "main")
	if s.SwarmMode {
		t.Fatal("swarm_mode.exit should clear SwarmMode")
	}
}

func TestSwarmSoloFleetStyle(t *testing.T) {
	// swarm 缩到只剩 1 个子 agent 时，TPS 存在仍按舰队 multi 样式（对齐参考 multi 判定）。
	s := NewState()
	s.SwarmMode = true
	for i, out := range []float64{10, 20, 30} {
		s.ApplyWireRow(stepEnd(1000+int64(i*1000), out, 1000, 500, "tool_use",
			map[string]any{"output": out}), "main")
	}
	// 只有 main 活跃：activeAgents=1、tpsAgents=1，无 live subagent → 不是 multi。
	sum := s.Summarize(4000)
	if sum.SwarmMode != true {
		t.Fatal("swarm mode should remain")
	}
	// 加一个活跃子 agent。
	for i, out := range []float64{10, 20, 30} {
		s.ApplyWireRow(stepEnd(2000+int64(i*1000), out, 1000, 500, "tool_use",
			map[string]any{"output": out}), "agent-0")
	}
	sum = s.Summarize(6000)
	if sum.ActiveAgents < 2 {
		t.Fatalf("ActiveAgents = %d, want >=2", sum.ActiveAgents)
	}
}
