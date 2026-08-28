package main

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

// State 持久化各邮箱已处理到的 UID 游标,重启不重复转发。
type State struct {
	mu      sync.Mutex
	path    string
	LastUID uint32            `json:"last_uid,omitempty"` // 旧版单账户游标,读取时兜底
	Cursors map[string]uint32 `json:"cursors"`
}

func LoadState(path string) *State {
	s := &State{path: path, Cursors: map[string]uint32{}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, s); err != nil {
			log.Printf("state 文件损坏,忽略: %v", err)
		}
		if s.Cursors == nil {
			s.Cursors = map[string]uint32{}
		}
	}
	return s
}

// Get 某邮箱的游标;0 表示还没监听过(首连时跳过历史邮件)。
func (s *State) Get(mailbox string) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cursors[mailbox]
}

func (s *State) Save(mailbox string, uid uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Cursors[mailbox] = uid
	data, _ := json.Marshal(s)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("写 state 失败: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("写 state 失败: %v", err)
	}
}

// Snapshot 各邮箱游标快照,状态接口用。
func (s *State) Snapshot() map[string]uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]uint32, len(s.Cursors))
	for k, v := range s.Cursors {
		out[k] = v
	}
	return out
}
