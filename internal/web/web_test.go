package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Verifies that a request for the configured path prefix exactly
// (without a trailing slash) is redirected to prefix + "/" — not to "/".
//
// Regression test: previously, http.StripPrefix + ServeMux redirected
// empty paths to "/", which lost the prefix and routed users to the
// wrong upstream in path-based reverse-proxy deployments.
func TestExactPrefixRedirect(t *testing.T) {
	const prefix = "/agents/alpha"

	srv := &Server{pathPrefix: prefix}

	// Wrap a no-op handler with the prefix-redirect logic, the same way
	// Start() does.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	handler := srv.exactPrefixRedirect(inner)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantLoc    string // "" means no redirect expected
	}{
		{"exact prefix gets redirected", "/agents/alpha", http.StatusTemporaryRedirect, "/agents/alpha/"},
		{"prefix with trailing slash passes through", "/agents/alpha/", 200, ""},
		{"prefix subpath passes through", "/agents/alpha/style.css", 200, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantLoc != "" {
				if got := rec.Header().Get("Location"); got != tt.wantLoc {
					t.Errorf("Location: got %q, want %q", got, tt.wantLoc)
				}
			} else {
				if got := rec.Header().Get("Location"); got != "" {
					t.Errorf("unexpected Location header: %q", got)
				}
			}
		})
	}
}

// Verifies that empty path_prefix leaves the request handling untouched
// (no wrapping, no redirect).
func TestExactPrefixRedirectNoPrefix(t *testing.T) {
	srv := &Server{pathPrefix: ""}

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	handler := srv.exactPrefixRedirect(inner)

	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("inner handler was not called")
	}
	if rec.Code != 200 {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
}