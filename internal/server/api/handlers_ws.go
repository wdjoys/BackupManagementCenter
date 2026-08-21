package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/auth"
	"backupmanagementcenter/internal/server/events"
)

// sessionForWS authenticates the cookie without CSRF (WS is a GET).
func (s *Server) sessionForWS(r *http.Request) (*model.Session, error) {
	return auth.Validate(r.Context(), s.ST, r)
}

// serveRunWS replays current state + backlog logs, then forwards live events
// until the run reaches terminal state.
func (s *Server) serveRunWS(w http.ResponseWriter, r *http.Request, runID string) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{r.Host}, // same-origin only
	})
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	ctx := r.Context()

	run, err := s.ST.GetRun(ctx, runID)
	if err != nil {
		c.Close(websocket.StatusInternalError, "run not found")
		return
	}

	sendEvent := func(ev events.Event) bool {
		payload, err := wsPayload(s, ev)
		if err != nil {
			return true
		}
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return c.Write(wctx, websocket.MessageText, payload) == nil
	}

	// Backlog: state then logs.
	if !sendEvent(events.Event{Type: events.State, Run: run}) {
		return
	}
	logs, err := s.ST.ListRunLogs(ctx, runID, 0, 100000)
	if err != nil {
		return
	}
	for i := range logs {
		if !sendEvent(events.Event{Type: events.Log, Entry: &logs[i]}) {
			return
		}
	}
	if isTerminal(run.Status) {
		return
	}

	ch, cancel := s.Bus.Subscribe(runID)
	defer cancel()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !sendEvent(ev) {
				return
			}
			if ev.Type == events.State && ev.Run != nil && isTerminal(ev.Run.Status) {
				return
			}
		case <-ticker.C:
			// Ping to keep intermediaries from idling out the connection.
			pctx, pcancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}

func wsPayload(s *Server, ev events.Event) ([]byte, error) {
	switch ev.Type {
	case events.State:
		return json.Marshal(map[string]any{"type": "state", "run": runView(ev.Run)})
	case events.Progress:
		return json.Marshal(map[string]any{"type": "progress", "progress": ev.Progress})
	case events.Log:
		e := *ev.Entry
		e.Message = redactMsg(e.Message)
		return json.Marshal(map[string]any{"type": "log", "entry": e})
	default:
		return json.Marshal(map[string]any{"type": string(ev.Type)})
	}
}

func isTerminal(status string) bool {
	switch status {
	case model.RunSucceeded, model.RunFailed, model.RunCancelled:
		return true
	}
	return false
}

var _ = chi.URLParam
