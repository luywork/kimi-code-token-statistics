package metrics

import (
	"sort"
)

// median 返回中位数；空切片返回 0。
func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// rowTime 提取行的毫秒时间戳；非法返回 0。
func rowTime(row map[string]any) int64 {
	switch t := row["time"].(type) {
	case float64:
		if t >= 0 {
			return int64(t)
		}
	case int64:
		if t >= 0 {
			return t
		}
	}
	return 0
}
