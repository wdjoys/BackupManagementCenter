package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/store"
)

func runView(run *model.Run) *model.Run {
	if run == nil {
		return nil
	}
	cp := *run
	if cp.ProgressJSON != "" {
		_ = json.Unmarshal([]byte(cp.ProgressJSON), &cp.Progress)
	}
	cp.ProgressJSON = ""
	return &cp
}

// GET /runs?plan_id&agent_id&status&operation&limit&offset
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	f := model2Filter(r.URL.Query())
	runs, err := s.ST.ListRuns(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]*model.Run, 0, len(runs))
	for i := range runs {
		out = append(out, runView(&runs[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /runs/{id}
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.ST.GetRun(r.Context(), pathParam(r, "id"))
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runView(run))
}

// GET /runs/{id}/logs?before_id=&limit=
// 旧版客户端的 before_seq 被接受但忽略（历史 seq 语义已被 id 取代），仍返回完整倒序首页。
func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request) {
	runID := pathParam(r, "id")
	before := int64(0)
	if v := r.URL.Query().Get("before_id"); v != "" {
		for i := range len(v) {
			if v[i] < '0' || v[i] > '9' {
				writeErr(w, http.StatusBadRequest, "validation_failed", "before_id must be uint")
				return
			}
			before = before*10 + int64(v[i]-'0')
		}
	}
	limit := queryInt(r, "limit", 200)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	logs, err := s.ST.ListRunLogs(r.Context(), runID, before, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 空结果统一序列化为 []（ListRunLogs 无行时返回 nil slice，此前会输出 null，
	// 与 /runs、/logs/server 等接口的 [] 不一致）。
	if logs == nil {
		logs = []model.RunLog{}
	}
	writeJSON(w, http.StatusOK, logs)
}

// POST /runs/{id}/cancel → 202; dispatcher forwards to the agent.
func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	if err := s.Jobs.CancelRun(r.Context(), pathParam(r, "id")); err != nil {
		if !mapStoreErr(w, err) {
			s.jobsErr(w, err)
		}
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// model2Filter builds a RunFilter from query params.
func model2Filter(q url.Values) store.RunFilter {
	get := q.Get
	f := store.RunFilter{
		AgentID: get("agent_id"),
		PlanID:  get("plan_id"),
		// repository_id 此前未映射，被静默忽略：调用方按仓库过滤时拿到的是全局列表，
		// 排障时容易据此得出错误结论（实测切片据此误判"该仓库无保留 run"）。
		RepositoryID: get("repository_id"),
		Operation:    get("operation"),
		Limit:        100,
	}
	if s := get("status"); s != "" {
		f.Statuses = []string{s}
	}
	if v := get("limit"); v != "" {
		n := atoiSafe(v, 100)
		if n > 0 && n <= 500 {
			f.Limit = n
		}
	}
	f.Offset = atoiSafe(get("offset"), 0)
	return f
}

func atoiSafe(s string, def int) int {
	if s == "" {
		return def
	}
	n := 0
	for i := range s {
		if s[i] < '0' || s[i] > '9' {
			return def
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// wsHandler serves GET /ws/runs/{runID}.
func wsHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := strings.TrimSpace(chi.URLParam(r, "runID"))
		if runID == "" || runID == "undefined" || runID == "null" {
			writeErr(w, http.StatusBadRequest, "validation_failed", "run id is required")
			return
		}
		if _, err := s.sessionForWS(r); err != nil {
			http.Error(w, `{"error":{"code":"unauthorized","message":"login required"}}`, http.StatusUnauthorized)
			return
		}
		s.serveRunWS(w, r, runID)
	}
}

var _ = events.State
