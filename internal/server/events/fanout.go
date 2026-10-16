package events

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// droppedSubscribers 累计因接收端过慢而被丢弃的订阅者数（见 Publish 的 default 分支）。
var droppedSubscribers atomic.Int64

// DroppedSubscribers 返回累计丢弃的订阅者数，供指标导出。
func DroppedSubscribers() int64 { return droppedSubscribers.Load() }

type fanout struct {
	mu   sync.Mutex
	subs map[string]map[*subscriber]struct{}
	done chan struct{}
}

type subscriber struct {
	ch  chan Event
	mu  sync.Mutex
	off bool
}

func newFanout() Bus {
	return &fanout{subs: map[string]map[*subscriber]struct{}{}, done: make(chan struct{})}
}

func (f *fanout) Subscribe(runID string) (<-chan Event, func()) {
	sub := &subscriber{ch: make(chan Event, 256)}
	f.mu.Lock()
	if f.subs[runID] == nil {
		f.subs[runID] = map[*subscriber]struct{}{}
	}
	f.subs[runID][sub] = struct{}{}
	f.mu.Unlock()
	cancel := func() { f.remove(runID, sub) }
	return sub.ch, cancel
}

func (f *fanout) remove(runID string, sub *subscriber) {
	sub.mu.Lock()
	if sub.off {
		sub.mu.Unlock()
		return
	}
	sub.off = true
	close(sub.ch)
	sub.mu.Unlock()
	f.detach(runID, sub)
}

// detach drops an already-closed subscriber from the run's subscriber set.
func (f *fanout) detach(runID string, sub *subscriber) {
	f.mu.Lock()
	delete(f.subs[runID], sub)
	if len(f.subs[runID]) == 0 {
		delete(f.subs, runID)
	}
	f.mu.Unlock()
}

func (f *fanout) Publish(runID string, ev Event) {
	f.mu.Lock()
	subs := make([]*subscriber, 0, len(f.subs[runID]))
	for s := range f.subs[runID] {
		subs = append(subs, s)
	}
	f.mu.Unlock()
	for _, s := range subs {
		// Send and close both hold sub.mu: a send must never race with the close
		// performed by remove() (the WS handler's cancel on disconnect), or the
		// producer panics with "send on closed channel" and takes down the server.
		s.mu.Lock()
		if s.off {
			s.mu.Unlock()
			continue
		}
		select {
		case s.ch <- ev:
			s.mu.Unlock()
		default:
			// Slow consumer: drop it rather than block the agent stream.
			s.off = true
			close(s.ch)
			s.mu.Unlock()
			f.detach(runID, s)
			// 丢弃此前完全静默：慢消费者（卡住的 WS 客户端）造成的进度/日志丢失在
			// 日志与指标里都看不到，只能从"界面不再更新"反推。这里补上 warn 与计数。
			droppedSubscribers.Add(1)
			slog.Warn("events: dropped slow subscriber", "run_id", runID, "event_type", string(ev.Type))
		}
	}
}

func (f *fanout) Close() {}
