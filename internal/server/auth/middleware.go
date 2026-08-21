package auth

import (
	"context"
	"encoding/json"
	"net/http"

	"backupmanagementcenter/internal/model"
)

type ctxKey int

const adminKey ctxKey = 1

// AdminFromContext returns the authenticated admin, if any.
func AdminFromContext(ctx context.Context) (*model.Admin, bool) {
	a, ok := ctx.Value(adminKey).(*model.Admin)
	return a, ok && a != nil
}

// Middleware resolves the session cookie and injects the admin into context.
// Unauthenticated requests simply proceed without an admin; handlers that
// require one use RequireAdmin.
func Middleware(st SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s, err := Validate(r.Context(), st, r); err == nil {
				// Minimal admin identity for context; username resolved by
				// handlers that need it.
				ctx := context.WithValue(r.Context(), adminKey, &model.Admin{ID: s.AdminID})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAdmin rejects requests without a valid session.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := AdminFromContext(r.Context()); !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCSRF enforces CSRF + same-origin on unsafe methods.
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !CheckCSRF(r) {
			writeErr(w, http.StatusForbidden, "forbidden", "csrf/origin check failed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":` + jsonString(msg) + `}}`))
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
