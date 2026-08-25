// Package logging 提供将标准库 log 输出转成结构化日志的轻量采集器。
package logging

import (
	"io"
	"strings"
	"sync"
	"time"
)

// Entry 是一条进程日志。Seq 在单个进程生命周期内递增。
type Entry struct {
	Seq       uint64
	Timestamp time.Time
	Level     string
	Message   string
}

// Handler 接收采集到的日志；返回错误时日志会暂存，等待下一次绑定。
type Handler func(Entry) error

// Sink 同时保留标准错误输出，并把日志转发给可变更的 Handler。
// Handler 不可用期间最多缓存 maxPending 条日志，避免连接故障导致内存无限增长。
type Sink struct {
	out         io.Writer
	maxPending  int
	mu          sync.Mutex
	sendMu      sync.Mutex
	handler     Handler
	pending     []Entry
	partialLine string
	nextSeq     uint64
}

// NewSink 创建日志采集器。
func NewSink(out io.Writer, maxPending int) *Sink {
	if maxPending <= 0 {
		maxPending = 2048
	}
	return &Sink{out: out, maxPending: maxPending}
}

// SetHandler 绑定 Handler，并按原顺序发送连接期间暂存的日志。
func (s *Sink) SetHandler(handler Handler) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	s.mu.Lock()
	s.handler = handler
	var pending []Entry
	if handler != nil {
		pending = append([]Entry(nil), s.pending...)
		s.pending = nil
	}
	s.mu.Unlock()

	if handler != nil {
		s.deliverLocked(handler, pending)
	}
}

// ClearHandler 停止向当前连接转发日志；后续日志会进入缓存。
func (s *Sink) ClearHandler() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	s.handler = nil
	s.mu.Unlock()
}

// Write 实现 io.Writer，供标准库 log.SetOutput 使用。
func (s *Sink) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	var outErr error
	if s.out != nil {
		_, outErr = s.out.Write(p)
	}

	s.mu.Lock()
	s.partialLine += string(p)
	parts := strings.Split(s.partialLine, "\n")
	s.partialLine = parts[len(parts)-1]
	entries := make([]Entry, 0, len(parts)-1)
	for _, line := range parts[:len(parts)-1] {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		s.nextSeq++
		level, message := parseLine(line)
		entries = append(entries, Entry{
			Seq:       s.nextSeq,
			Timestamp: time.Now().UTC(),
			Level:     level,
			Message:   message,
		})
	}
	s.mu.Unlock()

	if len(entries) > 0 {
		s.sendMu.Lock()
		s.mu.Lock()
		handler := s.handler
		s.mu.Unlock()
		if handler != nil {
			s.deliverLocked(handler, entries)
		} else {
			s.mu.Lock()
			s.appendPendingLocked(entries)
			s.mu.Unlock()
		}
		s.sendMu.Unlock()
	}
	return len(p), outErr
}

// deliverLocked 需要调用方持有 sendMu，以保证日志顺序。
func (s *Sink) deliverLocked(handler Handler, entries []Entry) {
	for i, entry := range entries {
		if err := handler(entry); err != nil {
			s.mu.Lock()
			if s.handler != nil {
				s.handler = nil
			}
			s.appendPendingLocked(entries[i:])
			s.mu.Unlock()
			return
		}
	}
}

func (s *Sink) appendPendingLocked(entries []Entry) {
	if len(entries) == 0 {
		return
	}
	s.pending = append(s.pending, entries...)
	if len(s.pending) > s.maxPending {
		s.pending = s.pending[len(s.pending)-s.maxPending:]
	}
}

func parseLine(line string) (string, string) {
	message := strings.TrimSpace(line)
	level := "info"
	for _, prefix := range []struct {
		text  string
		level string
	}{
		{"[DEBUG]", "debug"},
		{"[INFO]", "info"},
		{"[WARN]", "warn"},
		{"[ERROR]", "error"},
		{"[FATAL]", "error"},
	} {
		if strings.HasPrefix(strings.ToUpper(message), prefix.text) {
			level = prefix.level
			message = strings.TrimSpace(message[len(prefix.text):])
			break
		}
	}
	if level == "info" {
		lower := strings.ToLower(message)
		switch {
		case strings.Contains(lower, "level=debug"):
			level = "debug"
		case strings.Contains(lower, "level=warn"):
			level = "warn"
		case strings.Contains(lower, "level=error"):
			level = "error"
		}
		if level == "info" {
			switch {
			case strings.Contains(lower, "failed "),
				strings.Contains(lower, "failure "),
				strings.Contains(lower, " error"),
				strings.HasPrefix(lower, "error"),
				strings.Contains(lower, "unable "),
				strings.Contains(lower, "cannot "):
				level = "error"
			case strings.Contains(lower, "warning "),
				strings.HasPrefix(lower, "warn "):
				level = "warn"
			}
		}
	}
	return level, message
}
