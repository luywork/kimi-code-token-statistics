// Package state 负责 metrics.State 的 JSON 持久化（重启续读断点）。
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"kimi-hud/internal/metrics"
)

// Store 状态存储。
type Store struct {
	dir string
}

// NewStore 创建指向 dir 的状态存储。
func NewStore(dir string) *Store { return &Store{dir: dir} }

// validSessionID 校验会话 ID 安全性：需带 session_/ses_ 前缀且不含路径分隔符，
// 防止构造越界路径或写入脏文件名（如历史遗留的 "agents"）。
func validSessionID(id string) bool {
	if !strings.HasPrefix(id, "session_") && !strings.HasPrefix(id, "ses_") {
		return false
	}
	return !strings.ContainsAny(id, `/\`)
}

// StatePath 返回某会话的状态文件路径；非法会话 ID 返回空串。
func (s *Store) StatePath(sessionID string) string {
	if !validSessionID(sessionID) {
		return ""
	}
	return filepath.Join(s.dir, "state-"+sessionID+".json")
}

// Load 读取会话状态；文件不存在、损坏或非法会话 ID 时返回空状态。
func (s *Store) Load(sessionID string) *metrics.State {
	path := s.StatePath(sessionID)
	if path == "" {
		return metrics.NewState()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return metrics.NewState()
	}
	var st metrics.State
	if err := json.Unmarshal(data, &st); err != nil {
		return metrics.NewState()
	}
	if st.Agents == nil {
		st.Agents = map[string]*metrics.Agent{}
	}
	if st.Version != metrics.StateVersion {
		st.Agents = map[string]*metrics.Agent{}
	}
	return &st
}

// Save 原子写会话状态（tmp + rename）；非法会话 ID 直接跳过。
func (s *Store) Save(sessionID string, st *metrics.State) error {
	path := s.StatePath(sessionID)
	if path == "" {
		return nil
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrNoSession 表示没有可用的活跃会话。
var ErrNoSession = errors.New("no active session")
