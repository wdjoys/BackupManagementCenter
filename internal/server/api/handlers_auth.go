package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/auth"
	"backupmanagementcenter/internal/server/store"
)

// authMiddleware resolves sessions into context.
func authMiddleware(s *Server) func(http.Handler) http.Handler {
	return auth.Middleware(s.ST)
}

// requireAuth rejects unauthenticated requests and enforces CSRF.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.AdminFromContext(r.Context()); !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			auth.RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next(w, r) })).ServeHTTP(w, r)
			return
		}
		next(w, r)
	}
}

func requireCSRF(next http.Handler) http.Handler { return auth.RequireCSRF(next) }

// GET /setup/status
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	has, err := s.ST.HasAdmin(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"initialized": has})
}

// POST /setup — creates the one and only admin; 404 afterwards forever.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	has, err := s.ST.HasAdmin(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if has {
		writeErr(w, http.StatusNotFound, "not_found", "setup already completed")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Username) < 3 || len(body.Password) < 10 {
		writeErr(w, http.StatusBadRequest, "validation_failed", "username >= 3 chars, password >= 10 chars")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	admin := &model.Admin{ID: newUUID(), Username: body.Username, PasswordHash: hash, CreatedAt: time.Now().UTC()}
	if err := s.ST.CreateAdmin(r.Context(), admin); err != nil {
		writeErr(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	s.Jobs.Audit(r.Context(), "admin", admin.ID, "setup.create_admin", "admin", admin.ID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// POST /auth/login — sets session + csrf cookies.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	token, admin, err := auth.Login(
		r.Context(),
		s.ST,
		s.ST.GetAdminByUsername,
		func(ctx context.Context, id string, at time.Time) error {
			return s.ST.UpdateAdminLastLogin(ctx, id, at)
		},
		body.Username, body.Password,
	)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "wrong username or password")
		return
	}
	csrf := auth.SetCSRFCookie(w, r)
	auth.SetSessionCookie(w, r, token, time.Now().Add(7*24*time.Hour))
	s.Jobs.Audit(r.Context(), "admin", admin.ID, "auth.login", "admin", admin.ID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"username": admin.Username, "csrf": csrf})
}

// POST /auth/logout
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	auth.Logout(r.Context(), s.ST, r)
	auth.ClearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// GET /auth/me
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	a, ok := auth.AdminFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	admin, err := s.ST.GetAdminByUsername(r.Context(), a.Username)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "admin gone")
		return
	}
	if err != nil {
		// Context only carries ID; fall back to ID-only response.
		writeJSON(w, http.StatusOK, map[string]string{"id": a.ID})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": admin.ID, "username": admin.Username})
}

// mapStoreErr converts domain errors into HTTP responses; returns true when
// handled.
func mapStoreErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, store.ErrInUse):
		writeErr(w, http.StatusConflict, "conflict", "resource still referenced")
	case errors.Is(err, store.ErrDuplicateRun):
		writeErr(w, http.StatusConflict, "duplicate_slot", "run already queued for this slot")
	case errors.Is(err, store.ErrInvalidTransition):
		writeErr(w, http.StatusConflict, "invalid_transition", "run state transition not allowed")
	case errors.Is(err, store.ErrTokenInvalid):
		writeErr(w, http.StatusBadRequest, "invalid_token", "enrollment token invalid or expired")
	case errors.Is(err, store.ErrAdminExists):
		writeErr(w, http.StatusConflict, "conflict", "admin already exists")
	default:
		return false
	}
	return true
}
