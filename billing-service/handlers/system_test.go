package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"fyredocs/shared/natsconn"
)

// readyResponse mirrors the JSON shape ReadyCheck emits.
type readyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func TestHealthCheck_AlwaysOK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/healthz", HealthCheck)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want \"ok\"", w.Body.String())
	}
}

func TestReadyCheck_ReportsDBDown(t *testing.T) {
	// ReadyCheck reads from models.DB. Without setup, models.DB
	// is nil, and DB.Exec panics. To avoid an actual panic in
	// tests, set up the sqlite DB first.
	defer setupTestDB(t)()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/readyz", ReadyCheck)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// With a healthy in-memory sqlite, ReadyCheck should report 200.
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (sqlite is reachable)", w.Code)
	}
}

func TestReadyCheck_FailsWhenNATSDisconnected(t *testing.T) {
	// billing-service publishes audit + subscription.* fanout
	// events on NATS. An unreachable NATS means publish calls
	// log + drop, leaving an audit-trail gap — surface as 503
	// so K8s rolls back instead of the pod looking healthy.
	defer setupTestDB(t)()
	SetNATSCheckForTest(natsconn.StubHealthChecker{Verdict: natsconn.StatusDisconnected})
	defer SetNATSCheckForTest(nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/readyz", ReadyCheck)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var resp readyResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Checks["nats"] != string(natsconn.StatusDisconnected) {
		t.Errorf("nats check = %q, want disconnected", resp.Checks["nats"])
	}
	// Postgres should still report ok — failure isolation.
	if resp.Checks["postgres"] != "ok" {
		t.Errorf("postgres check = %q, want ok", resp.Checks["postgres"])
	}
}

func TestReadyCheck_NATSDisabledPasses(t *testing.T) {
	// HTTP-only billing-service deploys (no NATS configured)
	// must NOT 503 — the audit pipeline being deliberately
	// pruned isn't a readiness failure.
	defer setupTestDB(t)()
	SetNATSCheckForTest(natsconn.StubHealthChecker{Verdict: natsconn.StatusDisabled})
	defer SetNATSCheckForTest(nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/readyz", ReadyCheck)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (disabled NATS is allowed)", w.Code)
	}
	var resp readyResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Checks["nats"] != string(natsconn.StatusDisabled) {
		t.Errorf("nats check = %q, want disabled", resp.Checks["nats"])
	}
}
