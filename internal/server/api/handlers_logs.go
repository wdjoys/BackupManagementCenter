package api

import (
	"net/http"
	"strconv"
	"strings"

	"backupmanagementcenter/internal/server/store"
)

// 查询Server进程日志，支持按ID向前分页。
func (s *Server) handleListServerLogs(w http.ResponseWriter, r *http.Request) {
	logStore, ok := s.ST.(store.LogStore)
	if !ok {
		writeErr(w, http.StatusNotImplemented, "logs_unavailable", "process log storage is unavailable")
		return
	}
	beforeID, valid := logCursor(r)
	if !valid {
		writeErr(w, http.StatusBadRequest, "validation_failed", "before_id must be a positive integer")
		return
	}
	logs, err := logStore.ListServerLogs(r.Context(), beforeID, processLogLimit(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// 查询指定Agent进程日志，支持按ID向前分页。
func (s *Server) handleListAgentLogs(w http.ResponseWriter, r *http.Request) {
	agentID := strings.TrimSpace(pathParam(r, "id"))
	if agentID == "" {
		writeErr(w, http.StatusBadRequest, "validation_failed", "agent id is required")
		return
	}
	if _, err := s.ST.GetAgent(r.Context(), agentID); err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	logStore, ok := s.ST.(store.LogStore)
	if !ok {
		writeErr(w, http.StatusNotImplemented, "logs_unavailable", "process log storage is unavailable")
		return
	}
	beforeID, valid := logCursor(r)
	if !valid {
		writeErr(w, http.StatusBadRequest, "validation_failed", "before_id must be a positive integer")
		return
	}
	logs, err := logStore.ListAgentLogs(r.Context(), agentID, beforeID, processLogLimit(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func processLogLimit(r *http.Request) int {
	limit := queryInt(r, "limit", 200)
	if limit <= 0 || limit > 500 {
		return 200
	}
	return limit
}

func logCursor(r *http.Request) (int64, bool) {
	value := strings.TrimSpace(r.URL.Query().Get("before_id"))
	if value == "" {
		return 0, true
	}
	if strings.HasPrefix(value, "-") {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
