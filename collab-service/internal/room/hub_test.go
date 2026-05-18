package room

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubConn is a Connection that records every Send and signals on
// Close. Concurrency-safe; tests can read Sent without locking
// because the hub serialises Send calls per-connection from a single
// goroutine.
type stubConn struct {
	id     string
	mu     sync.Mutex
	sent   [][]byte
	closed atomic.Bool
	// sendErr, if non-nil, is returned from Send to exercise the
	// eviction path (a misbehaving socket should get dropped by the
	// hub instead of backpressuring the room).
	sendErr error
}

func newStubConn(id string) *stubConn {
	return &stubConn{id: id}
}

func (c *stubConn) ID() string { return c.id }

func (c *stubConn) Send(payload []byte) error {
	if c.sendErr != nil {
		return c.sendErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Copy payload so the hub can recycle its buffer.
	dup := make([]byte, len(payload))
	copy(dup, payload)
	c.sent = append(c.sent, dup)
	return nil
}

func (c *stubConn) Close() error {
	c.closed.Store(true)
	return nil
}

func (c *stubConn) snapshot() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.sent))
	copy(out, c.sent)
	return out
}

// waitFor polls a predicate until it returns true or `timeout`
// elapses. Tests use it to wait for the hub's run-loop to process
// an event without sleep-fest fragility.
func waitFor(t *testing.T, timeout time.Duration, pred func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

func TestHub_FindOrCreate_IsIdempotent(t *testing.T) {
	h := NewHub()
	r1 := h.FindOrCreate("doc-1")
	r2 := h.FindOrCreate("doc-1")
	if r1 != r2 {
		t.Error("FindOrCreate should return the same Room for the same docID")
	}
	if got := h.RoomCount(); got != 1 {
		t.Errorf("RoomCount = %d, want 1", got)
	}
}

func TestHub_DifferentDocsGetDifferentRooms(t *testing.T) {
	h := NewHub()
	r1 := h.FindOrCreate("doc-1")
	r2 := h.FindOrCreate("doc-2")
	if r1 == r2 {
		t.Error("rooms for different docs must be distinct")
	}
	if got := h.RoomCount(); got != 2 {
		t.Errorf("RoomCount = %d, want 2", got)
	}
}

func TestRoom_BroadcastReachesPeersButNotSender(t *testing.T) {
	h := NewHub()
	r := h.FindOrCreate("doc-1")
	a := newStubConn("a")
	b := newStubConn("b")
	c := newStubConn("c")
	r.Join(a)
	r.Join(b)
	r.Join(c)
	waitFor(t, time.Second, func() bool { return r.Size() == 3 },
		"three clients joined")

	r.Broadcast("a", []byte("hello"))
	waitFor(t, time.Second,
		func() bool { return len(b.snapshot()) == 1 && len(c.snapshot()) == 1 },
		"b and c each received one frame")

	if got := len(a.snapshot()); got != 0 {
		t.Errorf("sender should not receive its own broadcast; got %d frames", got)
	}
	if string(b.snapshot()[0]) != "hello" || string(c.snapshot()[0]) != "hello" {
		t.Errorf("payload mismatch; got b=%q c=%q", b.snapshot()[0], c.snapshot()[0])
	}
}

func TestRoom_LeaveStopsReceivingBroadcasts(t *testing.T) {
	h := NewHub()
	r := h.FindOrCreate("doc-1")
	a := newStubConn("a")
	b := newStubConn("b")
	r.Join(a)
	r.Join(b)
	waitFor(t, time.Second, func() bool { return r.Size() == 2 }, "two clients")

	r.Leave("b")
	waitFor(t, time.Second, func() bool { return r.Size() == 1 }, "b has left")
	if !b.closed.Load() {
		t.Error("Leave should Close the evicted connection")
	}

	r.Broadcast("a", []byte("after-leave"))
	// Give the broadcast a tick to process. b should NOT have
	// received anything.
	time.Sleep(20 * time.Millisecond)
	if got := len(b.snapshot()); got != 0 {
		t.Errorf("b received %d frames after leaving; want 0", got)
	}
}

func TestRoom_SelfDestructsWhenEmpty(t *testing.T) {
	h := NewHub()
	r := h.FindOrCreate("doc-1")
	a := newStubConn("a")
	r.Join(a)
	waitFor(t, time.Second, func() bool { return r.Size() == 1 }, "one client")

	r.Leave("a")
	// After the last leave, the room signals onEmpty and the hub
	// drops it from the map. RoomCount eventually goes to 0.
	waitFor(t, time.Second, func() bool { return h.RoomCount() == 0 },
		"room removed from hub after last leave")
	if h.Find("doc-1") != nil {
		t.Error("Find should return nil for a self-destructed room")
	}
}

func TestRoom_EvictsConnectionsThatErrorOnSend(t *testing.T) {
	h := NewHub()
	r := h.FindOrCreate("doc-1")
	good := newStubConn("good")
	bad := newStubConn("bad")
	bad.sendErr = errors.New("simulated socket write failure")

	r.Join(good)
	r.Join(bad)
	waitFor(t, time.Second, func() bool { return r.Size() == 2 }, "two clients")

	// Broadcast from `good` — bad should be evicted on its Send
	// failure. Hub keeps good around.
	r.Broadcast("good", []byte("ping"))

	waitFor(t, time.Second, func() bool { return bad.closed.Load() },
		"bad connection closed after send error")
	waitFor(t, time.Second, func() bool { return r.Size() == 1 },
		"room size drops to 1 after evicting bad")
}

func TestHub_RaceFreeConcurrentJoins(t *testing.T) {
	// Quick smoke test for the FindOrCreate double-checked locking.
	// `go test -race` would catch breakage; this just exercises the
	// path.
	h := NewHub()
	var wg sync.WaitGroup
	const N = 50
	rooms := make([]*Room, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rooms[idx] = h.FindOrCreate("doc-shared")
		}(i)
	}
	wg.Wait()
	// All goroutines should have got the same room.
	for i := 1; i < N; i++ {
		if rooms[i] != rooms[0] {
			t.Fatalf("concurrent FindOrCreate produced distinct rooms at idx %d", i)
		}
	}
	if got := h.RoomCount(); got != 1 {
		t.Errorf("RoomCount = %d, want 1 after concurrent FindOrCreate", got)
	}
}

func TestRoom_JoinAfterShutdownClosesConn(t *testing.T) {
	h := NewHub()
	r := h.FindOrCreate("doc-1")
	a := newStubConn("a")
	r.Join(a)
	waitFor(t, time.Second, func() bool { return r.Size() == 1 }, "one client")
	r.Leave("a")
	waitFor(t, time.Second, func() bool { return h.RoomCount() == 0 }, "room gone")

	// Now try to join the dead room handle. The room should refuse
	// silently AND close the connection so the caller doesn't leak.
	late := newStubConn("late")
	r.Join(late)
	waitFor(t, time.Second, func() bool { return late.closed.Load() },
		"late join after shutdown closes the new connection")
}

// stubPersister is a single-map persister: Save writes a snapshot,
// Load reads it back. Tests use it to exercise both directions
// against the same backing store, which is closer to what the
// HTTP-backed persister will do in production than two
// independent maps would be.
//
// Tests can pre-seed via seed() to simulate "a previous instance
// of the service wrote this snapshot before we booted".
type stubPersister struct {
	mu   sync.Mutex
	data map[string][][]byte
}

func newStubPersister() *stubPersister {
	return &stubPersister{data: make(map[string][][]byte)}
}

func (s *stubPersister) seed(docID string, frames ...[]byte) {
	s.Save(docID, frames)
}

func (s *stubPersister) Load(docID string) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	frames := s.data[docID]
	if frames == nil {
		return nil
	}
	// Defensive copy so a later mutation through the returned
	// slice doesn't corrupt the stored snapshot.
	out := make([][]byte, len(frames))
	for i, f := range frames {
		cp := make([]byte, len(f))
		copy(cp, f)
		out[i] = cp
	}
	return out
}

func (s *stubPersister) Save(docID string, frames [][]byte) {
	cp := make([][]byte, len(frames))
	for i, f := range frames {
		dup := make([]byte, len(f))
		copy(dup, f)
		cp[i] = dup
	}
	s.mu.Lock()
	s.data[docID] = cp
	s.mu.Unlock()
}

func (s *stubPersister) savedFor(docID string) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[docID]
}

func TestHub_LoadsPersistedLogOnRoomCreation(t *testing.T) {
	p := newStubPersister()
	p.seed("doc-cold", []byte("history-A"), []byte("history-B"))
	h := NewHubWithPersister(p)
	r := h.FindOrCreate("doc-cold")

	// A client joining a freshly-created room should see the
	// persisted history via the replay path.
	a := newStubConn("a")
	r.Join(a)
	waitFor(t, time.Second, func() bool { return len(a.snapshot()) == 2 },
		"joiner receives persisted history")
	got := a.snapshot()
	if string(got[0]) != "history-A" || string(got[1]) != "history-B" {
		t.Errorf("seeded replay = %q,%q; want history-A,history-B", got[0], got[1])
	}
}

func TestHub_SavesLogWhenRoomSelfDestructs(t *testing.T) {
	p := newStubPersister()
	h := NewHubWithPersister(p)
	r := h.FindOrCreate("doc-save")

	a := newStubConn("a")
	r.Join(a)
	waitFor(t, time.Second, func() bool { return r.Size() == 1 }, "a joined")
	r.Broadcast("a", []byte("frame-1"))
	r.Broadcast("a", []byte("frame-2"))
	r.Leave("a")
	waitFor(t, time.Second, func() bool { return h.RoomCount() == 0 },
		"room self-destructed")

	saved := p.savedFor("doc-save")
	if len(saved) != 2 {
		t.Fatalf("Save received %d frames, want 2", len(saved))
	}
	if string(saved[0]) != "frame-1" || string(saved[1]) != "frame-2" {
		t.Errorf("saved frames = %q,%q; want frame-1,frame-2", saved[0], saved[1])
	}
}

func TestHub_RoundTripsSaveThenLoad(t *testing.T) {
	// End-to-end: a room edits some frames, dies, and a NEW
	// room for the same docID picks up where the old one left off.
	p := newStubPersister()
	h := NewHubWithPersister(p)

	r1 := h.FindOrCreate("doc-round")
	a := newStubConn("a")
	r1.Join(a)
	waitFor(t, time.Second, func() bool { return r1.Size() == 1 }, "a joined r1")
	r1.Broadcast("a", []byte("epoch-1"))
	r1.Leave("a")
	waitFor(t, time.Second, func() bool { return h.RoomCount() == 0 }, "r1 gone")

	// Fresh room — load from the persister, replay to b.
	r2 := h.FindOrCreate("doc-round")
	if r2 == r1 {
		t.Fatal("FindOrCreate after self-destruct returned the same Room")
	}
	b := newStubConn("b")
	r2.Join(b)
	waitFor(t, time.Second, func() bool { return len(b.snapshot()) == 1 },
		"b receives persisted history from previous epoch")
	got := b.snapshot()
	if string(got[0]) != "epoch-1" {
		t.Errorf("post-restart replay = %q; want epoch-1", got[0])
	}
}

func TestRoom_ReplaysLogToLateJoiner(t *testing.T) {
	h := NewHub()
	r := h.FindOrCreate("doc-replay")
	a := newStubConn("a")
	r.Join(a)
	waitFor(t, time.Second, func() bool { return r.Size() == 1 }, "a joined")

	r.Broadcast("a", []byte("first"))
	r.Broadcast("a", []byte("second"))
	r.Broadcast("a", []byte("third"))

	// Late joiner — should receive all three frames via the replay
	// path BEFORE any future broadcast reaches them.
	b := newStubConn("b")
	r.Join(b)
	waitFor(t, time.Second, func() bool { return len(b.snapshot()) == 3 },
		"late joiner receives full log")

	got := b.snapshot()
	if string(got[0]) != "first" || string(got[1]) != "second" || string(got[2]) != "third" {
		t.Errorf("replay order = %q/%q/%q, want first/second/third", got[0], got[1], got[2])
	}
	// Sanity: client a (the sender) didn't get its own frames
	// back via fan-out — the replay is for joiners only.
	if len(a.snapshot()) != 0 {
		t.Errorf("sender a received %d frames; expected 0 (sender exclusion)", len(a.snapshot()))
	}
}

func TestRoom_ReplayDoesNotIncludeFutureBroadcasts(t *testing.T) {
	// Subtle: the replay must be done synchronously with the
	// join so the new client always sees the log followed by
	// any subsequent broadcast (and not the reverse). We can't
	// observe the ordering directly without a scheduling hook,
	// but we CAN assert that after Join+Broadcast settles, the
	// late joiner has both the historical frames AND the new
	// one, exactly once each.
	h := NewHub()
	r := h.FindOrCreate("doc-order")
	a := newStubConn("a")
	r.Join(a)
	waitFor(t, time.Second, func() bool { return r.Size() == 1 }, "a joined")
	r.Broadcast("a", []byte("history-1"))

	b := newStubConn("b")
	r.Join(b)
	r.Broadcast("a", []byte("after-join"))

	waitFor(t, time.Second, func() bool { return len(b.snapshot()) == 2 },
		"late joiner sees history + post-join frame")
	got := b.snapshot()
	if string(got[0]) != "history-1" || string(got[1]) != "after-join" {
		t.Errorf("delivery order = %q,%q; want history-1,after-join", got[0], got[1])
	}
}

func TestRoom_LogEvictsOldestBeyondFrameCap(t *testing.T) {
	prev := SetMaxLogFrames(3)
	t.Cleanup(func() { SetMaxLogFrames(prev) })

	h := NewHub()
	r := h.FindOrCreate("doc-evict")
	a := newStubConn("a")
	r.Join(a)
	waitFor(t, time.Second, func() bool { return r.Size() == 1 }, "a joined")

	// Send 5 frames — the cap is 3, so the oldest 2 should be
	// evicted.
	for _, p := range []string{"f1", "f2", "f3", "f4", "f5"} {
		r.Broadcast("a", []byte(p))
	}

	b := newStubConn("b")
	r.Join(b)
	waitFor(t, time.Second, func() bool { return len(b.snapshot()) == 3 },
		"late joiner receives the last 3 frames after eviction")
	got := b.snapshot()
	if string(got[0]) != "f3" || string(got[1]) != "f4" || string(got[2]) != "f5" {
		t.Errorf("post-eviction replay = %q/%q/%q; want f3/f4/f5", got[0], got[1], got[2])
	}
}

func TestRoom_LogEvictsOldestBeyondByteCap(t *testing.T) {
	// Each frame is 10 bytes; cap at 25 so we hold ~2 frames.
	prev := SetMaxLogBytes(25)
	t.Cleanup(func() { SetMaxLogBytes(prev) })

	h := NewHub()
	r := h.FindOrCreate("doc-bytes")
	a := newStubConn("a")
	r.Join(a)
	waitFor(t, time.Second, func() bool { return r.Size() == 1 }, "a joined")

	for _, p := range []string{"AAAAAAAAAA", "BBBBBBBBBB", "CCCCCCCCCC", "DDDDDDDDDD"} {
		r.Broadcast("a", []byte(p))
	}

	b := newStubConn("b")
	r.Join(b)
	// The cap allows at most 2 full 10-byte frames (20 < 25; a
	// third would push us to 30 > 25). So the late joiner
	// receives the last 2.
	waitFor(t, time.Second, func() bool { return len(b.snapshot()) == 2 },
		"late joiner receives only frames within byte cap")
	got := b.snapshot()
	if string(got[0]) != "CCCCCCCCCC" || string(got[1]) != "DDDDDDDDDD" {
		t.Errorf("byte-cap eviction kept %q/%q; want C/D", got[0], got[1])
	}
}

