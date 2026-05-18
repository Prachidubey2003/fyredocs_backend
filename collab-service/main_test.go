package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"collab-service/internal/authverify"
	"collab-service/routes"
)

// TestAuthMiddleware_GatesConnectEndpoint exercises the actual
// main-package wiring (buildAuthMiddleware + routes.Register).
// Unit tests for individual paths live in routes/routes_test.go;
// this test asserts the integration produces the right behaviour
// end-to-end: an unauthenticated GET to /v1/docs/foo/connect is
// rejected with 401 by the middleware before the upgrade handler
// runs.
func TestAuthMiddleware_GatesConnectEndpoint(t *testing.T) {
	t.Setenv("JWT_HS256_SECRET", "test-secret")

	mux := http.NewServeMux()
	routes.Register(mux, routes.Options{
		Hub:            Hub,
		AuthMiddleware: buildAuthMiddleware(),
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Plain GET (no upgrade) to /v1/docs/foo/connect — the
	// middleware should 401 before the handler runs.
	resp, err := http.Get(srv.URL + "/v1/docs/foo/connect")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (gated by middleware)", resp.StatusCode)
	}

	// Sanity: with gateway-trust mode + X-User-ID we should get
	// past the middleware. The upgrade will fail (we didn't send
	// a real WS handshake) but the failure mode is "bad request"
	// from the upgrader, not "unauthorized" from the middleware.
	t.Setenv("AUTH_TRUST_GATEWAY_HEADERS", "true")
	mux2 := http.NewServeMux()
	routes.Register(mux2, routes.Options{
		Hub:            Hub,
		AuthMiddleware: buildAuthMiddleware(),
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()

	req, _ := http.NewRequest(http.MethodGet, srv2.URL+"/v1/docs/foo/connect", nil)
	req.Header.Set("X-User-ID", "user-1")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == http.StatusUnauthorized {
		t.Errorf("got 401 with gateway header set — middleware should have admitted the request")
	}
}

// Compile-time use of authverify so the import isn't dead if the
// integration test above ever gets refactored. (FromContext is
// the package's public surface that handlers use to read the
// auth identity off the request context.)
var _ = authverify.FromContext
