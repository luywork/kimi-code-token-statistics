package metrics

// Summary 一次汇总输出（对齐 summarizeMetrics 的公共字段）。
type Summary struct {
	TPS           float64
	TPSStale      bool
	HasTPS        bool
	TTFTMs        float64
	HasTTFT       bool
	TPSTotal      float64
	TPSAgents     int
	ActiveAgents  int
	MainActive    bool
	MainSpeed     bool
	TurnStartedAt int64
	ModelAlias    string
	SwarmMode     bool
	CacheRate     float64
	HasCache      bool
}

// Summarize 在给定时刻聚合舰队速度与缓存指标。
func (s *State) Summarize(now int64) Summary {
	var (
		activeSpeeds []float64
		activeTtfts  []float64
		activeAgents int
		mainActive   bool
		mainSpeed    bool
		soleActive   *Agent
		soleFresh    []Sample
	)

	for name, agent := range s.Agents {
		if agent == nil {
			continue
		}
		fresh := agent.Samples[:0:0]
		for _, smp := range agent.Samples {
			if smp.T >= now-SampleWindowMS {
				fresh = append(fresh, smp)
			}
		}
		// 过期样本就地剔除（对齐源码直接改写 agent.samples）
		agent.Samples = fresh

		speed := 0.0
		hasSpeed := false
		if len(fresh) > 0 {
			vals := sampleValues(tail(fresh, MaxSamples))
			speed = median(vals)
			hasSpeed = true
		}
		generating := agent.LastRequestAt != 0 &&
			now-agent.LastRequestAt < SampleWindowMS &&
			(agent.LastStepEndAt == 0 || agent.LastRequestAt > agent.LastStepEndAt)
		recent := len(fresh) > 0 && fresh[len(fresh)-1].T >= now-ActiveWindowMS

		settled := name != "main" &&
			agent.LastTurnEndAt != 0 &&
			(len(fresh) == 0 || agent.LastTurnEndAt >= fresh[len(fresh)-1].T) &&
			(agent.LastRequestAt == 0 || agent.LastTurnEndAt >= agent.LastRequestAt)

		if !generating && (!recent || settled) {
			continue
		}
		activeAgents++
		soleActive = agent
		soleFresh = fresh
		if name == "main" {
			mainActive = true
		}
		if hasSpeed {
			activeSpeeds = append(activeSpeeds, speed)
			if name == "main" {
				mainSpeed = true
			}
		}
		if agent.LastTtftMs != 0 && agent.LastSampleAt >= now-SampleWindowMS {
			activeTtfts = append(activeTtfts, agent.LastTtftMs)
		}
	}

	var (
		tps       float64
		hasTPS    bool
		tpsStale  bool
		tpsTotal  float64
		tpsAgents int
		ttftMs    float64
		hasTTFT   bool
	)

	switch {
	case activeAgents >= 2:
		if len(activeSpeeds) > 0 {
			tpsTotal = sum(activeSpeeds)
			tpsAgents = len(activeSpeeds)
			tps = tpsTotal / float64(tpsAgents)
			hasTPS = true
		}
		if len(activeTtfts) > 0 {
			ttftMs = median(activeTtfts)
			hasTTFT = true
		}
	case activeAgents == 1:
		var windowMedian float64
		hasWindow := false
		if len(soleFresh) >= MinSamples {
			windowMedian = median(sampleValues(tail(soleFresh, MaxSamples)))
			hasWindow = true
		}
		var newest int64
		if len(soleFresh) > 0 {
			newest = soleFresh[len(soleFresh)-1].T
		}
		if hasWindow && newest != 0 && now-newest <= TpsTTLMS {
			tps = windowMedian
			hasTPS = true
		} else if soleActive.HasMedian {
			tps = soleActive.LastMedian
			tpsStale = true
			hasTPS = true
		}
		if hasTPS {
			tpsTotal = tps
			tpsAgents = 1
		}
		if soleActive.LastTtftMs != 0 {
			ttftMs = soleActive.LastTtftMs
			hasTTFT = true
		}
	default:
		if main := s.Agents["main"]; main != nil && main.HasMedian {
			tps = main.LastMedian
			tpsStale = true
			hasTPS = true
		}
		if main := s.Agents["main"]; main != nil && main.LastTtftMs != 0 {
			ttftMs = main.LastTtftMs
			hasTTFT = true
		}
	}

	var turnStartedAt int64
	if main := s.Agents["main"]; main != nil {
		if main.LastTurnPromptAt != 0 &&
			(main.LastTurnEndAt == 0 || main.LastTurnPromptAt > main.LastTurnEndAt) {
			turnStartedAt = main.LastTurnPromptAt
		}
	}

	rate, hasCache := s.CacheHitRate()

	return Summary{
		TPS:           tps,
		TPSStale:      tpsStale,
		HasTPS:        hasTPS,
		TTFTMs:        ttftMs,
		HasTTFT:       hasTTFT,
		TPSTotal:      tpsTotal,
		TPSAgents:     tpsAgents,
		ActiveAgents:  activeAgents,
		MainActive:    mainActive,
		MainSpeed:     mainSpeed,
		TurnStartedAt: turnStartedAt,
		ModelAlias:    s.ModelAlias,
		SwarmMode:     s.SwarmMode,
		CacheRate:     rate,
		HasCache:      hasCache,
	}
}

func tail(samples []Sample, n int) []Sample {
	if len(samples) <= n {
		return samples
	}
	return samples[len(samples)-n:]
}

func sum(values []float64) float64 {
	var total float64
	for _, v := range values {
		total += v
	}
	return total
}
