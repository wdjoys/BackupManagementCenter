package api

import (
	"encoding/json"
	"net/http"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/jobs"
)

// POST /restores/dry-run — filesystem only; returns would-be change stats.
func (s *Server) handleDryRunRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepositoryID string   `json:"repository_id"`
		SnapshotID   string   `json:"snapshot_id"`
		IncludePaths []string `json:"include_paths,omitempty"`
		TargetPath   string   `json:"target_path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.RepositoryID == "" || body.SnapshotID == "" || body.TargetPath == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "repository_id, snapshot_id and target_path are required")
		return
	}
	if len(body.TargetPath) == 0 || body.TargetPath[0] != '/' {
		writeErr(w, http.StatusBadRequest, "validation_failed", "target_path must be absolute")
		return
	}
	stats, _, err := s.Jobs.DryRunRestore(r.Context(), body.RepositoryID, body.SnapshotID, body.IncludePaths, body.TargetPath)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// POST /restores
func (s *Server) handleStartRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepositoryID string             `json:"repository_id"`
		SnapshotID   string             `json:"snapshot_id"`
		RestoreKind  string             `json:"restore_kind"`
		Target       model.RestoreTarget `json:"target"`
		Overwrite    bool               `json:"overwrite"`
		Confirmation string             `json:"confirmation,omitempty"`
		// TargetPassword: database kinds only, entered in the UI.
		TargetPassword string            `json:"target_password,omitempty"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	switch body.RestoreKind {
	case model.KindFilesystem:
		if body.Target.TargetPath == "" || body.Target.TargetPath[0] != '/' {
			writeErr(w, http.StatusBadRequest, "validation_failed", "filesystem restore needs absolute target_path")
			return
		}
		switch body.Target.OverwriteMode {
		case "never", "if-changed", "always":
		default:
			writeErr(w, http.StatusBadRequest, "validation_failed", "overwrite_mode must be never|if-changed|always")
			return
		}
	case model.KindPostgreSQL, model.KindMySQL, model.KindMongoDB, model.KindSQLite:
		if body.Target.Database == "" {
			writeErr(w, http.StatusBadRequest, "validation_failed", "database targets need target.database")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "validation_failed", "unknown restore_kind")
		return
	}
	in := jobs.RestoreInput{
		RepositoryID:   body.RepositoryID,
		SnapshotID:     body.SnapshotID,
		RestoreKind:    body.RestoreKind,
		Target:         body.Target,
		Overwrite:      body.Overwrite,
		Confirmation:   body.Confirmation,
		TargetPassword: body.TargetPassword,
	}
	req, run, err := s.Jobs.StartRestore(r.Context(), actorID(r), in)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	s.Met.ObserveRun("restore_requested", "queued", 0)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"restore_request_id": req.ID,
		"run":                runView(run),
	})
}

// GET /restores?limit=
func (s *Server) handleListRestores(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	reqs, err := s.ST.ListRestoreRequests(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]model.RestoreRequest, 0, len(reqs))
	for _, rr := range reqs {
		if rr.TargetJSON != "" {
			_ = json.Unmarshal([]byte(rr.TargetJSON), &rr.Target)
		}
		out = append(out, rr)
	}
	writeJSON(w, http.StatusOK, out)
}
