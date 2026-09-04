package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
)

func newUUID() string {
	id, err := uuidV7()
	if err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return id
}

func uuidV7() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// UUIDv7: 48-bit unix ms in top bytes, version 7, random rest.
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	return formatUUID(b), nil
}

func formatUUID(b [16]byte) string {
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func logf(format string, args ...any) { log.Printf(format, args...) }

func newToken32() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ---- Agents ----

type agentView struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	Hostname            string              `json:"hostname"`
	OS                  string              `json:"os"`
	Arch                string              `json:"arch"`
	Version             string              `json:"version"`
	Status              model.AgentStatus   `json:"status"`
	Revoked             bool                `json:"revoked"`
	LastSeenAt          *time.Time          `json:"last_seen_at,omitempty"`
	EnrolledAt          time.Time           `json:"enrolled_at"`
	Capabilities        []model.ToolInfo    `json:"capabilities"`
	SourcePathMappings  []model.PathMapping `json:"source_path_mappings"`
	RestorePathMappings []model.PathMapping `json:"restore_path_mappings"`
}

// GET /agents
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.ST.ListAgents(r.Context())
	if err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	out := make([]agentView, 0, len(agents))
	for _, a := range agents {
		out = append(out, agentView{
			ID: a.ID, Name: a.Name, Hostname: a.Hostname, OS: a.OS, Arch: a.Arch,
			Version: a.Version, Status: a.Status, Revoked: a.Revoked, LastSeenAt: a.LastSeenAt,
			EnrolledAt: a.EnrolledAt, Capabilities: a.Capabilities, SourcePathMappings: a.SourcePathMappings, RestorePathMappings: a.RestorePathMappings,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /agents/{id} — revoke.
func (s *Server) handleRevokeAgent(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := s.ST.RevokeAgent(r.Context(), id); err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	// Kick any live stream immediately; the revoked flag alone only blocks
	// future Connect calls.
	if s.Reg != nil {
		s.Reg.Unregister(id)
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "agent.revoke", "agent", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// PATCH /agents/{id} — rename.
func (s *Server) handleRenameAgent(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 128 {
		writeErr(w, http.StatusBadRequest, "invalid_name", "name must be 1-128 characters")
		return
	}
	if err := s.ST.RenameAgent(r.Context(), id, name); err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "agent.rename", "agent", id, marshalDetail(map[string]any{"name": name}))
	w.WriteHeader(http.StatusNoContent)
}
func actorID(r *http.Request) string {
	if id := r.Header.Get("X-Actor-ID"); id != "" {
		return id
	}
	return ""
}

// ---- Enrollment tokens ----

type enrollmentTokenView struct {
	ID        string     `json:"id"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
}

// POST /enrollment-tokens — returns the plaintext token exactly once.
func (s *Server) handleCreateEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TargetAgentID string `json:"target_agent_id"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}
	}
	if input.TargetAgentID != "" {
		agent, err := s.ST.GetAgent(r.Context(), input.TargetAgentID)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "agent_not_found", "agent not found")
			return
		}
		if agent.Revoked {
			writeErr(w, http.StatusBadRequest, model.ErrAgentRevoked, "agent is revoked")
			return
		}
		if agent.Status == model.AgentOnline {
			writeErr(w, http.StatusBadRequest, model.ErrAgentOnline, "agent must be offline")
			return
		}
	}
	token, err := newToken32()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	t := &model.EnrollmentToken{ID: newUUID(), TokenHash: secrets.HashToken(token), ExpiresAt: time.Now().UTC().Add(15 * time.Minute), TargetAgentID: input.TargetAgentID}
	if err := s.ST.CreateEnrollmentToken(r.Context(), t); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	action := "enrollment.create_token"
	if input.TargetAgentID != "" {
		action = "enrollment.create_takeover_token"
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), action, "enrollment_token", t.ID, map[string]any{"target_agent_id": input.TargetAgentID})
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": t.ExpiresAt, "target_agent_id": input.TargetAgentID})
}

// GET /enrollment-tokens
func (s *Server) handleListEnrollmentTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.ST.ListEnrollmentTokens(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]enrollmentTokenView, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, enrollmentTokenView{ID: t.ID, ExpiresAt: t.ExpiresAt, UsedAt: t.UsedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// marshalDetail keeps audit payloads small and JSON-safe.
func marshalDetail(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return json.RawMessage(b)
}
