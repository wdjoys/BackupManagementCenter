package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/store"
)

const planKinds = "filesystem|postgresql|mysql|mongodb|sqlite"

type planView struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	AgentID        string          `json:"agent_id"`
	Kind           string          `json:"kind"`
	Schedule       string          `json:"schedule"`
	Timezone       string          `json:"timezone"`
	Enabled        bool            `json:"enabled"`
	Source         json.RawMessage `json:"source"`
	RepositoryID   string          `json:"repository_id"`
	Retention      model.Retention `json:"retention"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
}

func planToView(p *model.Plan) planView {
	return planView{
		ID: p.ID, Name: p.Name, AgentID: p.AgentID, Kind: p.Kind,
		Schedule: p.Schedule, Timezone: p.Timezone, Enabled: p.Enabled,
		Source: json.RawMessage(p.SourceJSON), RepositoryID: p.RepositoryID,
		Retention: p.Retention, TimeoutSeconds: p.TimeoutSeconds,
		CreatedAt: p.CreatedAt.Format(timeRFC3339), UpdatedAt: p.UpdatedAt.Format(timeRFC3339),
	}
}

// GET /plans?agent_id=
func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := s.ST.ListPlans(r.Context(), r.URL.Query().Get("agent_id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]planView, 0, len(plans))
	for i := range plans {
		out = append(out, planToView(&plans[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /plans/{id}
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	p, err := s.ST.GetPlan(r.Context(), pathParam(r, "id"))
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, planToView(p))
}

type planBody struct {
	Name           string          `json:"name"`
	AgentID        string          `json:"agent_id"`
	Kind           string          `json:"kind"`
	Schedule       string          `json:"schedule"`
	Timezone       string          `json:"timezone"`
	Enabled        *bool           `json:"enabled"`
	Source         json.RawMessage `json:"source"`
	RepositoryID   string          `json:"repository_id"`
	Retention      model.Retention `json:"retention"`
	TimeoutSeconds int             `json:"timeout_seconds"`
}

func (b *planBody) validate() (string, bool) {
	switch {
	case len(b.Name) < 1 || len(b.Name) > 200:
		return "name must be 1..200 chars", false
	case !validKind(b.Kind):
		return "kind must be one of " + planKinds, false
	case b.AgentID == "" || b.RepositoryID == "":
		return "agent_id and repository_id are required", false
	case b.Schedule == "":
		return "schedule (5-field cron) is required", false
	case b.Timezone == "":
		b.Timezone = "UTC"
	case b.TimeoutSeconds == 0:
		b.TimeoutSeconds = 3600
	}
	if b.TimeoutSeconds < 60 || b.TimeoutSeconds > 86400 {
		return "timeout_seconds must be 60..86400", false
	}
	if b.Retention.KeepLast+b.Retention.KeepDaily+b.Retention.KeepWeekly+b.Retention.KeepMonthly == 0 {
		return "retention must keep at least one rule", false
	}
	return "", true
}

func validKind(k string) bool {
	switch k {
	case model.KindFilesystem, model.KindPostgreSQL, model.KindMySQL, model.KindMongoDB, model.KindSQLite:
		return true
	}
	return false
}

// POST /plans
func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	var body planBody
	if !readJSON(w, r, &body) {
		return
	}
	if msg, ok := body.validate(); !ok {
		writeErr(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	var src model.PlanSource
	if err := json.Unmarshal(body.Source, &src); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "invalid source: "+err.Error())
		return
	}
	if err := s.Jobs.ValidatePlanSource(r.Context(), body.Kind, src, body.AgentID); err != nil {
		s.jobsErr(w, err)
		return
	}
	repo, err := s.ST.GetRepository(r.Context(), body.RepositoryID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	if repo.AgentID != body.AgentID {
		writeErr(w, http.StatusBadRequest, "validation_failed", "repository does not belong to this agent")
		return
	}
	now := time.Now().UTC()
	p := &model.Plan{
		ID: newUUID(), Name: body.Name, AgentID: body.AgentID, Kind: body.Kind,
		Schedule: body.Schedule, Timezone: body.Timezone,
		Enabled: body.Enabled == nil || *body.Enabled,
		SourceJSON: string(body.Source), RepositoryID: body.RepositoryID,
		Retention: body.Retention, TimeoutSeconds: body.TimeoutSeconds,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.ST.CreatePlan(r.Context(), p); err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "plan.create", "plan", p.ID, marshalDetail(map[string]string{"name": p.Name, "kind": p.Kind}))
	writeJSON(w, http.StatusCreated, planToView(p))
}

// PUT /plans/{id}
func (s *Server) handleUpdatePlan(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	existing, err := s.ST.GetPlan(r.Context(), id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	var body planBody
	if !readJSON(w, r, &body) {
		return
	}
	if msg, ok := body.validate(); !ok {
		writeErr(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	var src model.PlanSource
	if err := json.Unmarshal(body.Source, &src); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "invalid source: "+err.Error())
		return
	}
	if err := s.Jobs.ValidatePlanSource(r.Context(), body.Kind, src, body.AgentID); err != nil {
		s.jobsErr(w, err)
		return
	}
	existing.Name = body.Name
	existing.AgentID = body.AgentID
	existing.Kind = body.Kind
	existing.Schedule = body.Schedule
	existing.Timezone = body.Timezone
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	existing.SourceJSON = string(body.Source)
	existing.RepositoryID = body.RepositoryID
	existing.Retention = body.Retention
	existing.TimeoutSeconds = body.TimeoutSeconds
	existing.UpdatedAt = time.Now().UTC()
	if err := s.ST.UpdatePlan(r.Context(), existing); err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "plan.update", "plan", id, nil)
	writeJSON(w, http.StatusOK, planToView(existing))
}

// DELETE /plans/{id} — rejected while runs exist.
func (s *Server) handleDeletePlan(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := s.ST.DeletePlan(r.Context(), id); err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "plan.delete", "plan", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// POST /plans/validate {kind, source, agent_id}
func (s *Server) handleValidatePlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind    string          `json:"kind"`
		Source  json.RawMessage `json:"source"`
		AgentID string          `json:"agent_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	var src model.PlanSource
	if err := json.Unmarshal(body.Source, &src); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "invalid source: "+err.Error())
		return
	}
	if err := s.Jobs.ValidatePlanSource(r.Context(), body.Kind, src, body.AgentID); err != nil {
		s.jobsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /plans/{id}/run — manual run.
func (s *Server) handleRunPlan(w http.ResponseWriter, r *http.Request) {
	run, err := s.Jobs.ManualRun(r.Context(), pathParam(r, "id"))
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	s.Met.SetQueueDepth(queueDepthHint(r))
	writeJSON(w, http.StatusAccepted, runView(run))
}

func queueDepthHint(_ *http.Request) int { return 0 } // real gauge set by dispatcher

var _ = store.ErrNotFound
var _ = strings.Contains
