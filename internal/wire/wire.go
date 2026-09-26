// Package wire 实现 wire.jsonl 的增量读取（对齐 kimi-code-hud 的 wire-reader.mjs）。
package wire

import (
	"bytes"
	"encoding/base64"
	"os"
)

// MaxLineBytes 单行上限，超限整行丢弃（防异常大行）。
const MaxLineBytes = 1 << 20

// Reader 单文件增量读取器；断点字段可 JSON 持久化。
type Reader struct {
	Offset     int64  `json:"offset"`
	Pending    []byte `json:"pending,omitempty"`
	TailMarker string `json:"tailMarker,omitempty"`
	Discarding bool   `json:"discarding,omitempty"`
}

// NewReader 返回从文件头开始的读取器。
func NewReader() *Reader { return &Reader{} }

// Result 一次读取的结果。
type Result struct {
	Lines     []string
	BytesRead int64
	Replaced  bool // 检测到文件被轮转/重写
	Complete  bool // 已读到文件尾（无半行残留）
}

// Stat 返回文件大小。
func Stat(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// Read 读取 [offset, offset+maxBytes) 的完整行。
// fileSize 由调用方传入，避免重复 stat。
func (r *Reader) Read(path string, fileSize int64, maxBytes int64) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	res := &Result{}

	// 轮转检测：offset 超界，或 offset 处前后 32 字节指纹不匹配。
	replaced := r.Offset > fileSize
	if !replaced && r.TailMarker != "" && r.Offset > 0 {
		if cur, ok := markerAt(f, r.Offset); ok && cur != r.TailMarker {
			replaced = true
		}
	}
	if replaced {
		r.Offset = 0
		r.Pending = nil
		r.Discarding = false
		res.Replaced = true
	}

	end := r.Offset + maxBytes
	if end > fileSize {
		end = fileSize
	}
	if end > r.Offset {
		buf := make([]byte, end-r.Offset)
		n, err := f.ReadAt(buf, r.Offset)
		if err != nil && n == 0 {
			return res, nil
		}
		chunk := buf[:n]
		r.Offset += int64(n)
		res.BytesRead = int64(n)
		r.scan(chunk, &res.Lines)
	}

	// 更新指纹供下次轮转检测。
	if r.Offset > 0 {
		if m, ok := markerAt(f, r.Offset); ok {
			r.TailMarker = m
		}
	}
	res.Complete = r.Offset >= fileSize && len(r.Pending) == 0 && !r.Discarding
	return res, nil
}

// scan 将新增字节按行切分；未换行的尾部保留在 Pending。
func (r *Reader) scan(chunk []byte, lines *[]string) {
	pending := append(r.Pending, chunk...)
	r.Pending = nil
	for {
		idx := bytes.IndexByte(pending, '\n')
		if idx < 0 {
			break
		}
		line := pending[:idx]
		pending = pending[idx+1:]
		if r.Discarding {
			r.Discarding = false
			continue
		}
		if len(line) > MaxLineBytes {
			// 该行已闭合（找到换行），直接丢弃即可，不设跨块标记。
			continue
		}
		*lines = append(*lines, string(line))
	}
	if len(pending) > MaxLineBytes {
		r.Discarding = true
		pending = nil
	}
	r.Pending = pending
}

// markerAt 计算文件 [max(0, offset-32), offset) 的 base64 指纹。
func markerAt(f *os.File, offset int64) (string, bool) {
	start := offset - 32
	if start < 0 {
		start = 0
	}
	buf := make([]byte, offset-start)
	n, err := f.ReadAt(buf, start)
	if err != nil && n == 0 {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(buf[:n]), true
}
