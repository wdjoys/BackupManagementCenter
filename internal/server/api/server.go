// Package api wires the HTTP surface of the server: setup/auth, agents,
// storage targets, repositories/snapshots, plans, runs (+WebSocket logs),
// restores, dashboard, health.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/jobs"
	"backupmanagementcenter/internal/server/metrics"
	"backupmanagementcenter/internal/server/store"
)

// Server carries the dependencies of the HTTP layer.
type Server struct {
	ST      store.Store
	Bus     events.Bus
	Met     *metrics.Metrics
	Jobs    *jobs.Orchestrator
	Version string
	// Ready reports overall server readiness for /health/ready.
	Ready func() bool
}

func New(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(requestLog)

	// Health endpoints are unauthenticated.
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/health/ready", func(w http.ResponseWriter, _ *http.Request) {
		if s.Ready == nil || s.Ready() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		http.Error(w, `{"error":{"code":"not_ready","message":"starting"}}`, http.StatusServiceUnavailable)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMiddleware(s))
		// Setup & auth are reachable pre-auth (setup gates itself).
		r.Get("/setup/status", s.handleSetupStatus)
		r.Post("/setup", s.handleSetup)
		r.Group(func(r chi.Router) {
			r.Use(func(next http.Handler) http.Handler {
				return s.requireAuth(next.ServeHTTP)
			})
			r.Get("/dashboard", s.handleDashboard)

			r.Get("/agents", s.handleListAgents)
			r.Delete("/agents/{id}", s.handleRevokeAgent)
			r.Post("/enrollment-tokens", s.handleCreateEnrollmentToken)
			r.Get("/enrollment-tokens", s.handleListEnrollmentTokens)

			r.Post("/storage-targets/validate", s.handleValidateStorageTarget)
			r.Post("/storage-targets", s.handleCreateStorageTarget)
			r.Get("/storage-targets", s.handleListStorageTargets)
			r.Delete("/storage-targets/{id}", s.handleDeleteStorageTarget)

			r.Post("/repositories", s.handleBindRepository)
			r.Get("/repositories", s.handleListRepositories)
			r.Get("/repositories/{id}/snapshots", s.handleRepoSnapshots)
			r.Get("/snapshots/{snapshotID}/tree", s.handleSnapshotTree)

			r.Post("/plans/validate", s.handleValidatePlan)
			r.Get("/plans", s.handleListPlans)
			r.Post("/plans", s.handleCreatePlan)
			r.Get("/plans/{id}", s.handleGetPlan)
			r.Put("/plans/{id}", s.handleUpdatePlan)
			r.Delete("/plans/{id}", s.handleDeletePlan)
			r.Post("/plans/{id}/run", s.handleRunPlan)

			r.Get("/runs", s.handleListRuns)
			r.Get("/runs/{id}", s.handleGetRun)
			r.Get("/runs/{id}/logs", s.handleRunLogs)
			r.Post("/runs/{id}/cancel", s.handleCancelRun)

			r.Post("/restores/dry-run", s.handleDryRunRestore)
			r.Post("/restores", s.handleStartRestore)
			r.Get("/restores", s.handleListRestores)
		})
	})

	r.Handle("/ws/runs/{runID}", wsHandler(s))

	// SPA static files.
	r.Handle("/*", spaHandler())

	return r
}

// ---- helpers ----

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	type e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.NewEncoder(w).Encode(map[string]e{"error": {Code: code, Message: msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)) // rclone.conf can be sizeable
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func pathParam(r *http.Request, name string) string { return chi.URLParam(r, name) }

func queryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

var errBadRequest = errors.New("bad request")

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		logf("%s %s -> %d", r.Method, r.URL.Path, ww.Status())
	})
}
