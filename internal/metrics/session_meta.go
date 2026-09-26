package metrics

// applySessionMetaRow 折入模型/思考元数据（仅 main，对齐 applySessionMetaRow）。
// 模型变化时重置舰队速度窗口——速度跨模型不可比。
func (s *State) applySessionMetaRow(row map[string]any, agent string) {
	if agent != "main" {
		return
	}
	typ, _ := row["type"].(string)
	switch typ {
	case "config.update", "profile.bind":
		s.adoptModelAlias(str(row["modelAlias"]))
		if lvl := str(row["thinkingEffort"]); lvl != "" {
			s.ThinkingLevel = lvl
		} else if lvl = str(row["thinkingLevel"]); lvl != "" {
			s.ThinkingLevel = lvl
		}
	case "llm.request":
		s.adoptModelAlias(str(row["modelAlias"]))
		if lvl := str(row["thinkingEffort"]); lvl != "" {
			s.ThinkingLevel = lvl
		}
	case "swarm_mode.enter", "swarm_mode.exit":
		s.SwarmMode = typ == "swarm_mode.enter"
	}
}

// adoptModelAlias 对齐 adoptModelAlias：模型变更时重置所有 agent 的速度窗口。
func (s *State) adoptModelAlias(alias string) {
	if alias == "" || alias == s.ModelAlias {
		return
	}
	hasSamples := false
	for _, b := range s.Agents {
		if len(b.Samples) > 0 || b.LastTtftMs != 0 {
			hasSamples = true
			break
		}
	}
	if s.ModelAlias != "" || hasSamples {
		s.ResetFleetWindows()
	}
	s.ModelAlias = alias
}

// ResetFleetWindows 清空全部 agent 的速度窗口（对齐 resetFleetWindows）。
func (s *State) ResetFleetWindows() {
	for _, b := range s.Agents {
		b.Samples = nil
		b.LastTtftMs = 0
		b.LastSampleAt = 0
		b.LastMedian = 0
		b.HasMedian = false
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
