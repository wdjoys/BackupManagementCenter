package api

import (
	"errors"
	"net/http"
	"strings"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/jobs"
)

// ---- Storage targets ----

type storageTargetView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	RemoteName string    `json:"remote_name"`
	RemotePath string    `json:"remote_path"`
	CreatedAt  string    `json:"created_at"`
	UpdatedAt  string    `json:"updated_at"`
}

func targetView(t *model.StorageTarget) storageTargetView {
	return storageTargetView{ID: t.ID, Name: t.Name, Type: t.Type, RemoteName: t.RemoteName, RemotePath: t.RemotePath, CreatedAt: t.CreatedAt.Format(timeRFC3339), UpdatedAt: t.UpdatedAt.Format(timeRFC3339)}
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

// POST /storage-targets/validate
func (s *Server) handleValidateStorageTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RcloneConf      string `json:"rclone_conf"`
		RemoteName      string `json:"remote_name"`
		ValidateAgentID string `json:"validate_agent_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	res, err := s.Jobs.ValidateStorageRemote(r.Context(), body.RcloneConf, body.RemoteName, body.ValidateAgentID)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// POST /storage-targets
func (s *Server) handleCreateStorageTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string `json:"name"`
		RcloneConf      string `json:"rclone_conf"`
		RemoteName      string `json:"remote_name"`
		RemotePath      string `json:"remote_path"`
		ValidateAgentID string `json:"validate_agent_id,omitempty"` // required when validate=true
		Validate        bool   `json:"validate"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Name == "" || body.RemoteName == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "name and remote_name are required")
		return
	}
	t, err := s.Jobs.CreateStorageTargetWithAgent(r.Context(), actorID(r), body.Name, body.RcloneConf, body.RemoteName, body.RemotePath, body.ValidateAgentID, body.Validate)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, targetView(t))
}

// GET /storage-targets
func (s *Server) handleListStorageTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.ST.ListStorageTargets(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]storageTargetView, 0, len(targets))
	for i := range targets {
		out = append(out, targetView(&targets[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// PATCH /storage-targets/{id} {name}
// Only the display name is editable. The encrypted connection configuration
// and remote path are immutable after import; create a new target to rotate
// credentials or move a remote safely.
func (s *Server) handleRenameStorageTarget(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "name is required")
		return
	}
	t, err := s.Jobs.RenameStorageTarget(r.Context(), actorID(r), pathParam(r, "id"), body.Name)
	if err != nil {
		if errors.Is(err, jobs.ErrStorageTargetName) {
			writeErr(w, http.StatusBadRequest, "validation_failed", "name is required")
			return
		}
		if !mapStoreErr(w, err) {
			s.jobsErr(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, targetView(t))
}

// DELETE /storage-targets/{id}
func (s *Server) handleDeleteStorageTarget(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := s.ST.DeleteStorageTarget(r.Context(), id); err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "storage_target.delete", "storage_target", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Repositories ----

type repositoryView struct {
	ID               string     `json:"id"`
	AgentID          string     `json:"agent_id"`
	AgentName        string     `json:"agent_name,omitempty"`
	StorageTargetID  string     `json:"storage_target_id"`
	StorageTargetName string    `json:"storage_target_name,omitempty"`
	RepositoryPath   string     `json:"repository_path"`
	Status           string     `json:"status"`
	LastCheckAt      *string    `json:"last_check_at,omitempty"`
	CreatedAt        string     `json:"created_at"`
	UpdatedAt        string     `json:"updated_at"`
}

// POST /repositories {agent_id, storage_target_id}
func (s *Server) handleBindRepository(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AgentID         string `json:"agent_id"`
		StorageTargetID string `json:"storage_target_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	repo, err := s.Jobs.BindRepository(r.Context(), actorID(r), body.AgentID, body.StorageTargetID)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.repoView(r, repo))
}

// GET /repositories
func (s *Server) handleListRepositories(w http.ResponseWriter, r *http.Request) {
	repos, err := s.ST.ListRepositories(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]repositoryView, 0, len(repos))
	for i := range repos {
		out = append(out, s.repoView(r, &repos[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /repositories/{id} removes the server-side binding only. Remote
// Restic snapshots are preserved and can be adopted again by binding the same
// Agent and storage target later.
func (s *Server) handleUnbindRepository(w http.ResponseWriter, r *http.Request) {
	if err := s.Jobs.UnbindRepository(r.Context(), actorID(r), pathParam(r, "id")); err != nil {
		if !mapStoreErr(w, err) {
			s.jobsErr(w, err)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) repoView(r *http.Request, repo *model.Repository) repositoryView {
	v := repositoryView{
		ID: repo.ID, AgentID: repo.AgentID, StorageTargetID: repo.StorageTargetID,
		RepositoryPath: repo.RepositoryPath, Status: repo.Status,
		CreatedAt: repo.CreatedAt.Format(timeRFC3339), UpdatedAt: repo.UpdatedAt.Format(timeRFC3339),
	}
	if repo.LastCheckAt != nil {
		s := repo.LastCheckAt.Format(timeRFC3339)
		v.LastCheckAt = &s
	}
	if a, err := s.ST.GetAgent(r.Context(), repo.AgentID); err == nil {
		v.AgentName = a.Name
	}
	if t, err := s.ST.GetStorageTarget(r.Context(), repo.StorageTargetID); err == nil {
		v.StorageTargetName = t.Name
	}
	return v
}

// GET /repositories/{id}/snapshots — synchronous browse run on the agent.
func (s *Server) handleRepoSnapshots(w http.ResponseWriter, r *http.Request) {
	repoID := pathParam(r, "id")
	repo, err := s.ST.GetRepository(r.Context(), repoID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	snaps, _, err := s.Jobs.Snapshots(r.Context(), repo.ID, repo.AgentID)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snaps)
}

// GET /snapshots/{snapshotID}/tree?repo=&path=
func (s *Server) handleSnapshotTree(w http.ResponseWriter, r *http.Request) {
	snapshotID := pathParam(r, "snapshotID")
	repoID := r.URL.Query().Get("repo")
	path := r.URL.Query().Get("path")
	if repoID == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "repo query parameter is required")
		return
	}
	repo, err := s.ST.GetRepository(r.Context(), repoID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	tree, _, err := s.Jobs.SnapshotTree(r.Context(), repo.ID, repo.AgentID, snapshotID, path)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

// jobsErr maps orchestrator errors (incl. stable codes) to HTTP.
func (s *Server) jobsErr(w http.ResponseWriter, err error) {
	var mt *jobs.MissingToolsError
	if errors.As(err, &mt) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": map[string]any{"code": "missing_tools", "message": "agent lacks required tools", "tools": mt.Tools},
		})
		return
	}
	if errors.Is(err, jobs.ErrWaitTimeout) {
		writeErr(w, http.StatusGatewayTimeout, "wait_timeout",
			"agent did not finish the operation in time; it may still complete - retry or check run logs")
		return
	}
	if mapStoreErr(w, err) {
		return
	}
	if code, ok := errorCode(err); ok {
		writeErr(w, http.StatusUnprocessableEntity, code, redactMsg(err.Error()))
		return
	}
	writeErr(w, http.StatusInternalServerError, "internal", redactMsg(err.Error()))
}
