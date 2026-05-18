// Package presence bridges multiple collab-service replicas via
// NATS core pub/sub.
//
// Why core pub/sub and not JetStream: presence is ephemeral — a
// frame published to NATS only matters to replicas actively
// holding clients in the same room AT THAT MOMENT. Persisting
// frames for late-arriving subscribers would defeat the
// "self-destruct on empty room" invariant and force every replica
// to re-play stale state on startup.
//
// Subject layout: `collab.broadcast.<docID>`. A single
// wildcard-subscription (`collab.broadcast.>`) on each replica
// fans every doc's traffic to every replica. The replica then
// looks up the local room and broadcasts (or drops the frame if
// no local room exists for that doc — meaning no local clients
// to deliver to).
//
// Envelope: 16 bytes replica-id || payload. The replica-id is
// minted at process start from crypto/rand. When a replica
// receives a frame whose envelope replica-id matches its own, it
// skips it — those are the echoes of frames we just published.
// This loop-prevention is necessary because nats core pub/sub
// fans messages back to the publisher.
package presence

import (
	"bytes"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
)

// SubjectPrefix is the NATS subject prefix shared by all collab
// broadcasts. The wildcard subscription is `SubjectPrefix + ".>"`.
const SubjectPrefix = "collab.broadcast"

// SubjectWildcard is the subscription pattern that captures every
// doc's broadcast. Exposed so main.go can re-use the constant
// when wiring the subscription.
const SubjectWildcard = SubjectPrefix + ".>"

// envelopeReplicaIDLen is the fixed-width replica-id prefix on
// every NATS message. 16 bytes is the same size we use for
// per-connection IDs in handlers.Connect, picked so the
// collision space across the fleet is comfortably > 2^60.
const envelopeReplicaIDLen = 16

// ErrEnvelopeTooShort is returned when a NATS payload doesn't
// even contain a full replica-id prefix. Indicates a producer
// bug; we drop the frame.
var ErrEnvelopeTooShort = errors.New("presence: envelope shorter than replica-id prefix")

// PublishFn is the side of the NATS API this package needs.
// Production wires it to `nats.Conn.Publish`; tests wire it to a
// recorder. Decoupling from *nats.Conn keeps the package
// unit-testable without an embedded NATS server.
type PublishFn func(subject string, data []byte) error

// Bridge is the cross-replica relay. Construct one per process
// with NewBridge, install Receive as the NATS subscription
// callback, and call Publish from the local broadcast hot path.
type Bridge struct {
	publish    PublishFn
	hub        Hub
	replicaID  [envelopeReplicaIDLen]byte
	dropEchoes bool
}

// Hub is the local-room registry. Bridge calls Find on inbound
// NATS messages. The interface keeps the package decoupled from
// room.Hub for testing.
type Hub interface {
	FindRoom(docID string) Room
}

// Room is the minimum surface the bridge needs from a Room. The
// real *room.Room satisfies it via BroadcastAll.
type Room interface {
	BroadcastAll(payload []byte)
}

// NewBridge constructs a bridge with a freshly-minted replica id.
// publish must be non-nil; hub may be nil only for tests that
// only exercise the publish side.
func NewBridge(publish PublishFn, hub Hub) (*Bridge, error) {
	if publish == nil {
		return nil, errors.New("presence: publish fn required")
	}
	b := &Bridge{publish: publish, hub: hub, dropEchoes: true}
	if _, err := rand.Read(b.replicaID[:]); err != nil {
		return nil, err
	}
	slog.Info("presence bridge ready", "replica_id", b.ReplicaIDHex())
	return b, nil
}

// ReplicaIDHex is a stable string for the local replica id —
// useful for logging and metrics labels.
func (b *Bridge) ReplicaIDHex() string {
	const hex = "0123456789abcdef"
	out := make([]byte, envelopeReplicaIDLen*2)
	for i, v := range b.replicaID {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}

// Publish fan-outs `payload` to every other replica that holds a
// client for docID. Idempotent: returns the publish error
// verbatim — callers typically log-and-drop because losing a
// fan-out frame is recoverable (Yjs re-syncs full state on
// reconnect / late join, awareness payloads are heartbeats).
func (b *Bridge) Publish(docID string, payload []byte) error {
	subject := SubjectPrefix + "." + docID
	envelope := make([]byte, envelopeReplicaIDLen+len(payload))
	copy(envelope[:envelopeReplicaIDLen], b.replicaID[:])
	copy(envelope[envelopeReplicaIDLen:], payload)
	return b.publish(subject, envelope)
}

// Receive is the NATS subscription callback. Production wires it
// with `nc.Subscribe(SubjectWildcard, func(m *nats.Msg) { ... })`
// — passing m.Subject and m.Data into this function.
//
// Behaviour:
//   - Envelope too short → drop (logged at debug).
//   - Replica-id matches us → drop (this is our own echo).
//   - No local room for docID → drop (we have no one to deliver to).
//   - Otherwise → BroadcastAll on the local room.
func (b *Bridge) Receive(subject string, data []byte) {
	if len(data) < envelopeReplicaIDLen {
		slog.Debug("presence: envelope too short", "subject", subject, "len", len(data))
		return
	}
	if b.dropEchoes && bytes.Equal(data[:envelopeReplicaIDLen], b.replicaID[:]) {
		return
	}
	docID, ok := docIDFromSubject(subject)
	if !ok {
		slog.Debug("presence: subject did not match prefix", "subject", subject)
		return
	}
	if b.hub == nil {
		return
	}
	room := b.hub.FindRoom(docID)
	if room == nil {
		return
	}
	room.BroadcastAll(data[envelopeReplicaIDLen:])
}

// docIDFromSubject pulls the doc id off `collab.broadcast.<doc>`.
// Returns ok=false if the subject doesn't match — which means the
// subscription is over-broad and we'd otherwise crash on bad
// data; better to drop.
func docIDFromSubject(subject string) (string, bool) {
	prefix := SubjectPrefix + "."
	if !strings.HasPrefix(subject, prefix) {
		return "", false
	}
	docID := strings.TrimPrefix(subject, prefix)
	if docID == "" || strings.ContainsAny(docID, " /") {
		return "", false
	}
	return docID, true
}
