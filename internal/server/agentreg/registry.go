package agentreg

import (
	"context"
	"sync"

	bmcv1 "backupmanagementcenter/api/proto/v1"
)

// Registry tracks connected agent streams and provides send/receive coordination.
type Registry struct {
	mu sync.RWMutex

	// streams maps agentID -> *streamState
	streams map[string]*streamState

	// onDisconnect callbacks
	onDisconnect []func(agentID string)
}

// streamState holds per-agent stream state.
type streamState struct {
	agentID string
	sendCh  chan *bmcv1.ServerMessage
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewRegistry creates a new agent registry.
func NewRegistry() *Registry {
	return &Registry{
		streams: make(map[string]*streamState),
	}
}

// Register adds or replaces a stream for the given agent.
// If an existing stream exists, it is cancelled (kicked) before the new one takes over.
// Returns a send channel for the server to push messages to the agent.
// The caller MUST call Unregister when the stream ends.
func (r *Registry) Register(ctx context.Context, agentID string) (<-chan *bmcv1.ServerMessage, context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Kick existing stream if any
	if existing, ok := r.streams[agentID]; ok {
		existing.cancel()
	}

	streamCtx, cancel := context.WithCancel(ctx)
	sendCh := make(chan *bmcv1.ServerMessage, 32)

	st := &streamState{
		agentID: agentID,
		sendCh:  sendCh,
		ctx:     streamCtx,
		cancel:  cancel,
	}
	r.streams[agentID] = st

	return sendCh, streamCtx
}

// Unregister removes the stream for the given agent.
// Should be called when the gRPC stream ends.
func (r *Registry) Unregister(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if st, ok := r.streams[agentID]; ok {
		st.cancel()
		delete(r.streams, agentID)
	}
}

// Send sends a message to the agent's stream.
// Returns error if agent is not connected or context is cancelled.
func (r *Registry) Send(agentID string, msg *bmcv1.ServerMessage) error {
	r.mu.RLock()
	st, ok := r.streams[agentID]
	r.mu.RUnlock()

	if !ok {
		return ErrAgentNotConnected
	}

	select {
	case st.sendCh <- msg:
		return nil
	case <-st.ctx.Done():
		return st.ctx.Err()
	}
}

// Connected returns true if the agent has a live stream.
func (r *Registry) Connected(agentID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.streams[agentID]
	return ok
}

// IsConnected is an alias of Connected kept for dispatcher call sites.
func (r *Registry) IsConnected(agentID string) bool { return r.Connected(agentID) }

// List returns all connected agent IDs.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.streams))
	for id := range r.streams {
		ids = append(ids, id)
	}
	return ids
}

// OnDisconnect registers a callback invoked when an agent disconnects.
// The callback is called with the agentID after the stream is removed.
func (r *Registry) OnDisconnect(fn func(agentID string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onDisconnect = append(r.onDisconnect, fn)
}

// notifyDisconnect calls all registered disconnect callbacks.
func (r *Registry) notifyDisconnect(agentID string) {
	for _, fn := range r.onDisconnect {
		fn(agentID)
	}
}

// ErrAgentNotConnected is returned when sending to a disconnected agent.
var ErrAgentNotConnected = &agentNotConnectedErr{}

type agentNotConnectedErr struct{}

func (e *agentNotConnectedErr) Error() string {
	return "agent not connected"
}

func (e *agentNotConnectedErr) Is(target error) bool {
	_, ok := target.(*agentNotConnectedErr)
	return ok
}