package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// todayWindow 返回 [本地今日 00:00, now] 的扫描窗口（毫秒）。
func todayWindow(t *testing.T) (int64, int64) {
	t.Helper()
	now := time.Now()
	y, mo, d := now.Date()
	start := time.Date(y, mo, d, 0, 0, 0, 0, now.Location()).UnixMilli()
	return start, now.UnixMilli()
}

// TestScanTodayFiltersToWindow 只统计窗口内（今日）记录，昨日记录不计入。
func TestScanTodayFiltersToWindow(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	mkdirAll(t, agent)

	startMs, endMs := todayWindow(t)
	yesterday := startMs - 3600_000      // 昨日
	today := startMs + (endMs-startMs)/2 // 今日（窗口内）
	lines := []string{
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":` + itoa(yesterday) + `}`,
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":10,"output":2,"inputCacheRead":8,"inputCacheCreation":0},"usageScope":"turn","time":` + itoa(today) + `}`,
	}
	writeFile(t, filepath.Join(agent, "wire.jsonl"), strings.Join(lines, "\n")+"\n")

	rpt, err := ScanToday(home, startMs, endMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.Requests != 1 {
		t.Fatalf("requests = %d; want 1", rpt.Requests)
	}
	if rpt.Tokens.InputOther != 10 || rpt.Tokens.Output != 2 ||
		rpt.Tokens.InputCacheRead != 8 || rpt.Tokens.Total != 20 {
		t.Fatalf("tokens = %+v; want only today record", rpt.Tokens)
	}
	if len(rpt.Models) != 1 {
		t.Fatalf("models = %d; want 1 (kimi-k3 today only)", len(rpt.Models))
	}
	if m := rpt.Models[0]; m.Model != "kimi-k3" || m.Requests != 1 ||
		m.Tokens.InputOther != 10 || m.Tokens.Output != 2 {
		t.Fatalf("model = %+v; want kimi-k3 1 request 10/2", m)
	}
	if len(rpt.Recent) != 1 {
		t.Fatalf("recent = %d; want 1 (today record)", len(rpt.Recent))
	}
	if r := rpt.Recent[0]; r.Model != "kimi-k3" || r.DurationMs != nil {
		t.Fatalf("recent[0] = %+v; want kimi-k3 with nil duration (no llm.request)", r)
	}
	if rpt.LimitsReached {
		t.Fatal("limits_reached = true; want false")
	}
}

// TestScanTodayRecentDurationFromPair llm.request 与紧邻 usage.record 配对求请求时长。
func TestScanTodayRecentDurationFromPair(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	mkdirAll(t, agent)

	startMs, endMs := todayWindow(t)
	today := startMs + (endMs-startMs)/2
	lines := []string{
		`{"type":"llm.request","time":` + itoa(today) + `}`,
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":` + itoa(today+5_000) + `}`,
	}
	writeFile(t, filepath.Join(agent, "wire.jsonl"), strings.Join(lines, "\n")+"\n")

	rpt, err := ScanToday(home, startMs, endMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rpt.Recent) != 1 || rpt.Recent[0].DurationMs == nil {
		t.Fatalf("recent = %+v; want 1 with duration from llm.request pair", rpt.Recent)
	}
	if *rpt.Recent[0].DurationMs != 5_000 {
		t.Fatalf("durationMs = %d; want 5000", *rpt.Recent[0].DurationMs)
	}
	if rpt.Requests != 1 {
		t.Fatalf("requests = %d; want 1", rpt.Requests)
	}
}

// TestScanTodayMtimeCoarseFilter mtime 早于窗口起点的文件整文件跳过（追加写语义）。
func TestScanTodayMtimeCoarseFilter(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	mkdirAll(t, agent)
	startMs, endMs := todayWindow(t)
	today := startMs + (endMs-startMs)/2
	path := filepath.Join(agent, "wire.jsonl")
	writeFile(t, path, `{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":`+itoa(today)+`}`+"\n")

	// 回拨文件 mtime 到昨日：即使内容带今日时间戳，也应按无今日写入整文件跳过。
	yesterday := time.UnixMilli(startMs).Add(-time.Hour)
	if err := os.Chtimes(path, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}

	rpt, err := ScanToday(home, startMs, endMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.Requests != 0 || rpt.ScannedFiles != 0 {
		t.Fatalf("requests = %d, scannedFiles = %d; want 0/0 (mtime 粗筛跳过)", rpt.Requests, rpt.ScannedFiles)
	}
}

// TestScanTodayZeroUsageSkipped grand_total()==0 的 usage.record 不计入。
func TestScanTodayZeroUsageSkipped(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	mkdirAll(t, agent)
	startMs, endMs := todayWindow(t)
	today := startMs + (endMs-startMs)/2
	writeFile(t, filepath.Join(agent, "wire.jsonl"),
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":0,"output":0,"inputCacheRead":0,"inputCacheCreation":0},"usageScope":"turn","time":`+itoa(today)+`}`+"\n"+
			`{"type":"context.append_loop_event","event":{"usage":{"inputOther":99,"output":99,"inputCacheRead":99,"inputCacheCreation":0}},"time":`+itoa(today)+`}`+"\n",
	)

	rpt, err := ScanToday(home, startMs, endMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.Requests != 0 || rpt.Tokens.Total != 0 {
		t.Fatalf("requests = %d, total = %d; want 0/0", rpt.Requests, rpt.Tokens.Total)
	}
}

// TestScanTodayLimitsReached 行数超过 today 收紧上限即停止并置 limits_reached。
func TestScanTodayLimitsReached(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	mkdirAll(t, agent)

	var sb strings.Builder
	// 写超过 maxTodayScannedLines 的短行（不含 usage.record，但仍计入 scannedLines）。
	for i := 0; i < maxTodayScannedLines+1; i++ {
		sb.WriteString(`{"type":"x"}` + "\n")
	}
	writeFile(t, filepath.Join(agent, "wire.jsonl"), sb.String())

	startMs, endMs := todayWindow(t)
	rpt, err := ScanToday(home, startMs, endMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rpt.LimitsReached {
		t.Fatal("limits_reached = false; want true")
	}
	if rpt.ScannedLines > maxTodayScannedLines {
		t.Fatalf("scannedLines = %d; want <= %d", rpt.ScannedLines, maxTodayScannedLines)
	}
}

// TestScanTodayNoSessions 无 sessions 目录返回 invalidHome 错误。
func TestScanTodayNoSessions(t *testing.T) {
	home := t.TempDir()
	startMs, _ := todayWindow(t)
	if _, err := ScanToday(home, startMs, startMs+1, nil); err == nil {
		t.Fatal("expected error for missing sessions dir")
	}
}

// TestScanTodayEmptyWindow 窗口内无 usage.record → 全部为 0（今日无调用的准确语义）。
func TestScanTodayEmptyWindow(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	mkdirAll(t, agent)
	startMs, endMs := todayWindow(t)
	// 只有窗口外的记录（昨日）
	yesterday := startMs - 3600_000
	_ = endMs // 空窗口断言用 endMs 传参即可，无需额外引用
	writeFile(t, filepath.Join(agent, "wire.jsonl"),
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":`+itoa(yesterday)+`}`+"\n")

	rpt, err := ScanToday(home, startMs, endMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.Requests != 0 || rpt.Tokens.Total != 0 {
		t.Fatalf("requests = %d, total = %d; want 0/0 (窗口外记录不计入)", rpt.Requests, rpt.Tokens.Total)
	}
}

// TestScanTodayWindowBoundary 窗口边界为闭区间：time==startMs 与 time==endMs 都计入。
func TestScanTodayWindowBoundary(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	mkdirAll(t, agent)
	startMs, _ := todayWindow(t)
	// 用自定义小窗口便于精确断言边界。
	s, e := startMs+1000, startMs+2000
	lines := []string{
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":1,"output":0,"inputCacheRead":0,"inputCacheCreation":0},"usageScope":"turn","time":` + itoa(s) + `}`,
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":2,"output":0,"inputCacheRead":0,"inputCacheCreation":0},"usageScope":"turn","time":` + itoa(e) + `}`,
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":4,"output":0,"inputCacheRead":0,"inputCacheCreation":0},"usageScope":"turn","time":` + itoa(s-1) + `}`,
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":8,"output":0,"inputCacheRead":0,"inputCacheCreation":0},"usageScope":"turn","time":` + itoa(e+1) + `}`,
	}
	writeFile(t, filepath.Join(agent, "wire.jsonl"), strings.Join(lines, "\n")+"\n")
	// 文件 mtime 必须在窗口内，否则 mtime 粗筛会整文件跳过。
	if err := os.Chtimes(filepath.Join(agent, "wire.jsonl"), time.UnixMilli(s), time.UnixMilli(s)); err != nil {
		t.Fatal(err)
	}

	rpt, err := ScanToday(home, s, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.Requests != 2 || rpt.Tokens.InputOther != 3 {
		t.Fatalf("requests = %d, inputOther = %d; want 2/3（闭区间只含边界两条）", rpt.Requests, rpt.Tokens.InputOther)
	}
}

// TestScanTodaySecondPrecisionTime 秒级时间戳（<1e10）归一化为毫秒后正确计入窗口。
func TestScanTodaySecondPrecisionTime(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	mkdirAll(t, agent)
	startMs, endMs := todayWindow(t)
	now := time.Now()
	mid := startMs + (endMs-startMs)/2
	// 秒级时间戳：mid 的秒
	sec := mid / 1000
	writeFile(t, filepath.Join(agent, "wire.jsonl"),
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":50,"output":5,"inputCacheRead":0,"inputCacheCreation":0},"usageScope":"turn","time":`+itoa(sec)+`}`+"\n")
	if err := os.Chtimes(filepath.Join(agent, "wire.jsonl"), now, now); err != nil {
		t.Fatal(err)
	}

	rpt, err := ScanToday(home, startMs, endMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.Requests != 1 || rpt.Tokens.InputOther != 50 {
		t.Fatalf("requests = %d, inputOther = %d; want 1/50（秒级时间戳应归一化）", rpt.Requests, rpt.Tokens.InputOther)
	}
}

// TestScanTodayStableRepeatedScan 同一数据多次扫描结果完全一致（今日档准确性/稳定性）。
func TestScanTodayStableRepeatedScan(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	mkdirAll(t, agent)
	startMs, endMs := todayWindow(t)
	now := time.Now()
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString(`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":10,"output":2,"inputCacheRead":8,"inputCacheCreation":0},"usageScope":"turn","time":` + itoa(startMs+(endMs-startMs)/2) + `}` + "\n")
	}
	writeFile(t, filepath.Join(agent, "wire.jsonl"), sb.String())
	if err := os.Chtimes(filepath.Join(agent, "wire.jsonl"), now, now); err != nil {
		t.Fatal(err)
	}

	var want uint64 = 0
	first := true
	for i := 0; i < 3; i++ {
		rpt, err := ScanToday(home, startMs, endMs, nil)
		if err != nil {
			t.Fatal(err)
		}
		if first {
			want = rpt.Tokens.Total
			first = false
		} else if rpt.Tokens.Total != want {
			t.Fatalf("扫描 %d: total = %d; want 稳定 %d", i, rpt.Tokens.Total, want)
		}
		if rpt.Requests != 100 {
			t.Fatalf("扫描 %d: requests = %d; want 100", i, rpt.Requests)
		}
	}
	if want != 2000 {
		t.Fatalf("total = %d; want 2000 (100×20)", want)
	}
}

// TestScanTodayMalformedCounts 记录计数与全量 Scan 一致：格式错误行计入 malformed。
func TestScanTodayMalformedCounts(t *testing.T) {
	home := t.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	mkdirAll(t, agent)
	startMs, endMs := todayWindow(t)
	today := startMs + (endMs-startMs)/2
	writeFile(t, filepath.Join(agent, "wire.jsonl"),
		`{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":1,"output":1,"inputCacheRead":0,"inputCacheCreation":0},"usageScope":"turn","time":`+itoa(today)+`}`+"\n"+
			`{"type":"usage.record",broken`+"\n",
	)

	rpt, err := ScanToday(home, startMs, endMs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rpt.Requests != 1 {
		t.Fatalf("requests = %d; want 1", rpt.Requests)
	}
	if rpt.MalformedRecords != 1 {
		t.Fatalf("malformed = %d; want 1", rpt.MalformedRecords)
	}
}
