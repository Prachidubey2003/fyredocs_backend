package eventbridge

import (
	"sync"
	"testing"
)

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

func TestReceive_ForwardsToLocalRoom(t *testing.T) {
	room := &stubRoom{}
	hub := &stubHub{rooms: map[string]Room{"doc-1": room}}
	b := NewBridge(hub)

	payload := []byte(`{"kind":"comment.added","docId":"doc-1","comment":{"id":"c-1"}}`)
	b.Receive("editor.comments.doc-1", payload)

	if room.calls() != 1 {
		t.Fatalf("BroadcastAll calls = %d, want 1", room.calls())
	}
	if string(room.received[0]) != string(payload) {
		t.Errorf("forwarded payload mismatch: got %q want %q",
			room.received[0], payload)
	}
}

func TestReceive_DropsWhenNoLocalRoom(t *testing.T) {
	// Most collab-service replicas at any moment don't have a
	// client for any given doc — the "no local room" path is
	// the common case and must be silent + cheap.
	hub := &stubHub{rooms: map[string]Room{}}
	b := NewBridge(hub)
	b.Receive("editor.comments.unknown", []byte("payload"))
	// No panic, no side-effect — just exercising the path.
}

func TestReceive_DropsBadSubject(t *testing.T) {
	room := &stubRoom{}
	hub := &stubHub{rooms: map[string]Room{"doc-1": room}}
	b := NewBridge(hub)

	b.Receive("not.our.subject", []byte("payload"))
	// Subjects with traversal-ish characters are dropped too —
	// belt-and-braces alongside NATS's own subject validation.
	b.Receive("editor.comments./etc/passwd", []byte("payload"))
	if room.calls() != 0 {
		t.Errorf("bad subjects produced %d calls, want 0", room.calls())
	}
}

func TestReceive_NilBridgeIsNoOp(t *testing.T) {
	// Pattern: main.go may end up with a nil bridge when NATS
	// is unconfigured. The NATS subscription should never call
	// Receive in that case (subscription wasn't installed), but
	// defensive nil-receivers keep the wiring forgiving.
	var b *Bridge
	b.Receive("editor.comments.doc-1", []byte("x"))
}

func TestReceive_NilHubIsNoOp(t *testing.T) {
	b := NewBridge(nil)
	b.Receive("editor.comments.doc-1", []byte("x"))
}

func TestDocIDFromSubject(t *testing.T) {
	cases := []struct {
		in       string
		wantID   string
		wantOK   bool
	}{
		{"editor.comments.abc", "abc", true},
		{"editor.comments.doc_01HV", "doc_01HV", true},
		{"editor.comments.", "", false},
		{"editor.comments", "", false},
		{"editor.comments.a/b", "", false},
		{"some.other.subject.abc", "", false},
	}
	for _, tc := range cases {
		gotID, gotOK := docIDFromSubject(tc.in)
		if gotID != tc.wantID || gotOK != tc.wantOK {
			t.Errorf("docIDFromSubject(%q) = (%q, %v); want (%q, %v)",
				tc.in, gotID, gotOK, tc.wantID, tc.wantOK)
		}
	}
}
