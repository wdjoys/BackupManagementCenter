package acme

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// Server provides the plain HTTP ACME HTTP-01 challenge endpoint. It is
// intentionally separate from the authenticated API and should be exposed
// only on port 80 or behind a dedicated TCP mapping.
type Server struct {
	Addr    string
	Webroot string
}

func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/.well-known/acme-challenge/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		if name == "." || name == "/" || name == "" {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(s.Webroot, name)
		if filepath.Dir(path) != filepath.Join(s.Webroot, ".well-known", "acme-challenge") {
			// The path is checked below using the full challenge directory.
		}
		challengePath := filepath.Join(s.Webroot, ".well-known", "acme-challenge", name)
		data, err := os.ReadFile(challengePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/live" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (s Server) Prepare() error {
	return os.MkdirAll(filepath.Join(s.Webroot, ".well-known", "acme-challenge"), 0o755)
}

func (s Server) Validate() error {
	if s.Addr == "" {
		return fmt.Errorf("acme: address is empty")
	}
	if s.Webroot == "" {
		return fmt.Errorf("acme: webroot is empty")
	}
	return nil
}
