package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHashPasswordRoundtrip(t *testing.T) {
	h, err := HashPassword("s3cret-π")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("s3cret-π", h) {
		t.Fatal("verify failed for correct password")
	}
	if VerifyPassword("wrong", h) {
		t.Fatal("verify passed for wrong password")
	}
}

func TestCheckCSRF(t *testing.T) {
	// GET passes without token.
	r := httptest.NewRequest("GET", "http://x/api", nil)
	if !CheckCSRF(r) {
		t.Fatal("GET should pass")
	}
	// POST without cookie/header fails.
	r = httptest.NewRequest("POST", "http://x/api", nil)
	if CheckCSRF(r) {
		t.Fatal("POST without csrf should fail")
	}
	// Matching cookie+header passes; same-origin ok.
	r = httptest.NewRequest("POST", "http://x/api", nil)
	r.Host = "x"
	r.Header.Set("Origin", "http://x")
	r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: "tok"})
	r.Header.Set(CSRFHeader, "tok")
	if !CheckCSRF(r) {
		t.Fatal("matching csrf should pass")
	}
	// Cross-origin fails.
	r.Header.Set("Origin", "http://evil")
	if CheckCSRF(r) {
		t.Fatal("cross-origin should fail")
	}
}

func TestWriteErrShape(t *testing.T) {
	w := httptest.NewRecorder()
	writeErr(w, 403, "forbidden", "no \"way\"")
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error.Code != "forbidden" || out.Error.Message != `no "way"` {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}
