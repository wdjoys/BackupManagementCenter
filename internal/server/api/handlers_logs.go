package api

import (
	"net/http"
	"strconv"
	"strings"

	"backupmanagementcenter/internal/server/store"
)

// 查询Server进程日志，支持before_id、limit、level和type筛选。
func (s *Server) handleListServerLogs(w http.ResponseWriter, r *http.Request) {
	logStore, ok := s.ST.(store.LogStore)
	if !ok {
		writeErr(w, http.StatusNotImplemented, "logs_unavailable", "process log storage is unavailable")
		return
	}
	filter, valid := processLogFilter(r)
	if !valid {
		writeErr(w, http.StatusBadRequest, "validation_failed", "invalid log cursor, level, or type")
		return
	}
	logs, err := logStore.ListServerLogs(r.Context(), filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// 查询指定Agent进程日志，支持before_id、limit、level和type筛选。
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
	filter, valid := processLogFilter(r)
	if !valid {
		writeErr(w, http.StatusBadRequest, "validation_failed", "invalid log cursor, level, or type")
		return
	}
	logs, err := logStore.ListAgentLogs(r.Context(), agentID, filter)
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
var processLogLevels = map[string]struct{}{
	"debug": {},
	"info":  {},
	"warn":  {},
	"error": {},
}

var processLogTypes = map[string]struct{}{
	"system":       {},
	"http":         {},
	"agent":        {},
	"run":          {},
	"scheduler":    {},
	"dispatcher":   {},
	"connection":  {},
	"command":     {},
	"notification": {},
}

func processLogFilter(r *http.Request) (store.ProcessLogFilter, bool) {
	beforeID, valid := logCursor(r)
	if !valid {
		return store.ProcessLogFilter{}, false
	}
	levels, valid := processLogValues(r, "level", processLogLevels)
	if !valid {
		return store.ProcessLogFilter{}, false
	}
	types, valid := processLogValues(r, "type", processLogTypes)
	if !valid {
		return store.ProcessLogFilter{}, false
	}
	return store.ProcessLogFilter{
		BeforeID: beforeID,
		Limit:    processLogLimit(r),
		Levels:   levels,
		Types:    types,
	}, true
}

func processLogValues(r *http.Request, name string, allowed map[string]struct{}) ([]string, bool) {
	values := r.URL.Query()[name]
	if len(values) == 0 {
		return nil, true
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(values))
	for _, raw := range values {
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(strings.ToLower(value))
			if value == "" {
				continue
			}
			if _, ok := allowed[value]; !ok {
				return nil, false
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out, true
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
