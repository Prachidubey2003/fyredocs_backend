// Package persister stores and retrieves a document's collab
// replay log across room lifecycles.
//
// Without a Persister, the in-memory replay buffer in
// `internal/room` covers intra-session catchup only: as soon as
// the last client leaves and the room self-destructs, the log
// dies with it. A returning user on a quiet doc would see no
// history.
//
// With a Persister, the run-loop:
//   - calls Load when the room starts up and seeds the log;
//   - calls Save when the room is about to die (last Leave),
//     persisting the current log under the document id.
//
// The package ships two implementations:
//
//   - [Noop] — discards saves, returns no frames on load. Used
//     when the platform is configured without a persistence
//     target; the WS path keeps working, just without durability.
//   - [InMemory] — backs the log in a map keyed by docID. Useful
//     for tests + single-process dev where you want to assert
//     load/save behaviour without a live editor-service.
//
// An HTTP-backed implementation that talks to editor-service's
// snapshot endpoint lands in a follow-up turn.
package persister

import "sync"

// Persister is the contract the Hub uses to outlive a room's
// in-memory state. Both methods are best-effort: a load failure
// just means "no history available" (room starts empty), and a
// save failure is logged but doesn't fail the shutdown path.
type Persister interface {
	// Load returns the previously-saved frames for `docID`, or
	// nil if no snapshot exists or the load failed. Implementations
	// should NOT block indefinitely — a slow editor-service must
	// not stall every cold room.
	Load(docID string) [][]byte
	// Save records the room's current log. May be called with
	// an empty slice when a room dies with no broadcasts; the
	// implementation chooses whether to overwrite or skip.
	Save(docID string, frames [][]byte)
}

// Noop is the default Persister — discards saves, returns nothing
// on load. Use when the platform is intentionally configured
// without durability (early dev, single-replica deployments,
// tests that don't care about the persistence path).
type Noop struct{}

func (Noop) Load(string) [][]byte    { return nil }
func (Noop) Save(string, [][]byte)   {}

// InMemory is a Persister that holds snapshots in a goroutine-safe
// map. It's useful for tests + single-process dev. Production
// uses the HTTP-backed implementation.
//
// On Save, the existing snapshot is overwritten — we keep only
// the most recent state, mirroring what the HTTP impl does. On
// Load, the returned slice is a defensive copy so callers can't
// mutate the stored frames through aliasing.
type InMemory struct {
	mu    sync.RWMutex
	store map[string][][]byte
}

// NewInMemory constructs an empty InMemory persister.
func NewInMemory() *InMemory {
	return &InMemory{store: make(map[string][][]byte)}
}

func (m *InMemory) Load(docID string) [][]byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	frames, ok := m.store[docID]
	if !ok {
		return nil
	}
	out := make([][]byte, len(frames))
	for i, f := range frames {
		cp := make([]byte, len(f))
		copy(cp, f)
		out[i] = cp
	}
	return out
}

func (m *InMemory) Save(docID string, frames [][]byte) {
	// Defensive copy on the way in too — the caller (a Room
	// run-loop) re-uses its `r.log` slice across rooms with the
	// same docID over time, and we don't want stored snapshots
	// to alias future log appends.
	cp := make([][]byte, len(frames))
	for i, f := range frames {
		dup := make([]byte, len(f))
		copy(dup, f)
		cp[i] = dup
	}
	m.mu.Lock()
	m.store[docID] = cp
	m.mu.Unlock()
}

// Has is a test-only convenience: reports whether a snapshot
// exists for the given docID. Not part of the Persister interface
// because production code never asks this question.
func (m *InMemory) Has(docID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.store[docID]
	return ok
}
