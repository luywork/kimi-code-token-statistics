package metrics

import (
	"encoding/json"
	"math"
)

// num 从 JSON 值提取非负有限 float64。
func num(v any) (float64, bool) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case int64:
		f = float64(n)
	case int:
		f = float64(n)
	case json.Number:
		parsed, err := n.Float64()
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

func sampleValues(samples []Sample) []float64 {
	vals := make([]float64, len(samples))
	for i, s := range samples {
		vals[i] = s.V
	}
	return vals
}

// lastSamples 返回末尾至多 n 个样本。
func lastSamples(samples []Sample, n int) []Sample {
	if len(samples) <= n {
		return samples
	}
	return samples[len(samples)-n:]
}

// applyThroughputRow 折入请求生命周期、TTFT 与 TPS（对齐 applyThroughputRow）。
func (s *State) applyThroughputRow(row map[string]any, agent string) {
	b := s.ensureAgent(agent)
	t := rowTime(row)
	typ, _ := row["type"].(string)

	// llm.request：标记请求开始（排除 compaction）。
	if typ == "llm.request" {
		kind, _ := row["kind"].(string)
		if kind != "compaction" && t > 0 && (b.LastRequestAt == 0 || t > b.LastRequestAt) {
			b.LastRequestAt = t
		}
		return
	}

	// 回合结束/压缩完成/取消：推进 step 结束锚点。
	activeCancel := typ == "turn.cancel" && !isQueued(row)
	if typ == "turn.ended" || typ == "full_compaction.complete" || activeCancel {
		if t > 0 && (b.LastStepEndAt == 0 || t > b.LastStepEndAt) {
			b.LastStepEndAt = t
		}
		return
	}

	if typ != "context.append_loop_event" {
		return
	}
	ev, ok := row["event"].(map[string]any)
	if !ok || ev["type"] != "step.end" {
		return
	}
	if t > 0 && (b.LastStepEndAt == 0 || t > b.LastStepEndAt) {
		b.LastStepEndAt = t
	}
	if ttft, ok := num(ev["llmFirstTokenLatencyMs"]); ok {
		b.LastTtftMs = ttft
	}

	usage, ok := ev["usage"].(map[string]any)
	if !ok {
		return
	}
	output, _ := num(usage["output"])
	streamMs, _ := num(ev["llmStreamDurationMs"])
	if streamMs == 0 {
		return
	}
	tps := output / (streamMs / 1000)
	if output <= 0 || streamMs < MinStreamMs || tps > MaxTPS || t <= 0 {
		return
	}
	if b.LastSampleAt != 0 && t-b.LastSampleAt > TpsTTLMS {
		b.Samples = nil
	}
	b.Samples = append(b.Samples, Sample{V: tps, T: t})
	if len(b.Samples) > MaxStoredSamples {
		b.Samples = b.Samples[len(b.Samples)-MaxStoredSamples:]
	}
	b.LastSampleAt = t
	if len(b.Samples) >= MinSamples {
		b.LastMedian = median(sampleValues(lastSamples(b.Samples, MaxSamples)))
		b.HasMedian = true
	}
}
