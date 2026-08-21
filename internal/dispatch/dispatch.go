// Package dispatch defines how the server hands runs to connected agents.
// The scheduler and API layer depend only on this interface; the gRPC channel
// layer implements it.
package dispatch

import "context"

// Dispatcher queues runs to agents. Implementations must serialize commands
// per repository (one writer per restic repo at a time) and keep runs queued
// while the agent is offline until their deadline.
type Dispatcher interface {
	// Enqueue registers a persisted run for delivery. Non-blocking.
	Enqueue(ctx context.Context, runID, agentID, repositoryID string)
	// Cancel asks the agent to stop a dispatched/running run; the run ends
	// cancelled or failed depending on agent confirmation/timeout.
	Cancel(ctx context.Context, runID string) error
	// ConnectedAgents returns IDs of agents with a live stream.
	ConnectedAgents() []string
	// IsConnected reports whether the agent stream is live.
	IsConnected(agentID string) bool
}
