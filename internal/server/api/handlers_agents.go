package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
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
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Hostname     string          `json:"hostname"`
	OS           string          `json:"os"`
	Arch         string          `json:"arch"`
	Version      string          `json:"version"`
	Status       model.AgentStatus `json:"status"`
	LastSeenAt   *time.Time      `json:"last_seen_at,omitempty"`
	EnrolledAt   time.Time       `json:"enrolled_at"`
	Capabilities []model.ToolInfo `json:"capabilities"`
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
			Version: a.Version, Status: a.Status, LastSeenAt: a.LastSeenAt,
			EnrolledAt: a.EnrolledAt, Capabilities: a.Capabilities,
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
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "agent.revoke", "agent", id, nil)
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
	token, err := newToken32()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	t := &model.EnrollmentToken{
		ID:        newUUID(),
		TokenHash: secrets.HashToken(token),
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}
	if err := s.ST.CreateEnrollmentToken(r.Context(), t); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "enrollment.create_token", "enrollment_token", t.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": t.ExpiresAt})
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
