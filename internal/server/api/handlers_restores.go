package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/jobs"

	"github.com/go-chi/chi/v5"
)

// POST /restores/dry-run — filesystem only; returns would-be change stats.
func (s *Server) handleDryRunRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepositoryID  string   `json:"repository_id"`
		SnapshotID    string   `json:"snapshot_id"`
		IncludePaths  []string `json:"include_paths,omitempty"`
		TargetPath    string   `json:"target_path"`
		OverwriteMode string   `json:"overwrite_mode"`
		TargetAgentID string   `json:"target_agent_id,omitempty"`
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
	stats, _, err := s.Jobs.DryRunRestore(r.Context(), body.RepositoryID, body.SnapshotID, body.IncludePaths, body.TargetPath, body.OverwriteMode, body.TargetAgentID)
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
		// TargetAgentID: optional executor; empty keeps the source agent.
		TargetAgentID string `json:"target_agent_id,omitempty"`
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
		// 整实例还原不开放：同步拒绝，避免来源快照的全部库写入目标实例。
		if strings.EqualFold(strings.TrimSpace(body.Target.Database), "all") {
			writeErr(w, http.StatusUnprocessableEntity, model.ErrUnsupportedRestoreManifest,
				"restoring every database in the snapshot is not supported; pick a single database")
			return
		}
		// 每个 kind 在真实实例上完成预备份/回滚端到端验证前保持 503 禁用。
		if !s.DatabaseRestoreKinds[body.RestoreKind] {
			writeErr(w, http.StatusServiceUnavailable, model.ErrDatabaseRestoreDisabled,
				"database restore for "+body.RestoreKind+" is disabled until pre-restore backup and rollback are verified; enable it with BMC_DATABASE_RESTORE_KINDS")
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
		TargetAgentID:  body.TargetAgentID,
	}
	req, run, err := s.Jobs.StartRestore(r.Context(), actorID(r), in)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	// 运行计数由 store 的终态观察者统一记录；此处不再写入合成的
	// "restore_requested/queued" 序列（它并非终态运行，会污染 bmc_runs_total）。
	writeJSON(w, http.StatusAccepted, map[string]any{
		"restore_request_id":   req.ID,
		"pre_restore_run_id":   req.PreRestoreRunID,
		"rollback_snapshot_id": req.RollbackSnapshotID,
		"phase":                req.Phase,
		"run":                  runView(run),
	})
}

// POST /restores/{id}/resolve — 人工确认并解除被阻塞的恢复请求。
// 只接受 manual_recovery_required / rollback_failed；要求 run ID 与处理说明，
// 由操作人承担“旧执行已停止、目标内容已核验/恢复”的确认责任。
func (s *Server) handleResolveRestore(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "id")
	if requestID == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "restore request id is required")
		return
	}
	var body struct {
		RunID            string `json:"run_id"`
		Note             string `json:"note"`
		ExecutionStopped bool   `json:"execution_stopped"`
		TargetVerified   bool   `json:"target_verified"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.RunID) == "" || strings.TrimSpace(body.Note) == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "run_id and note are required")
		return
	}
	if !body.ExecutionStopped || !body.TargetVerified {
		writeErr(w, http.StatusBadRequest, "validation_failed", "both execution_stopped and target_verified must be confirmed")
		return
	}
	if err := s.Jobs.ResolveRestore(r.Context(), actorID(r), requestID, strings.TrimSpace(body.RunID), strings.TrimSpace(body.Note)); err != nil {
		s.jobsErr(w, err)
		return
	}
	rr, err := s.ST.GetRestoreRequest(r.Context(), requestID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"restore_request_id":   rr.ID,
		"run_id":               rr.RunID,
		"phase":                rr.Phase,
		"rollback_snapshot_id": rr.RollbackSnapshotID,
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
