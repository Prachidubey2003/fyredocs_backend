// Package routes wires every HTTP path collab-service exposes
// onto a single [http.ServeMux]. main.go calls [Register] once
// at startup; the registered-paths test in this package asserts
// the exact public surface so route additions/removals can't slip
// in silently.
//
// The "standard service contract" routes (`/healthz`, `/readyz`,
// `/metrics`) live here for parity with editor-service. The
// websocket upgrade lives here too — it's wrapped in the auth
// middleware that main.go supplies, so this package never imports
// the verifier directly.
package routes

import (
	"net/http"
	"sync"

	sharedmetrics "fyredocs/shared/metrics"

	"collab-service/handlers"
	"collab-service/internal/presence"
	"collab-service/internal/room"
)

// Options bundles the dependencies the router needs. All fields
// are optional in tests, but production passes a Hub and an auth
// middleware.
type Options struct {
	Hub            *room.Hub
	Bridge         *presence.Bridge
	AllowedOrigins []string
	// AuthMiddleware wraps the websocket route. Nil means no
	// wrapping — only suitable for tests that exercise Connect
	// directly. Production MUST pass the JWT middleware.
	AuthMiddleware func(http.Handler) http.Handler
}

// Register installs every public route on mux. Idempotent only in
// the sense that it doesn't re-register paths — calling twice on
// the same mux will panic (http.ServeMux's contract).
func Register(mux *http.ServeMux, opts Options) {
	mux.HandleFunc("/healthz", Healthz)
	mux.HandleFunc("/readyz", makeReadyz(opts.Hub))
	mux.Handle("/metrics", sharedmetrics.HTTPMetricsHandler())

	var connect http.Handler = handlers.Connect(opts.Hub, opts.AllowedOrigins, opts.Bridge)
	if opts.AuthMiddleware != nil {
		connect = opts.AuthMiddleware(connect)
	}
	mux.Handle("/v1/docs/", connect)
}

// RegisteredPaths is the canonical list of paths the service
// exposes. The corresponding test asserts that Register installs
// exactly this set — if you add a route, add it here too, and the
// test will tell you if you forgot.
//
// Trailing-slash paths are subtree handlers (stdlib net/http
// pattern); the websocket Connect handler validates the exact
// shape `/v1/docs/{id}/connect` itself.
var RegisteredPaths = []string{
	"/healthz",
	"/readyz",
	"/metrics",
	"/v1/docs/",
}

// Healthz is the liveness probe — answers OK if the process is
// up, independent of downstream dependencies. Standard service
// contract per [fyredocs_backend/CLAUDE.md] §6.
func Healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

// readyState lets the readyz probe distinguish "process is
// starting" from "process is running but degraded". For v0 there
// is no real degradation path — Ready is true as soon as the
// router is constructed. Adding NATS / DB / auth-service Pings
// follows the standard pattern: each contributes to the readyz
// payload.
var (
	readyMu sync.Mutex
	ready   = true
)

// SetReady toggles the readyz state. main.go uses this on
// SIGTERM to flip readyz to 503 during graceful shutdown — load
// balancers stop sending traffic before in-flight connections
// finish draining.
func SetReady(v bool) {
	readyMu.Lock()
	ready = v
	readyMu.Unlock()
}

// IsReady reports the current state — exposed for tests that
// need to verify they set it back correctly via t.Cleanup.
func IsReady() bool {
	readyMu.Lock()
	defer readyMu.Unlock()
	return ready
}

func makeReadyz(hub *room.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !IsReady() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		rooms := 0
		if hub != nil {
			rooms = hub.RoomCount()
		}
		_, _ = w.Write([]byte(`{"status":"ready","hub":{"rooms":` + itoa(rooms) + `}}`))
	}
}

// itoa is a tiny stdlib-only int formatter. Inlined to keep the
// readyz response writer alloc-free in the hot path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
