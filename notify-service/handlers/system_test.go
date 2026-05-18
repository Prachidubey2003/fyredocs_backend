package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"fyredocs/shared/natsconn"

	"notify-service/internal/models"
)

func newSystemRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/healthz", HealthCheck)
	r.GET("/readyz", ReadyCheck)
	return r
}

// readyResponse is the JSON shape the tests parse.
type readyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func TestHealthCheck_AlwaysOK(t *testing.T) {
	r := newSystemRouter()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", w.Body.String())
	}
}

func TestReadyCheck_HappyPath(t *testing.T) {
	_, _, restore := setupTest(t) // sets up models.DB
	defer restore()
	SetNATSCheckForTest(natsconn.StubHealthChecker{Verdict: natsconn.StatusOK})
	defer SetNATSCheckForTest(nil)

	r := newSystemRouter()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp readyResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "ready" {
		t.Errorf("status = %q, want ready", resp.Status)
	}
	if resp.Checks["postgres"] != "ok" {
		t.Errorf("postgres check = %q", resp.Checks["postgres"])
	}
	if resp.Checks["nats"] != "ok" {
		t.Errorf("nats check = %q", resp.Checks["nats"])
	}
}

func TestReadyCheck_FailsWhenNATSDisconnected(t *testing.T) {
	// NATS being down means the fanout consumer + the legacy
	// notify.send.> consumer both go silent. Operators want
	// /readyz to surface this so K8s rolls back instead of
	// the pod looking healthy while events back up.
	_, _, restore := setupTest(t)
	defer restore()
	SetNATSCheckForTest(natsconn.StubHealthChecker{Verdict: natsconn.StatusDisconnected})
	defer SetNATSCheckForTest(nil)

	r := newSystemRouter()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var resp readyResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "not ready" {
		t.Errorf("status = %q, want not ready", resp.Status)
	}
	if resp.Checks["nats"] != "disconnected" {
		t.Errorf("nats check = %q, want disconnected", resp.Checks["nats"])
	}
	// Postgres should still report ok — the failure is isolated to nats.
	if resp.Checks["postgres"] != "ok" {
		t.Errorf("postgres check = %q, want ok", resp.Checks["postgres"])
	}
}

func TestReadyCheck_NATSDisabledReportsDisabledNotFailed(t *testing.T) {
	// main.go allows running without NATS (HTTP-only mode).
	// /readyz should report `nats: disabled` rather than
	// failing — operators on a deliberately-pruned deploy
	// don't want false negatives.
	_, _, restore := setupTest(t)
	defer restore()
	// nil checker → production adapter path. natsconn.Conn
	// is nil in the test binary (no Connect call), so the
	// adapter reports "not configured".
	SetNATSCheckForTest(nil)

	r := newSystemRouter()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (NATS-disabled is allowed); body=%s", w.Code, w.Body.String())
	}
	var resp readyResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Checks["nats"] != "disabled" {
		t.Errorf("nats check = %q, want disabled", resp.Checks["nats"])
	}
}

func TestReadyCheck_FailsWhenPostgresDown(t *testing.T) {
	// Drive `SELECT 1` to fail by closing the test DB BEFORE
	// the request fires. We swap to a deliberately-broken DB
	// rather than mocking — the gorm error path is part of
	// the contract under test.
	_, _, restore := setupTest(t)
	defer restore()

	// Force an error by closing the underlying connection.
	sqlDB, _ := models.DB.DB()
	_ = sqlDB.Close()

	SetNATSCheckForTest(natsconn.StubHealthChecker{Verdict: natsconn.StatusOK})
	defer SetNATSCheckForTest(nil)

	r := newSystemRouter()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var resp readyResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Checks["postgres"] == "ok" {
		t.Errorf("postgres check should report error; got %q", resp.Checks["postgres"])
	}
}
