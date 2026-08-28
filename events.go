package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// Event 运行事件,进内存环形缓冲供 Web 后台展示,同时写标准日志。
type Event struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"` // info | ok | warn | error
	Msg   string    `json:"msg"`
}

type EventLog struct {
	mu      sync.Mutex
	buf     []Event
	max     int
	path    string // 持久化文件(jsonl);为空则仅内存
	written int    // 启动以来追加的行数,超阈值触发压缩重写
}

var Ev = &EventLog{max: 300}

// Init 启用持久化:回载历史事件,之后每条事件追加落盘。
func (l *EventLog) Init(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var loaded []Event
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var e Event
		if json.Unmarshal(line, &e) == nil {
			loaded = append(loaded, e)
		}
	}
	if len(loaded) > l.max {
		loaded = loaded[len(loaded)-l.max:]
	}
	l.buf = append(loaded, l.buf...)
	l.rewriteLocked()
}

func (l *EventLog) Add(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[%s] %s", level, msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	e := Event{Time: time.Now(), Level: level, Msg: msg}
	l.buf = append(l.buf, e)
	if len(l.buf) > l.max {
		l.buf = l.buf[len(l.buf)-l.max:]
	}
	if l.path == "" {
		return
	}
	line, _ := json.Marshal(e)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err == nil {
		f.Write(append(line, '\n'))
		f.Close()
		l.written++
	}
	if l.written > 3000 { // 文件里累积太多旧行,压缩为内存中的最近若干条
		l.rewriteLocked()
	}
}

func (l *EventLog) rewriteLocked() {
	if l.path == "" {
		return
	}
	var out bytes.Buffer
	for _, e := range l.buf {
		line, _ := json.Marshal(e)
		out.Write(append(line, '\n'))
	}
	tmp := l.path + ".tmp"
	if os.WriteFile(tmp, out.Bytes(), 0600) == nil {
		os.Rename(tmp, l.path)
	}
	l.written = 0
}

// Recent 返回最近 n 条(新的在前)。
func (l *EventLog) Recent(n int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > len(l.buf) {
		n = len(l.buf)
	}
	out := make([]Event, n)
	for i := 0; i < n; i++ {
		out[i] = l.buf[len(l.buf)-1-i]
	}
	return out
}
