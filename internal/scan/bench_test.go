package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkScanToday 验收基线（对齐方案 §7 P3-新2）：今日行数≈10 万时单次扫描 ≤500ms。
func BenchmarkScanToday100k(b *testing.B) {
	home := b.TempDir()
	agent := filepath.Join(home, "sessions", "s1", "agents", "main")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		b.Fatal(err)
	}
	now := time.Now()
	start := now.Add(-time.Minute).UnixMilli()
	// 10 万行 usage.record（今日窗口内），分散到 100 个会话文件模拟真实分布。
	const files, perFile = 100, 1000
	for f := 0; f < files; f++ {
		p := filepath.Join(home, "sessions", fmt.Sprintf("s%03d", f), "agents", "main")
		if err := os.MkdirAll(p, 0o755); err != nil {
			b.Fatal(err)
		}
		fh, err := os.Create(filepath.Join(p, "wire.jsonl"))
		if err != nil {
			b.Fatal(err)
		}
		for i := 0; i < perFile; i++ {
			fmt.Fprintf(fh, `{"type":"usage.record","model":"kimi-k3","usage":{"inputOther":100,"output":20,"inputCacheRead":80,"inputCacheCreation":0},"usageScope":"turn","time":%d}`+"\n", start+int64(i))
		}
		fh.Close()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rpt, err := ScanToday(home, start, now.UnixMilli(), nil)
		if err != nil {
			b.Fatal(err)
		}
		if rpt.Requests != files*perFile {
			b.Fatalf("requests = %d; want %d", rpt.Requests, files*perFile)
		}
	}
}
