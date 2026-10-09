package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/jobs"
	"backupmanagementcenter/internal/server/store"
)

// ---- Storage targets ----

type storageTargetView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	RemoteName string `json:"remote_name"`
	RemotePath string `json:"remote_path"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
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
	ID                string  `json:"id"`
	AgentID           string  `json:"agent_id"`
	AgentName         string  `json:"agent_name,omitempty"`
	StorageTargetID   string  `json:"storage_target_id"`
	StorageTargetName string  `json:"storage_target_name,omitempty"`
	RepositoryPath    string  `json:"repository_path"`
	Status            string  `json:"status"`
	LastCheckAt       *string `json:"last_check_at,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
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

// GET /repositories/{id}/snapshots — 优先使用缓存的同步浏览。
func (s *Server) handleRepoSnapshots(w http.ResponseWriter, r *http.Request) {
	repoID := pathParam(r, "id")
	repo, err := s.ST.GetRepository(r.Context(), repoID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	if !refresh && r.URL.Query().Get("cached") == "1" {
		cs, hasCache := s.ST.(store.SnapshotCacheStore)
		if !hasCache {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		cached, stale, err := cs.GetSnapshotListBrowseCache(r.Context(), repo.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeErr(w, http.StatusInternalServerError, "internal", redactMsg(err.Error()))
			return
		}
		var snaps []model.Snapshot
		if err := json.Unmarshal([]byte(cached.SnapshotsJSON), &snaps); err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if store.SnapshotFingerprint(snaps) != cached.Fingerprint {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		snaps = s.Jobs.FilterHiddenSnapshots(r.Context(), repo.ID, snaps)
		if snaps == nil {
			snaps = []model.Snapshot{}
		}
		if stale {
			w.Header().Set("X-BMC-Cache", "STALE")
		} else {
			w.Header().Set("X-BMC-Cache", "HIT")
		}
		if !cached.VerifiedAt.IsZero() {
			w.Header().Set("X-BMC-Verified-At", cached.VerifiedAt.UTC().Format(timeRFC3339))
		}
		writeJSON(w, http.StatusOK, snaps)
		return
	}
	snaps, _, cacheInfo, err := s.Jobs.SnapshotsWithOptions(r.Context(), repo.ID, repo.AgentID, refresh)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	writeSnapshotCacheHeaders(w, cacheInfo)
	writeJSON(w, http.StatusOK, snaps)
}

// DELETE /repositories/{id}/snapshots/{snapshotID} 请求手动删除单个快照。
// HTTP 层不等待 Agent，也不直接调用 restic；后台由 scheduler 按仓库 FIFO
// 处理真正的 forget + prune。响应 202 后立即从列表隐藏。
func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	repoID := pathParam(r, "id")
	snapshotID := pathParam(r, "snapshotID")

	repo, err := s.ST.GetRepository(r.Context(), repoID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}

	// 要求当前 repository 存在有效的快照列表缓存，并确认该 snapshotID 在缓存中；
	// 避免对任意 ID 发起破坏性命令。
	cs, hasCache := s.ST.(store.SnapshotCacheStore)
	if !hasCache {
		writeErr(w, http.StatusConflict, "snapshot_list_refresh_required",
			"snapshot list cache unavailable; refresh the list first")
		return
	}
	cached, err := cs.GetSnapshotListCache(r.Context(), repoID)
	if err != nil {
		writeErr(w, http.StatusConflict, "snapshot_list_refresh_required",
			"snapshot list cache is missing or stale; refresh the list first")
		return
	}
	var snaps []model.Snapshot
	if json.Unmarshal([]byte(cached.SnapshotsJSON), &snaps) != nil {
		writeErr(w, http.StatusConflict, "snapshot_list_refresh_required",
			"snapshot list cache is corrupt; refresh the list first")
		return
	}
	if store.SnapshotFingerprint(snaps) != cached.Fingerprint {
		writeErr(w, http.StatusConflict, "snapshot_list_refresh_required",
			"snapshot list cache fingerprint mismatch; refresh the list first")
		return
	}
	found := false
	for _, snap := range snaps {
		if snap.ID == snapshotID {
			found = true
			break
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, "not_found", "snapshot not found in repository")
		return
	}

	del, _, err := s.Jobs.QueueSnapshotDeletion(r.Context(), actorID(r), repo.ID, snapshotID)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"deletion_id": del.ID,
		"status":      string(del.State),
	})
}

// GET /snapshots/{snapshotID}/tree?repo=&path=&refresh=1
func (s *Server) handleSnapshotTree(w http.ResponseWriter, r *http.Request) {
	snapshotID := pathParam(r, "snapshotID")
	repoID := r.URL.Query().Get("repo")
	cachePath := r.URL.Query().Get("path")
	if repoID == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "repo query parameter is required")
		return
	}
	repo, err := s.ST.GetRepository(r.Context(), repoID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	if !refresh && r.URL.Query().Get("cached") == "1" {
		cs, hasCache := s.ST.(store.SnapshotCacheStore)
		if !hasCache {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		listCached, listStale, err := cs.GetSnapshotListBrowseCache(r.Context(), repo.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeErr(w, http.StatusInternalServerError, "internal", redactMsg(err.Error()))
			return
		}
		var snaps []model.Snapshot
		if err := json.Unmarshal([]byte(listCached.SnapshotsJSON), &snaps); err != nil || store.SnapshotFingerprint(snaps) != listCached.Fingerprint {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		filteredSnaps := s.Jobs.FilterHiddenSnapshots(r.Context(), repo.ID, snaps)
		found := false
		for _, snap := range filteredSnaps {
			if snap.ID == snapshotID {
				found = true
				break
			}
		}
		if !found {
			writeErr(w, http.StatusNotFound, "not_found", "snapshot not found in repository")
			return
		}

		treeCached, treeStale, err := cs.GetSnapshotTreeBrowseCache(r.Context(), repo.ID, snapshotID, cachePath)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeErr(w, http.StatusInternalServerError, "internal", redactMsg(err.Error()))
			return
		}
		var tree jobs.TreeResult
		if err := json.Unmarshal([]byte(treeCached.TreeJSON), &tree); err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if listStale || treeStale {
			w.Header().Set("X-BMC-Cache", "STALE")
		} else {
			w.Header().Set("X-BMC-Cache", "HIT")
		}
		if !treeCached.VerifiedAt.IsZero() {
			w.Header().Set("X-BMC-Verified-At", treeCached.VerifiedAt.UTC().Format(timeRFC3339))
		}
		writeJSON(w, http.StatusOK, tree)
		return
	}
	tree, _, cacheInfo, err := s.Jobs.SnapshotTreeWithOptions(r.Context(), repo.ID, repo.AgentID, snapshotID, cachePath, refresh)
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	writeSnapshotCacheHeaders(w, cacheInfo)
	writeJSON(w, http.StatusOK, tree)
}

func writeSnapshotCacheHeaders(w http.ResponseWriter, info jobs.CacheInfo) {
	if info.Hit {
		w.Header().Set("X-BMC-Cache", "HIT")
	} else {
		w.Header().Set("X-BMC-Cache", "MISS")
	}
	if info.VerifiedAt != nil {
		w.Header().Set("X-BMC-Verified-At", info.VerifiedAt.UTC().Format(timeRFC3339))
	}
}

// jobsErr maps orchestrator errors (incl. stable codes) to HTTP.
func (s *Server) jobsErr(w http.ResponseWriter, err error) {
	var mt *jobs.MissingToolsError
	if errors.Is(err, jobs.ErrForbidden) {
		// 覆盖恢复的确认值不匹配等刻意拒绝：属客户端问题，必须是 4xx 而不是 500，
		// 否则运维会把它当成服务端内部错误。文案保持简短（反枚举）。
		writeErr(w, http.StatusForbidden, model.ErrForbidden, "forbidden")
		return
	}
	if errors.Is(err, jobs.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	if errors.Is(err, store.ErrCacheGenerationChanged) {
		writeErr(w, http.StatusConflict, "cache_generation_changed", "repository changed while browsing; retry")
		return
	}
	if errors.Is(err, jobs.ErrRestoreBusy) {
		writeErr(w, http.StatusConflict, model.ErrDatabaseRestoreBusy, "another database restore is in progress; wait for it to finish")
		return
	}
	if errors.Is(err, store.ErrRestoreTargetBusy) {
		writeErr(w, http.StatusConflict, model.ErrRestoreTargetBusy, "another restore is already writing to this target path; wait for it to finish")
		return
	}
	if errors.Is(err, jobs.ErrRestoreConflict) || errors.Is(err, store.ErrRestoreConflict) {
		writeErr(w, http.StatusConflict, "restore_conflict", "restore request is not in a state that can be resolved")
		return
	}
	if errors.Is(err, jobs.ErrCapabilitiesPending) {
		writeErr(w, http.StatusConflict, model.ErrAgentCapabilitiesPending, "target agent connection has not reported its capabilities yet; retry shortly")
		return
	}
	if errors.Is(err, jobs.ErrUnsafeDatabaseRestore) {
		writeErr(w, http.StatusUnprocessableEntity, model.ErrAgentUpgradeRequired, "target agent does not support pre-restore backup and rollback; upgrade it first")
		return
	}
	if errors.Is(err, jobs.ErrSnapshotRefreshRequired) {
		writeErr(w, http.StatusConflict, model.ErrSnapshotListRefreshRequired, "a verified snapshot list or directory cache is required; refresh the snapshot list first")
		return
	}
	if errors.Is(err, jobs.ErrSnapshotKindMismatch) {
		writeErr(w, http.StatusUnprocessableEntity, "restore_snapshot_kind_mismatch", "the snapshot kind does not match the requested restore kind")
		return
	}
	if errors.Is(err, jobs.ErrSnapshotRestoreProtected) {
		writeErr(w, http.StatusConflict, model.ErrSnapshotRestoreProtected, "the snapshot backs an unresolved restore and cannot be deleted")
		return
	}
	if errors.Is(err, jobs.ErrUnsupportedManifest) {
		writeErr(w, http.StatusUnprocessableEntity, model.ErrUnsupportedRestoreManifest, "the snapshot is not a single-database artifact")
		return
	}
	if errors.Is(err, jobs.ErrAgentOffline) {
		writeErr(w, http.StatusConflict, model.ErrAgentUnavailable, "target agent is offline")
		return
	}
	if errors.Is(err, jobs.ErrAgentRevoked) {
		writeErr(w, http.StatusConflict, model.ErrAgentRevoked, "target agent is revoked")
		return
	}
	if errors.Is(err, jobs.ErrRepositoryNotReady) {
		writeErr(w, http.StatusConflict, model.ErrRepositoryMissing, "cross-agent restore requires a ready source repository")
		return
	}
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
