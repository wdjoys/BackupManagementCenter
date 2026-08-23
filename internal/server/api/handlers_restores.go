package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/jobs"
)

// POST /restores/dry-run — filesystem only; returns would-be change stats.
func (s *Server) handleDryRunRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepositoryID  string   `json:"repository_id"`
		SnapshotID    string   `json:"snapshot_id"`
		IncludePaths  []string `json:"include_paths,omitempty"`
		TargetPath    string   `json:"target_path"`
		OverwriteMode string   `json:"overwrite_mode"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.RepositoryID == "" || body.SnapshotID == "" || body.TargetPath == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "repository_id, snapshot_id and target_path are required")
		return
	}
	if !isAbsPath(body.TargetPath) {
		writeErr(w, http.StatusBadRequest, "validation_failed", "target_path must be absolute")
		return
	}
	if body.OverwriteMode == "" {
		body.OverwriteMode = "always"
	}
	if body.OverwriteMode != "never" && body.OverwriteMode != "if-changed" && body.OverwriteMode != "always" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "overwrite_mode must be never|if-changed|always")
		return
	}
	stats, _, err := s.Jobs.DryRunRestore(r.Context(), body.RepositoryID, body.SnapshotID, body.IncludePaths, body.TargetPath, body.OverwriteMode)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// POST /restores
func (s *Server) handleStartRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepositoryID string              `json:"repository_id"`
		SnapshotID   string              `json:"snapshot_id"`
		RestoreKind  string              `json:"restore_kind"`
		Target       model.RestoreTarget `json:"target"`
		Overwrite    bool                `json:"overwrite"`
		Confirmation string              `json:"confirmation,omitempty"`
		// TargetPassword: database kinds only, entered in the UI.
		TargetPassword string `json:"target_password,omitempty"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	switch body.RestoreKind {
	case model.KindFilesystem:
		if !isAbsPath(body.Target.TargetPath) {
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
		// Database overwrite/pre-restore rollback is intentionally gated until
		// the end-to-end restore suite is enabled and passing. Filesystem
		// restore and read-only browsing remain available meanwhile.
		if os.Getenv("BMC_ENABLE_DATABASE_RESTORE") != "1" {
			writeErr(w, http.StatusServiceUnavailable, model.ErrDatabaseRestoreDisabled, "database restore is disabled until pre-restore/rollback verification is enabled")
			return
		}
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
		"restore_request_id":   req.ID,
		"pre_restore_run_id":   req.PreRestoreRunID,
		"rollback_snapshot_id": req.RollbackSnapshotID,
		"phase":                req.Phase,
		"run":                  runView(run),
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

// isAbsPath accepts POSIX absolute paths and Windows drive-letter paths
// (development hosts run the agent on Windows too).
func isAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	if len(p) >= 2 && p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		return true
	}
	return false
}
