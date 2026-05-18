package presence

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

type recordedPublish struct {
	subject string
	data    []byte
}

type publishRecorder struct {
	mu    sync.Mutex
	calls []recordedPublish
	err   error
}

func (p *publishRecorder) publish(subject string, data []byte) error {
	if p.err != nil {
		return p.err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	p.calls = append(p.calls, recordedPublish{subject: subject, data: cp})
	return nil
}

type stubRoom struct {
	mu       sync.Mutex
	received [][]byte
}

func (s *stubRoom) BroadcastAll(payload []byte) {
	cp := make([]byte, len(payload))
	copy(cp, payload)
	s.mu.Lock()
	s.received = append(s.received, cp)
	s.mu.Unlock()
}

func (s *stubRoom) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.received)
}

type stubHub struct {
	rooms map[string]Room
}

func (h *stubHub) FindRoom(docID string) Room {
	r, ok := h.rooms[docID]
	if !ok {
		return nil
	}
	return r
}

func TestPublish_BuildsEnvelopeWithReplicaIDPrefix(t *testing.T) {
	rec := &publishRecorder{}
	b, err := NewBridge(rec.publish, nil)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if err := b.Publish("doc-1", []byte("hello")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(rec.calls))
	}
	got := rec.calls[0]
	if got.subject != "collab.broadcast.doc-1" {
		t.Errorf("subject = %q, want %q", got.subject, "collab.broadcast.doc-1")
	}
	if !bytes.Equal(got.data[:envelopeReplicaIDLen], b.replicaID[:]) {
		t.Errorf("envelope replica-id prefix mismatch")
	}
	if !bytes.Equal(got.data[envelopeReplicaIDLen:], []byte("hello")) {
		t.Errorf("envelope payload = %q, want %q", got.data[envelopeReplicaIDLen:], "hello")
	}
}

func TestReceive_DropsOurOwnEcho(t *testing.T) {
	rec := &publishRecorder{}
	room := &stubRoom{}
	hub := &stubHub{rooms: map[string]Room{"doc-1": room}}
	b, err := NewBridge(rec.publish, hub)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	// Build an envelope that LOOKS like one of ours and feed it
	// back through Receive. We must NOT call BroadcastAll —
	// that's the whole point of the loop check.
	envelope := append(b.replicaID[:0:envelopeReplicaIDLen], b.replicaID[:]...)
	envelope = append(envelope, []byte("echo")...)
	b.Receive("collab.broadcast.doc-1", envelope)
	if room.calls() != 0 {
		t.Errorf("local room received own echo (got %d calls)", room.calls())
	}
}

func TestReceive_DispatchesRemoteFrameToLocalRoom(t *testing.T) {
	rec := &publishRecorder{}
	room := &stubRoom{}
	hub := &stubHub{rooms: map[string]Room{"doc-1": room}}
	b, err := NewBridge(rec.publish, hub)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	// A different replica's frame.
	remoteID := [envelopeReplicaIDLen]byte{}
	for i := range remoteID {
		remoteID[i] = byte(i + 1)
	}
	envelope := append([]byte{}, remoteID[:]...)
	envelope = append(envelope, []byte("from remote")...)
	b.Receive("collab.broadcast.doc-1", envelope)
	if room.calls() != 1 {
		t.Fatalf("local room calls = %d, want 1", room.calls())
	}
	if string(room.received[0]) != "from remote" {
		t.Errorf("delivered payload = %q, want %q", room.received[0], "from remote")
	}
}

func TestReceive_DropsWhenNoLocalRoom(t *testing.T) {
	rec := &publishRecorder{}
	hub := &stubHub{rooms: map[string]Room{}} // empty
	b, err := NewBridge(rec.publish, hub)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	remoteID := [envelopeReplicaIDLen]byte{0xff}
	envelope := append([]byte{}, remoteID[:]...)
	envelope = append(envelope, []byte("nobody home")...)
	// Receive should not panic, just silently drop.
	b.Receive("collab.broadcast.unknown-doc", envelope)
}

func TestReceive_DropsShortEnvelope(t *testing.T) {
	rec := &publishRecorder{}
	room := &stubRoom{}
	hub := &stubHub{rooms: map[string]Room{"doc-1": room}}
	b, err := NewBridge(rec.publish, hub)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	b.Receive("collab.broadcast.doc-1", []byte{1, 2, 3}) // < 16 bytes
	if room.calls() != 0 {
		t.Errorf("local room received malformed frame")
	}
}

func TestReceive_DropsBadSubject(t *testing.T) {
	rec := &publishRecorder{}
	room := &stubRoom{}
	hub := &stubHub{rooms: map[string]Room{"doc-1": room}}
	b, err := NewBridge(rec.publish, hub)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	remoteID := [envelopeReplicaIDLen]byte{0xab}
	envelope := append([]byte{}, remoteID[:]...)
	envelope = append(envelope, []byte("x")...)
	// Subject doesn't match the prefix — drop.
	b.Receive("not.our.subject", envelope)
	// Subject has trailing path traversal — drop.
	b.Receive("collab.broadcast./etc/passwd", envelope)
	if room.calls() != 0 {
		t.Errorf("local room received frame on bad subject")
	}
}

func TestNewBridge_RejectsNilPublisher(t *testing.T) {
	_, err := NewBridge(nil, &stubHub{})
	if err == nil {
		t.Error("expected error for nil publisher")
	}
}

func TestPublish_PropagatesUnderlyingError(t *testing.T) {
	rec := &publishRecorder{err: errors.New("nats down")}
	b, err := NewBridge(rec.publish, nil)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if err := b.Publish("doc-1", []byte("x")); err == nil {
		t.Error("expected publish to surface NATS error")
	}
}

func TestReplicaIDHex_StableAcrossCalls(t *testing.T) {
	b, err := NewBridge(func(string, []byte) error { return nil }, nil)
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	first := b.ReplicaIDHex()
	if len(first) != envelopeReplicaIDLen*2 {
		t.Errorf("hex len = %d, want %d", len(first), envelopeReplicaIDLen*2)
	}
	if b.ReplicaIDHex() != first {
		t.Error("ReplicaIDHex returned different values on subsequent calls")
	}
}
