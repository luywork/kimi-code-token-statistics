package wire

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalRead(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wire.jsonl")

	writeFile(t, p, "line1\nline2\n")
	size, _ := Stat(p)

	r := NewReader()
	res, err := r.Read(p, size, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 2 || res.Lines[0] != "line1" || res.Lines[1] != "line2" {
		t.Fatalf("lines = %v", res.Lines)
	}
	if !res.Complete {
		t.Fatal("expected complete")
	}
	if res.BytesRead != int64(len("line1\nline2\n")) {
		t.Fatalf("bytesRead = %d", res.BytesRead)
	}

	// 追加新行，再读增量。
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("line3\n")
	f.Close()
	size, _ = Stat(p)

	res, err = r.Read(p, size, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 1 || res.Lines[0] != "line3" {
		t.Fatalf("incremental lines = %v", res.Lines)
	}
}

func TestSplitHalfLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wire.jsonl")

	// 写入不带换行的半行，再写下一段（模拟写入中途读取）。
	writeFile(t, p, "hel")
	size, _ := Stat(p)
	r := NewReader()
	res, _ := r.Read(p, size, 1<<20)
	if len(res.Lines) != 0 {
		t.Fatalf("expected no lines, got %v", res.Lines)
	}
	if len(r.Pending) != 3 {
		t.Fatalf("pending = %q", r.Pending)
	}
	if res.Complete {
		t.Fatal("half line should not be complete")
	}

	// 追加完整行。
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("lo world\nnext\n")
	f.Close()
	size, _ = Stat(p)
	res, _ = r.Read(p, size, 1<<20)
	if len(res.Lines) != 2 || res.Lines[0] != "hello world" || res.Lines[1] != "next" {
		t.Fatalf("lines = %v", res.Lines)
	}
	if len(r.Pending) != 0 {
		t.Fatalf("pending should be empty, got %q", r.Pending)
	}
}

func TestUtf8Boundary(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wire.jsonl")

	// "生成" = e7 94 9f e6 88 90；在中间切一半。
	b := []byte("a\xe7\x94")
	os.WriteFile(p, b, 0o644)
	size, _ := Stat(p)
	r := NewReader()
	res, _ := r.Read(p, size, 1<<20)
	if len(res.Lines) != 0 {
		t.Fatalf("unexpected lines %v", res.Lines)
	}

	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte("\x9f\xe6\x88\x90b\n"))
	f.Close()
	size, _ = Stat(p)
	res, _ = r.Read(p, size, 1<<20)
	if len(res.Lines) != 1 || res.Lines[0] != "a生成b" {
		t.Fatalf("utf8 join failed: %q", res.Lines)
	}
}

func TestRotationDetected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wire.jsonl")

	writeFile(t, p, "AAAA\nBBBB\nCCCC\n")
	size, _ := Stat(p)
	r := NewReader()
	res, _ := r.Read(p, size, 1<<20)
	if len(res.Lines) != 3 {
		t.Fatalf("lines = %v", res.Lines)
	}
	// 记录 offset 处指纹。

	// 文件被截断重写为完全不同的内容（同 size）。
	writeFile(t, p, "XXXX\nYYYY\nZZZZ\n")
	size, _ = Stat(p)
	res, _ = r.Read(p, size, 1<<20)
	if !res.Replaced {
		t.Fatal("rotation not detected")
	}
	if len(res.Lines) != 3 || res.Lines[0] != "XXXX" {
		t.Fatalf("after rotation lines = %v", res.Lines)
	}
}

func TestOversizedLineDropped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wire.jsonl")

	big := make([]byte, MaxLineBytes+100)
	for i := range big {
		big[i] = 'x'
	}
	os.WriteFile(p, append(append(big, '\n'), []byte("ok\n")...), 0o644)
	size, _ := Stat(p)
	r := NewReader()
	res, _ := r.Read(p, size, 1<<22) // 一次读入全部
	if len(res.Lines) != 1 || res.Lines[0] != "ok" {
		t.Fatalf("oversized not dropped: %q", res.Lines)
	}
}
