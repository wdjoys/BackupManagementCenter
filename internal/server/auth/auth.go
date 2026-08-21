// Package auth implements argon2id password hashing, server-side sessions
// with sliding expiry, cookie handling and CSRF protection.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
)

const (
	SessionCookie = "bmc_session"
	CSRFCookie    = "bmc_csrf"
	CSRFHeader    = "X-CSRF-Token"

	idleTTL    = 12 * time.Hour
	absoluteTL = 7 * 24 * time.Hour
)

// SessionStore is the session-facing slice of store.Store.
type SessionStore interface {
	CreateSession(ctx context.Context, s *model.Session) error
	GetSession(ctx context.Context, idHash string) (*model.Session, error)
	TouchSession(ctx context.Context, idHash string, lastSeen time.Time) error
	DeleteSession(ctx context.Context, idHash string) error
}

// ---- Argon2id ----

const argonMemory = 64 * 1024 // KiB
const argonTime = 2
const argonThreads = 1

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, 32)
	var b strings.Builder
	b.WriteString("$argon2id$v=19$m=65536,t=2,p=1$")
	b.WriteString(base64.RawStdEncoding.EncodeToString(salt))
	b.WriteByte('$')
	b.WriteString(base64.RawStdEncoding.EncodeToString(h))
	return b.String(), nil
}

func VerifyPassword(password, phc string) bool {
	parts := strings.Split(phc, "$")
	// "", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := parseArgonParams(parts[3], &m, &t, &p); err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func parseArgonParams(s string, m *uint32, t *uint32, p *uint8) (bool, error) {
	for _, kv := range strings.Split(s, ",") {
		switch {
		case strings.HasPrefix(kv, "m="):
			n, ok := parseUint32(kv[2:])
			if !ok {
				return false, errors.New("bad m")
			}
			*m = n
		case strings.HasPrefix(kv, "t="):
			n, ok := parseUint32(kv[2:])
			if !ok {
				return false, errors.New("bad t")
			}
			*t = n
		case strings.HasPrefix(kv, "p="):
			n, ok := parseUint32(kv[2:])
			if !ok || n > 255 {
				return false, errors.New("bad p")
			}
			*p = uint8(n)
		}
	}
	return true, nil
}

func parseUint32(s string) (uint32, bool) {
	if s == "" {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
		if n > 0xFFFFFFFF {
			return 0, false
		}
	}
	return uint32(n), true
}

// ---- Sessions ----

var ErrInvalidCredentials = errors.New("auth: invalid credentials")
var ErrSessionExpired = errors.New("auth: session expired")

// Login verifies credentials and issues a session, returning the bearer
// token (only its hash is persisted).
func Login(ctx context.Context, st SessionStore, getAdmin func(ctx context.Context, username string) (*model.Admin, error), updateLogin func(ctx context.Context, id string, at time.Time) error, username, password string) (token string, admin *model.Admin, err error) {
	admin, err = getAdmin(ctx, username)
	if err != nil {
		return "", nil, ErrInvalidCredentials
	}
	if !VerifyPassword(password, admin.PasswordHash) {
		return "", nil, ErrInvalidCredentials
	}
	token, err = newToken()
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()
	s := &model.Session{
		IDHash:     secrets.HashToken(token),
		AdminID:    admin.ID,
		ExpiresAt:  now.Add(absoluteTL),
		CreatedAt:  now,
		LastSeenAt: now,
	}
	if err := st.CreateSession(ctx, s); err != nil {
		return "", nil, err
	}
	if err := updateLogin(ctx, admin.ID, now); err != nil {
		return "", nil, err
	}
	return token, admin, nil
}

// Validate resolves the current session from the request cookie, applying
// sliding idle TTL.
func Validate(ctx context.Context, st SessionStore, r *http.Request) (*model.Session, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return nil, ErrSessionExpired
	}
	s, err := st.GetSession(ctx, secrets.HashToken(c.Value))
	if err != nil {
		return nil, ErrSessionExpired
	}
	now := time.Now().UTC()
	if now.After(s.ExpiresAt) || now.Sub(s.LastSeenAt) > idleTTL {
		_ = st.DeleteSession(ctx, s.IDHash)
		return nil, ErrSessionExpired
	}
	if now.Sub(s.LastSeenAt) > time.Minute {
		_ = st.TouchSession(ctx, s.IDHash, now)
	}
	return s, nil
}

func Logout(ctx context.Context, st SessionStore, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		_ = st.DeleteSession(ctx, secrets.HashToken(c.Value))
	}
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ---- Cookies ----

func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		Expires:  expires,
	})
}

func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   -1,
	})
}

func SetCSRFCookie(w http.ResponseWriter, r *http.Request) (csrf string) {
	csrf, _ = newToken()
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookie,
		Value:    csrf,
		Path:     "/",
		HttpOnly: false, // JS must read it to echo X-CSRF-Token
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		Expires:  time.Now().Add(absoluteTL),
	})
	return csrf
}

// CheckCSRF validates the double-submit token plus same-origin for unsafe
// methods.
func CheckCSRF(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		host := r.Host
		if !sameOrigin(origin, host) {
			return false
		}
	}
	c, err := r.Cookie(CSRFCookie)
	if err != nil || c.Value == "" {
		return false
	}
	h := r.Header.Get(CSRFHeader)
	if h == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(h)) == 1
}

func sameOrigin(origin, host string) bool {
	o := origin
	o = strings.TrimPrefix(o, "https://")
	o = strings.TrimPrefix(o, "http://")
	o = strings.TrimSuffix(o, "/")
	if i := strings.Index(o, "/"); i >= 0 {
		o = o[:i]
	}
	return o == host
}
