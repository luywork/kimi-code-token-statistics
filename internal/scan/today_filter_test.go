package scan

import (
	"path/filepath"
	"testing"
)

// scan_today_provider_filter 只聚合所选厂家的记录（与全量 Scan 同一 resolvedFilter 口径）。
func TestScanTodayProviderFilter(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "config.toml"), `
[models."kimi-code/k3"]
provider = "managed:kimi-code"
model = "kimi-k3"

[models."custom/glm"]
provider = "custom"
model = "glm-5.3"
`)
	start := int64(1_784_500_000_000)
	writeRecords(t, home, "session-1", []rec{
		{"kimi-code/k3", start, "turn", [4]uint64{100, 20, 80, 0}},
		{"custom/glm", start + 1, "turn", [4]uint64{70, 5, 0, 0}},
	})

	report, err := ScanToday(home, start, start+1000, &ScanFilter{Providers: []string{"managed:kimi-code"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 || len(report.Models) != 1 || report.Models[0].Model != "kimi-k3" {
		t.Fatalf("filtered: req=%d models=%+v", report.Requests, report.Models)
	}
	if report.Tokens.InputOther != 100 || report.Tokens.Total != 200 {
		t.Fatalf("tokens = %+v", report.Tokens)
	}
	// 维度选项仍为筛选前全量口径（前端下拉不因筛选缩水）。
	found := false
	for _, v := range report.Dimensions.Providers {
		if v.Value == "custom" {
			found = true
		}
	}
	if !found {
		t.Fatalf("providers options should be pre-filter, got %+v", report.Dimensions.Providers)
	}

	// 无筛选 = 全量。
	report, err = ScanToday(home, start, start+1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 2 {
		t.Fatalf("unfiltered requests = %d; want 2", report.Requests)
	}
}
