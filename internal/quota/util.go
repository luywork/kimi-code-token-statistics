package quota

import (
	"encoding/json"
	"os"
	"strconv"
)

// writeAtomicJSON 原子写 JSON 文件（tmp + rename）。
func writeAtomicJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadDiskCache 从磁盘读取上次的配额缓存。
func (c *Client) loadDiskCache() *Quota {
	data, err := os.ReadFile(c.cachePath)
	if err != nil {
		return nil
	}
	var q Quota
	if err := json.Unmarshal(data, &q); err != nil {
		return nil
	}
	if q.FetchedAt.IsZero() {
		return nil
	}
	return &q
}

func itoa(n int) string { return strconv.Itoa(n) }
