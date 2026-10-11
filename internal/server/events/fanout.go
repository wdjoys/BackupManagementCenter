package events

import "sync"

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
		}
	}
}

func (f *fanout) Close() {}
