package metrics

// applyCacheRow 折入会话累计的 cache 计数（仅 main agent，对齐 applyCacheWireRow）。
// 只消费 step.end 的完整 usage，忽略 usage.record 以免与 step.end 重复计数。
func (s *State) applyCacheRow(row map[string]any) {
	if row["type"] != "context.append_loop_event" {
		return
	}
	ev, ok := row["event"].(map[string]any)
	if !ok || ev["type"] != "step.end" {
		return
	}
	usage, ok := ev["usage"].(map[string]any)
	if !ok {
		return
	}
	inputOther, ok1 := num(usage["inputOther"])
	inputCacheRead, ok2 := num(usage["inputCacheRead"])
	inputCacheCreation, ok3 := num(usage["inputCacheCreation"])
	if !ok1 || !ok2 || !ok3 {
		return
	}
	s.ensureCache()
	s.Cache.ReadTokens += int64(inputCacheRead)
	s.Cache.InputTokens += int64(inputOther + inputCacheRead + inputCacheCreation)
}

// CacheHitRate 返回 token 加权的 cache 命中率（0~1）；无有效数据返回 false。
func (s *State) CacheHitRate() (float64, bool) {
	if s.Cache == nil || s.Cache.InputTokens <= 0 {
		return 0, false
	}
	rate := float64(s.Cache.ReadTokens) / float64(s.Cache.InputTokens)
	if rate > 1 {
		rate = 1
	}
	return rate, true
}
