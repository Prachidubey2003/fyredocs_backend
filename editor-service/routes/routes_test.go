package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// TestRoutes_Healthz checks the liveness endpoint without standing up a DB.
// /healthz must remain dependency-free per the standard service contract.
func TestRoutes_Healthz(t *testing.T) {
	r := gin.New()
	SetupRouter(r, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}

// TestRoutes_ReadyzReportsDBNotInitialized verifies that /readyz fails fast
// (and returns a structured body) when the DB hasn't been wired. Tests run
// before any models.Connect() call, so this is the expected state.
func TestRoutes_ReadyzReportsDBNotInitialized(t *testing.T) {
	r := gin.New()
	SetupRouter(r, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestRoutes_RegisteredPaths(t *testing.T) {
	r := gin.New()
	SetupRouter(r, nil, nil)

	want := []struct {
		method, path string
	}{
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/readyz"},
		{http.MethodPost, "/v1/documents"},
		{http.MethodGet, "/v1/documents"},
		{http.MethodGet, "/v1/documents/:id"},
		{http.MethodDelete, "/v1/documents/:id"},
		{http.MethodPost, "/v1/documents/:id/edit"},
		{http.MethodGet, "/v1/documents/:id/download"},
		{http.MethodGet, "/v1/documents/:id/revisions"},
		{http.MethodGet, "/v1/documents/:id/revisions/:revId/download"},
		{http.MethodPost, "/v1/documents/:id/revisions/:revId/restore"},
		{http.MethodGet, "/v1/documents/:id/spdom"},
		{http.MethodPost, "/v1/documents/:id/comments"},
		{http.MethodGet, "/v1/documents/:id/comments"},
		{http.MethodPost, "/v1/documents/:id/comments/:commentId/resolve"},
	}

	have := map[string]bool{}
	for _, ri := range r.Routes() {
		have[ri.Method+" "+ri.Path] = true
	}
	for _, w := range want {
		key := w.method + " " + w.path
		if !have[key] {
			t.Errorf("missing route: %s", key)
		}
	}
}
