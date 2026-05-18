// Command collab-service is the multiplayer-session backend for
// the editor. v0 ships a stdlib net/http server with the standard
// health endpoints, a JWT-gated websocket upgrade (`/v1/docs/{id}/connect`),
// an in-memory [room.Hub], Prometheus metrics, and a NATS
// cross-replica bridge.
//
// The Hub is a package var rather than a per-request dependency
// because every connection inside a single process lands on the
// same Go runtime, and the Hub's internal locks already serialise
// cross-room mutations. Horizontal scale-out happens via the NATS
// presence bridge between hubs in different processes — see
// [collab-service/internal/presence] — NOT by sharding the hub
// itself.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"fyredocs/shared/config"
	"fyredocs/shared/natsconn"

	"collab-service/internal/authverify"
	"collab-service/internal/eventbridge"
	"collab-service/internal/metrics"
	"collab-service/internal/persister"
	"collab-service/internal/presence"
	"collab-service/internal/room"
	"collab-service/routes"
)

// Hub is the process-wide registry of multiplayer rooms.
// Persister selection happens in buildPersister(): if
// EDITOR_SERVICE_URL is set, we use the HTTP-backed persister
// that talks to editor-service's `/internal/v1/snapshots`
// endpoints. Otherwise we fall back to Noop — load returns nil,
// save discards, and rooms behave exactly the way they did
// before persistence landed.
var Hub = room.NewHubWithPersister(buildPersister())

// buildPersister chooses the persister implementation based on
// env. Returning Noop on misconfig (rather than failing the
// process) keeps the WS path working in single-process / dev
// deployments; the warning in the logs signals "no durability"
// without taking the room layer down.
func buildPersister() room.Persister {
	url := strings.TrimSpace(os.Getenv("EDITOR_SERVICE_URL"))
	if url == "" {
		slog.Warn("EDITOR_SERVICE_URL unset; running without snapshot persistence")
		return persister.Noop{}
	}
	p, err := persister.NewHTTP(persister.HTTPOptions{BaseURL: url})
	if err != nil {
		slog.Warn("HTTP persister init failed; falling back to Noop",
			"error", err, "url", url)
		return persister.Noop{}
	}
	slog.Info("collab snapshot persistence enabled", "url", url)
	return p
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	// Hand the Hub to the rooms-total gauge so /metrics scrapes
	// see live counts without a separate tracking path. The
	// connections gauge + broadcast-bytes counter are wired
	// inside handlers/connect.go.
	metrics.Bind(Hub)

	// Optional NATS cross-replica fan-out. If NATS is unreachable
	// (e.g., single-replica local dev), the bridge stays nil and
	// the service runs in single-replica mode — every other piece
	// of the WS path still works.
	bridge := buildPresenceBridge()

	// Inbound event bridge — forwards editor-service's
	// `editor.comments.<docID>` JSON events into local rooms as
	// WS frames so the frontend's CommentsList updates live.
	// buildPresenceBridge already called natsconn.Connect; we
	// re-use the established connection here.
	subscribeCommentEvents()

	mux := http.NewServeMux()
	routes.Register(mux, routes.Options{
		Hub:            Hub,
		Bridge:         bridge,
		AllowedOrigins: parseAllowedOrigins(os.Getenv("COLLAB_ALLOWED_ORIGINS")),
		AuthMiddleware: buildAuthMiddleware(),
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8091" // plan §4.3.1 reserves :8091 for collab-service
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// Long IdleTimeout so eventual websocket upgrades stay
		// open; today these are read-only health probes but we
		// set the number once so the connection-pumping handlers
		// don't have to wrestle with it later.
		IdleTimeout: 5 * time.Minute,
	}

	go func() {
		slog.Info("collab-service listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("shutting down")

	// Flip readyz to 503 BEFORE draining so load balancers stop
	// sending new traffic. Existing connections keep flowing
	// until srv.Shutdown returns or the timeout fires.
	routes.SetReady(false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown failed", "error", err)
	}
}

// subscribeCommentEvents installs the inbound subscription that
// turns editor-service's `editor.comments.<docID>` events into
// WS frames on local rooms. No-op if NATS isn't connected (the
// presence bridge would have already logged the warning) — the
// service still works, comments just won't update live.
func subscribeCommentEvents() {
	if natsconn.Conn == nil {
		return
	}
	br := eventbridge.NewBridge(&eventbridgeHubAdapter{Hub})
	_, err := natsconn.Conn.Subscribe(eventbridge.CommentEventsSubscription, func(msg *nats.Msg) {
		br.Receive(msg.Subject, msg.Data)
	})
	if err != nil {
		slog.Warn("eventbridge subscription failed", "error", err)
		return
	}
	slog.Info("eventbridge subscribed", "subject", eventbridge.CommentEventsSubscription)
}

// buildPresenceBridge tries to connect to NATS and install a
// subscription that delivers cross-replica frames into local
// rooms. Returns nil on any failure — the service runs fine in
// single-replica mode; missing NATS just means no cross-replica
// fan-out, exactly the same way editor-service handles a missing
// NATS for its EDIT_EVENTS stream.
func buildPresenceBridge() *presence.Bridge {
	if err := natsconn.Connect(); err != nil {
		slog.Warn("NATS unavailable; collab-service running in single-replica mode", "error", err)
		return nil
	}
	bridge, err := presence.NewBridge(natsconn.Conn.Publish, &hubAdapter{Hub})
	if err != nil {
		slog.Warn("presence bridge init failed", "error", err)
		return nil
	}
	_, err = natsconn.Conn.Subscribe(presence.SubjectWildcard, func(msg *nats.Msg) {
		bridge.Receive(msg.Subject, msg.Data)
	})
	if err != nil {
		slog.Warn("presence subscription failed", "error", err)
		return nil
	}
	slog.Info("presence bridge subscribed", "subject", presence.SubjectWildcard)
	return bridge
}

// hubAdapter satisfies presence.Hub on top of *room.Hub. The
// indirection exists because presence.Hub returns an interface
// (presence.Room) while *room.Hub returns a concrete *room.Room —
// Go's type system requires the explicit cast. Also guards
// against the typed-nil pitfall (`var r *room.Room; return r` as
// a presence.Room would be non-nil interface holding nil pointer).
type hubAdapter struct{ h *room.Hub }

func (h *hubAdapter) FindRoom(docID string) presence.Room {
	r := h.h.Find(docID)
	if r == nil {
		return nil
	}
	return r
}

// eventbridgeHubAdapter is the same shape as hubAdapter but
// returns eventbridge.Room. Go's nominal typing means two
// interfaces with identical method sets are still distinct
// types, so we need a separate adapter — they're both four
// lines, no clever generic helper buys anything here.
type eventbridgeHubAdapter struct{ h *room.Hub }

func (h *eventbridgeHubAdapter) FindRoom(docID string) eventbridge.Room {
	r := h.h.Find(docID)
	if r == nil {
		return nil
	}
	return r
}

// buildAuthMiddleware constructs the auth wrapper applied to the
// /v1/docs/ route. Returns a no-op verifier if JWT env config is
// missing — sufficient for local dev (where AUTH_TRUST_GATEWAY_HEADERS
// is usually true and the gateway is the actual verifier), but
// the service logs a loud warning so production misconfig is
// obvious in the logs.
//
// Redis-backed denylist is intentionally NOT wired here yet: the
// collab-service has no other Redis dependency, and the denylist
// is a defense-in-depth feature on top of a gateway that already
// checks it. Adding Redis lands when we wire other Redis-backed
// features (presence pubsub, idempotency cache).
func buildAuthMiddleware() func(http.Handler) http.Handler {
	verifier, err := authverify.NewVerifierFromEnv(nil)
	if err != nil {
		slog.Warn("auth verifier init failed; collab-service will reject all unauthenticated requests",
			"error", err)
		// Fall through: the nil Verifier makes the middleware
		// fail closed — better than fail open when env is
		// misconfigured.
	}

	trustGateway := config.GetEnvBool("AUTH_TRUST_GATEWAY_HEADERS", false)

	opts := authverify.MiddlewareOptions{
		Verifier:              verifier,
		TrustGatewayHeaders:   trustGateway,
		AccessTokenCookieName: os.Getenv("AUTH_ACCESS_COOKIE_NAME"),
		AccessTokenQueryParam: os.Getenv("AUTH_ACCESS_QUERY_PARAM"),
	}

	return func(next http.Handler) http.Handler {
		return authverify.Middleware(opts, next)
	}
}

// parseAllowedOrigins reads a comma-separated origin list from
// the env var. Empty / unset means "accept any origin", which is
// only safe for dev — deployment MUST pass COLLAB_ALLOWED_ORIGINS
// (e.g. `https://app.fyredocs.com,https://staging.fyredocs.com`).
func parseAllowedOrigins(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
