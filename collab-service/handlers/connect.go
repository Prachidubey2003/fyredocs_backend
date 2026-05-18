// Package handlers contains HTTP entrypoints for collab-service.
//
// The marquee endpoint is `GET /v1/docs/{id}/connect` — the
// websocket upgrade that bridges editor tabs to a [room.Room].
// JWT verification, rate limiting, and origin policy are layered
// on top via middleware in [routes].
package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"

	"collab-service/internal/metrics"
	"collab-service/internal/presence"
	"collab-service/internal/room"
	"collab-service/internal/wsconn"
)

// Connect builds the websocket handler over the given hub.
//
// `allowedOrigins` is the same-origin allowlist applied during
// the WS upgrade. An empty list means "any origin", which is
// only appropriate for tests/dev — production routes MUST pass a
// concrete list.
//
// `bridge` is the optional NATS cross-replica fan-out. nil means
// single-replica mode (no other collab-service instances exist);
// inbound frames are still broadcast locally, just not forwarded.
//
// The handler:
//  1. Parses the doc id from the path,
//  2. Verifies the Origin header,
//  3. Upgrades the connection,
//  4. Mints a per-connection id (v7-style hex),
//  5. Joins the room,
//  6. Blocks on the read pump until the client disconnects.
func Connect(hub *room.Hub, allowedOrigins []string, bridge *presence.Bridge) http.HandlerFunc {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     originChecker(allowedOrigins),
	}

	return func(w http.ResponseWriter, r *http.Request) {
		docID := docIDFromPath(r.URL.Path)
		if docID == "" {
			http.Error(w, "doc id required", http.StatusBadRequest)
			return
		}

		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrade already wrote the HTTP error.
			slog.Debug("ws upgrade failed", "error", err, "doc", docID)
			return
		}

		connID, err := newConnID()
		if err != nil {
			// Highly unlikely (crypto/rand failure); close the
			// just-opened socket cleanly so the client retries.
			_ = ws.Close()
			slog.Error("conn id generation failed", "error", err)
			return
		}

		room := hub.FindOrCreate(docID)
		conn := wsconn.New(connID, ws)
		room.Join(conn)
		metrics.Connections.Inc()
		defer metrics.Connections.Dec()

		slog.Info("ws connected", "doc", docID, "conn", connID)
		conn.Run(&roomReceiver{room: room, docID: docID, bridge: bridge})
		slog.Info("ws disconnected", "doc", docID, "conn", connID)
	}
}

// roomReceiver bridges wsconn callbacks into the room API. Inbound
// frames become broadcasts (sender-excluded) + an optional NATS
// publish; disconnects become Leave calls. Kept separate from
// wsconn.Conn so the wsconn package has no dependency on room.
type roomReceiver struct {
	room   *room.Room
	docID  string
	bridge *presence.Bridge // optional; nil = single-replica mode
}

func (rr *roomReceiver) OnMessage(senderID string, payload []byte) {
	// Copy the payload — wsconn reuses the underlying read buffer
	// across calls. The room's send fan-out lives past this
	// callback, so we'd otherwise hand out a buffer that's about
	// to be overwritten.
	buf := make([]byte, len(payload))
	copy(buf, payload)
	// Per-recipient bytes: a single inbound frame fans out to
	// (room.Size - 1) peers. Counting at fan-out approximates
	// network bytes leaving the service better than counting
	// inbound bytes, which is the property we actually need for
	// capacity planning.
	peers := rr.room.Size() - 1
	if peers > 0 {
		metrics.BroadcastBytes.Add(float64(len(buf) * peers))
	}
	rr.room.Broadcast(senderID, buf)
	// Cross-replica fan-out: publish to NATS so other replicas
	// holding clients for the same doc can deliver. Log-and-drop
	// on error — losing a frame is recoverable (Yjs re-syncs on
	// reconnect; awareness is heartbeat-driven).
	if rr.bridge != nil {
		if err := rr.bridge.Publish(rr.docID, buf); err != nil {
			slog.Warn("presence publish failed", "doc", rr.docID, "error", err)
		}
	}
}

func (rr *roomReceiver) OnClose(senderID string) {
	rr.room.Leave(senderID)
}

// docIDFromPath extracts `{id}` from `/v1/docs/{id}/connect`.
// Returns the empty string if the shape doesn't match.
//
// Routing in v0 uses stdlib net/http, which doesn't bind path
// parameters for us. When we move to a router (gin or chi in a
// follow-up) this helper goes away.
func docIDFromPath(path string) string {
	const prefix = "/v1/docs/"
	const suffix = "/connect"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	// Reject empty or path-traversal-ish values.
	if inner == "" || strings.ContainsAny(inner, "/?#") {
		return ""
	}
	return inner
}

func newConnID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// originChecker returns a websocket.Upgrader CheckOrigin function
// that accepts only the given origins. An empty list means
// "accept any origin" — DO NOT use that in production.
//
// We don't fall back to a same-host check: the gateway terminates
// TLS and the hostnames the browser sends won't match the
// in-cluster service host. Explicit allowlist is the only
// correct shape.
func originChecker(allowed []string) func(*http.Request) bool {
	if len(allowed) == 0 {
		return func(*http.Request) bool { return true }
	}
	set := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		set[strings.ToLower(strings.TrimSpace(o))] = struct{}{}
	}
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// No Origin header → non-browser client. Allow; the
			// JWT middleware (next iteration) is the real gate.
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		key := strings.ToLower(u.Scheme + "://" + u.Host)
		_, ok := set[key]
		return ok
	}
}

