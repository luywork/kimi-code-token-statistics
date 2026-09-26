package metrics

// applyTurnRow 折入用户回合时钟（对齐 applyTurnRow）。
// prompt 锚点仅 main；turn 结束标记逐 agent 记录。
func (s *State) applyTurnRow(row map[string]any, agent string) {
	b := s.ensureAgent(agent)
	t := rowTime(row)
	if t <= 0 {
		return
	}
	typ, _ := row["type"].(string)

	if agent == "main" && typ == "turn.prompt" {
		if b.LastTurnPromptAt == 0 || t > b.LastTurnPromptAt {
			b.LastTurnPromptAt = t
		}
		return
	}
	if typ == "turn.ended" || (typ == "turn.cancel" && !isQueued(row)) {
		if b.LastTurnEndAt == 0 || t > b.LastTurnEndAt {
			b.LastTurnEndAt = t
		}
		return
	}
	if typ == "context.append_loop_event" {
		ev, ok := row["event"].(map[string]any)
		if ok && ev["type"] == "step.end" && ev["finishReason"] == "end_turn" {
			if b.LastTurnEndAt == 0 || t > b.LastTurnEndAt {
				b.LastTurnEndAt = t
			}
		}
	}
}

func isQueued(row map[string]any) bool {
	target, _ := row["target"].(string)
	return target == "queued"
}

// TurnActive 返回 main 回合是否进行中（用于 gen Ns 计时）。
func (s *State) TurnActive() bool {
	main := s.Agents["main"]
	if main == nil || main.LastTurnPromptAt == 0 {
		return false
	}
	return main.LastTurnEndAt == 0 || main.LastTurnPromptAt > main.LastTurnEndAt
}
