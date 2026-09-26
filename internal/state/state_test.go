package state

import (
	"os"
	"path/filepath"
	"testing"

	"kimi-hud/internal/metrics"
)

func TestStatePathRejectsUnsafeID(t *testing.T) {
	s := NewStore(t.TempDir())
	if p := s.StatePath("agents"); p != "" {
		t.Fatalf("bare id should be rejected, got %q", p)
	}
	if p := s.StatePath("session_ok"); p == "" {
		t.Fatal("session_ prefixed id should be accepted")
	}
	if p := s.StatePath(`session_../evil`); p != "" {
		t.Fatalf("path traversal should be rejected, got %q", p)
	}
}

func TestSaveRejectsUnsafeID(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save("agents", metrics.NewState()); err != nil {
		t.Fatalf("Save of unsafe id should no-op without error, got %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("unsafe id must not write any file, got %v", entries)
	}
}

func TestLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	st := metrics.NewState()
	st.ModelAlias = "kimi-code/k3"
	if err := s.Save("session_abc", st); err != nil {
		t.Fatal(err)
	}
	got := s.Load("session_abc")
	if got.ModelAlias != "kimi-code/k3" {
		t.Fatalf("ModelAlias = %q", got.ModelAlias)
	}
	if p := filepath.Base(s.StatePath("session_abc")); p != "state-session_abc.json" {
		t.Fatalf("path = %q", p)
	}
}
