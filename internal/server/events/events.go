// Package events is the in-process pub/sub bus between the gRPC channel
// layer (producer: agent stream messages) and WebSocket/API consumers.
package events

import "backupmanagementcenter/internal/model"

type Type string

const (
	State    Type = "state"    // Run field changed (full snapshot)
	Progress Type = "progress" // progress update
	Log      Type = "log"      // single log entry
)

type Event struct {
	Type     Type
	Run      *model.Run      // State events; non-nil
	Progress *model.Progress // Progress events
	Entry    *model.RunLog   // Log events
}

type Bus interface {
	// Subscribe returns a receive-only channel receiving subsequent events
	// for the run plus a cancel function. Buffer size is implementation
	// defined; a lagging subscriber is dropped (channel closed) instead of
	// blocking producers.
	Subscribe(runID string) (<-chan Event, func())
	Publish(runID string, ev Event)
	Close()
}

// New returns the default in-process bus.
func New() Bus { return newFanout() }
