// Package eventbridge forwards NATS events published by other
// services into local collab rooms as WebSocket frames.
//
// The presence bridge in `internal/presence` handles
// collab-service-to-collab-service traffic (replicating Yjs sync
// bytes across replicas). This package is the inbound side from
// OTHER services — today only editor-service, which publishes
// comment add/resolve notifications on `editor.comments.<docID>`
// so peers can see live updates without polling.
//
// Why a separate package: presence carries opaque Yjs frames
// with a replica-id loop-prevention envelope. Event frames are
// JSON-formatted, originate from a different service (no echo
// loop possible), and need a different subject prefix. Sharing
// the presence bridge would muddy both contracts.
package eventbridge

import (
	"log/slog"
	"strings"
)

// CommentEventsSubjectPrefix mirrors the constant in
// editor-service/handlers/events.go. Defined here too so the
// collab-service can compile + test without importing editor-
// service code (per CLAUDE.md §1, no cross-service imports).
const CommentEventsSubjectPrefix = "editor.comments"

// CommentEventsSubscription is the wildcard subject this package
// subscribes to. Exposed so main.go can re-use the constant when
// wiring `nc.Subscribe(...)`.
const CommentEventsSubscription = CommentEventsSubjectPrefix + ".>"

// RoomFinder is the minimum subset of `*room.Hub` the bridge
// needs. Decoupled interface keeps the package independent of
// the room package's concrete types and makes testing trivial.
type RoomFinder interface {
	FindRoom(docID string) Room
}

// Room is the bridge's view of a multiplayer room: the only thing
// it does is broadcast bytes to every connected client.
type Room interface {
	BroadcastAll(payload []byte)
}

// Bridge forwards NATS messages into local rooms. Construct one
// with NewBridge and wire its Receive method as the subscription
// callback.
type Bridge struct {
	hub RoomFinder
}

// NewBridge constructs a bridge over the given hub. A nil hub is
// allowed for testing the message-routing logic in isolation —
// Receive becomes a no-op.
func NewBridge(hub RoomFinder) *Bridge {
	return &Bridge{hub: hub}
}

// Receive is the NATS subscription callback. Production wires it
// with `nc.Subscribe(CommentEventsSubscription, func(m *nats.Msg) {
//     bridge.Receive(m.Subject, m.Data)
// })`.
//
// We forward the entire JSON payload verbatim as a WS frame —
// the frontend already parses comment-event shapes for its local
// add/resolve handlers, so passing the same JSON over the wire
// reuses that codepath.
//
// No loop prevention needed: editor-service publishes once,
// collab-service replicas each receive the message exactly once
// from NATS, and a replica's clients receive from THAT replica
// only — so a given user sees each event once.
func (b *Bridge) Receive(subject string, data []byte) {
	if b == nil || b.hub == nil {
		return
	}
	docID, ok := docIDFromSubject(subject)
	if !ok {
		slog.Debug("eventbridge: subject did not match prefix", "subject", subject)
		return
	}
	room := b.hub.FindRoom(docID)
	if room == nil {
		// No local clients for this doc — drop silently. This is
		// the common path on replicas that don't host the doc.
		return
	}
	room.BroadcastAll(data)
}

// docIDFromSubject pulls the doc id off
// `editor.comments.<docID>`. Returns ok=false if the subject
// doesn't match — defensive, because subscriptions can drift.
func docIDFromSubject(subject string) (string, bool) {
	prefix := CommentEventsSubjectPrefix + "."
	if !strings.HasPrefix(subject, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(subject, prefix)
	if id == "" || strings.ContainsAny(id, " /") {
		return "", false
	}
	return id, true
}
