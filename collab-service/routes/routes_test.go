package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"collab-service/internal/room"
)

func TestRegister_InstallsAllAdvertisedPaths(t *testing.T) {
	// Belt-and-braces check: every entry in RegisteredPaths
	// must yield a non-404 response after Register installs it.
	// Subtree handlers (trailing slash) match on the prefix
	// itself, so probing the literal `/v1/docs/` is enough.
	mux := http.NewServeMux()
	Register(mux, Options{Hub: room.NewHub()})

	for _, p := range RegisteredPaths {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusNotFound {
				t.Errorf("path %q returned 404 — not registered", p)
			}
		})
	}
}

func TestHealthz_ReturnsOK(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, Options{Hub: room.NewHub()})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "ok")
	}
}

func TestReadyz_ReportsHubRooms(t *testing.T) {
	hub := room.NewHub()
	mux := http.NewServeMux()
	Register(mux, Options{Hub: hub})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"rooms":0`) {
		t.Errorf("readyz body = %q, want rooms:0", rec.Body.String())
	}
}

func TestReadyz_503WhenNotReady(t *testing.T) {
	SetReady(false)
	t.Cleanup(func() { SetReady(true) })

	mux := http.NewServeMux()
	Register(mux, Options{Hub: room.NewHub()})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if IsReady() {
		t.Error("IsReady() = true after SetReady(false)")
	}
}

func TestAuthMiddleware_AppliedToConnectRoute(t *testing.T) {
	// If we provide an auth middleware that always rejects, the
	// /v1/docs/ route must 401 rather than reach Connect.
	rejectAll := func(_ http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no", http.StatusUnauthorized)
		})
	}
	mux := http.NewServeMux()
	Register(mux, Options{Hub: room.NewHub(), AuthMiddleware: rejectAll})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (middleware not applied?)", rec.Code)
	}
}

func TestItoa_EdgeCases(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{-1, "-1"},
		{-1234567890, "-1234567890"},
	}
	for _, tc := range cases {
		if got := itoa(tc.in); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
