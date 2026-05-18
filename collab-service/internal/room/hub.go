package room

import (
	"sync"
	"sync/atomic"
)

// Connection is the per-client side of a Room. The handler layer
// implements it on top of a websocket; tests implement it on top of
// a channel.
//
// Send may be called from a goroutine other than the one that
// constructed the Connection. Implementations are responsible for
// their own write serialisation — typically a per-conn outbox
// channel + a single writer goroutine. If Send returns an error the
// hub treats the connection as dead and drops it.
type Connection interface {
	// ID is a stable per-connection identifier (typically a v7
	// UUID). Used so the hub can target/exclude specific clients
	// without comparing pointer values.
	ID() string
	// Send delivers one frame to the client. Bytes are the verbatim
	// payload — the hub doesn't interpret them.
	Send(payload []byte) error
	// Close releases connection-side resources. Called by the hub
	// when the connection is being evicted (peer disconnect, send
	// error, or hub shutdown).
	Close() error
}

// Persister is the contract the Hub uses to outlive a room's
// in-memory state. Defined here (rather than imported from
// internal/persister) so the room package has no dependency on
// persister implementations — the persister package satisfies
// this interface duck-typed.
//
// Both methods are best-effort. Load returning nil means "no
// history available, start empty". Save should not block long;
// implementations that talk to a remote service must time out.
type Persister interface {
	Load(docID string) [][]byte
	Save(docID string, frames [][]byte)
}

// Hub is the top-level registry of all Rooms. There is exactly one
// Hub per collab-service process; it owns the lifecycle of every
// Room.
//
// All public methods are safe for concurrent use. Internally the
// Hub uses a single RWMutex to guard the rooms map; per-room
// coordination lives inside each Room (channel-serialised).
type Hub struct {
	mu        sync.RWMutex
	rooms     map[string]*Room
	persister Persister // optional; nil = no cross-room durability
}

// NewHub constructs an empty Hub with no persister — late-joiner
// catchup is in-memory only, and room state dies with the room.
func NewHub() *Hub {
	return &Hub{rooms: make(map[string]*Room)}
}

// NewHubWithPersister constructs a Hub backed by `p`. Each newly-
// created room loads its initial log from p.Load, and saves its
// log to p.Save just before self-destructing. Pass a no-op
// implementation if durability is intentionally disabled.
func NewHubWithPersister(p Persister) *Hub {
	return &Hub{rooms: make(map[string]*Room), persister: p}
}

// FindOrCreate returns the Room for the given document id, creating
// it on first use. The returned Room is ready to accept Join calls.
func (h *Hub) FindOrCreate(docID string) *Room {
	h.mu.RLock()
	r, ok := h.rooms[docID]
	h.mu.RUnlock()
	if ok {
		return r
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Re-check under the write lock — another goroutine may have
	// raced us between the RLock and the Lock.
	if r, ok := h.rooms[docID]; ok {
		return r
	}
	r = newRoom(docID, h.persister, func() {
		// Self-destruct callback: when a room empties out, it asks
		// the hub to remove it from the map. Callback instead of a
		// back-pointer inverts the dependency cleanly.
		h.mu.Lock()
		delete(h.rooms, docID)
		h.mu.Unlock()
	})
	h.rooms[docID] = r
	return r
}

// Find returns the existing Room for `docID`, or nil if no clients
// are connected. Useful for instrumentation; the hot path uses
// FindOrCreate.
func (h *Hub) Find(docID string) *Room {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.rooms[docID]
}

// RoomCount returns the number of currently-active rooms. Drives
// the `/metrics` gauge.
func (h *Hub) RoomCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms)
}

// Room is the per-document multiplayer session. Membership and
// broadcast are serialised through `events` so the connection map
// is touched by exactly one goroutine (the run-loop).
type Room struct {
	docID     string
	events    chan event
	done      chan struct{}
	onEmpty   func()
	stopOnce  sync.Once
	persister Persister // optional; nil = no load/save

	// log is the in-memory replay buffer. Frames are appended on
	// every broadcast and replayed to clients that join later.
	// Only the run() goroutine reads or mutates these — no mutex
	// is needed.
	//
	// Yjs's sync protocol is idempotent: re-applying an update a
	// client already has is harmless, just wasteful. So a "send
	// the whole log on join" strategy is correct even when the
	// new client overlaps with frames they originated themselves
	// in a previous session. The bound is what saves us from
	// unbounded growth on long-lived rooms.
	log      [][]byte
	logBytes int
}

// maxLogFrames / maxLogBytes cap the in-memory replay buffer.
// Stored as atomics because earlier tests' run-loop goroutines
// can still be alive (and reading the limits) while a later test
// tweaks them via SetMaxLog*. Production tunes them once at
// init and forgets — these are not hot-path values.
//
// Production tuning values: 1024 frames (covers a few minutes of
// typical Yjs-update traffic per room) and 8 MiB total — enough
// for a steady editing session, small enough that one room can't
// starve another for GC headroom.
var (
	maxLogFrames atomic.Int64
	maxLogBytes  atomic.Int64
)

func init() {
	maxLogFrames.Store(1024)
	maxLogBytes.Store(8 * 1024 * 1024)
}

// SetMaxLogFrames overrides the frame-count cap. Returns the
// previous value so tests can restore it. Production code does
// not call this.
func SetMaxLogFrames(n int) int {
	prev := maxLogFrames.Swap(int64(n))
	return int(prev)
}

// SetMaxLogBytes overrides the total-byte cap. Returns the
// previous value so tests can restore it.
func SetMaxLogBytes(n int) int {
	prev := maxLogBytes.Swap(int64(n))
	return int(prev)
}

type eventKind int

const (
	evJoin eventKind = iota
	evLeave
	evBroadcast
	evSize
)

type event struct {
	kind    eventKind
	conn    Connection // evJoin only
	from    string     // evLeave (connID), evBroadcast (senderID to exclude)
	payload []byte     // evBroadcast
	reply   chan int   // evSize
}

func newRoom(docID string, persister Persister, onEmpty func()) *Room {
	r := &Room{
		docID:     docID,
		events:    make(chan event, 64),
		done:      make(chan struct{}),
		onEmpty:   onEmpty,
		persister: persister,
	}
	go r.run()
	return r
}

// DocID is the document id this room serves.
func (r *Room) DocID() string { return r.docID }

// Join registers a connection to the room. Returns once the join
// event has been queued; the actual map insert happens on the
// run-loop goroutine. The connection becomes broadcast-eligible
// after this returns.
//
// If the room is shutting down (race during the last-leave →
// self-destruct window), Join closes the connection so the caller
// doesn't leak the socket.
//
// Concurrency: there are three windows we need to handle.
//  1. Room is healthy → events <- ev wins.
//  2. Room is shut down → <-r.done wins, we close the conn.
//  3. Tiny window where r.done is closed AFTER our send lands in
//     the buffer. The run-loop's defer drains stragglers and
//     closes their conns, so this is still leak-free.
//
// The leading fast-path select on r.done is the common shutdown
// case (RoomCount has already hit 0 in the hub map): pick it
// deterministically instead of letting the inner select randomize
// between two ready arms.
func (r *Room) Join(conn Connection) {
	select {
	case <-r.done:
		_ = conn.Close()
		return
	default:
	}
	select {
	case r.events <- event{kind: evJoin, conn: conn}:
	case <-r.done:
		_ = conn.Close()
	}
}

// Leave removes the connection identified by `connID`. Idempotent.
func (r *Room) Leave(connID string) {
	select {
	case r.events <- event{kind: evLeave, from: connID}:
	case <-r.done:
	}
}

// Broadcast relays `payload` to every connection in the room
// EXCEPT the sender. We exclude the sender because Yjs's sync
// protocol relies on the local client having already applied its
// own update — re-delivering would either double-apply (broken
// state) or no-op via CRDT idempotence (wasted bandwidth).
func (r *Room) Broadcast(senderID string, payload []byte) {
	select {
	case r.events <- event{kind: evBroadcast, from: senderID, payload: payload}:
	case <-r.done:
	}
}

// BroadcastAll relays `payload` to every connection in the room
// with no exclusion. Used by the NATS presence bridge: when a
// remote replica forwards a message into this process, every
// local client is a recipient (the original sender lives on
// another replica and was already excluded from THAT replica's
// fan-out). An empty senderID will never match a connID (we mint
// connIDs as 16-byte hex) so it's a safe sentinel.
func (r *Room) BroadcastAll(payload []byte) {
	r.Broadcast("", payload)
}

// Size returns the current member count. Synchronous — blocks
// until the run-loop services the query. Cheap enough for metrics
// scraping; not a hot-path operation.
func (r *Room) Size() int {
	reply := make(chan int, 1)
	select {
	case r.events <- event{kind: evSize, reply: reply}:
	case <-r.done:
		return 0
	}
	select {
	case n := <-reply:
		return n
	case <-r.done:
		return 0
	}
}

// run is the single goroutine that owns the clients map. All
// mutations + broadcasts flow through this loop, so no mutex is
// needed on `clients` itself.
func (r *Room) run() {
	// Seed the log from the persister BEFORE we begin processing
	// events. Doing this here (rather than in newRoom) guarantees
	// that the first evJoin sees a populated log — Join is queued
	// in the events channel and won't be drained until we drop
	// into the select loop below. Persister.Load must therefore
	// be reasonably fast (production impls put a timeout on the
	// underlying HTTP call).
	if r.persister != nil {
		if seed := r.persister.Load(r.docID); seed != nil {
			for _, f := range seed {
				r.appendLog(f)
			}
		}
	}
	clients := make(map[string]Connection)
	defer func() {
		// On shutdown, close every connection so the read/write
		// pumps in the handler exit cleanly.
		for _, c := range clients {
			_ = c.Close()
		}
		// Drain any in-flight events that landed in the buffer
		// after we processed the final Leave. The only kind we
		// must clean up here is evJoin — its conn needs Close
		// to avoid a leaked socket. evBroadcast / evLeave /
		// evSize own no resources and harmlessly drop on the
		// floor.
		for {
			select {
			case ev := <-r.events:
				if ev.kind == evJoin && ev.conn != nil {
					_ = ev.conn.Close()
				}
			default:
				return
			}
		}
	}()
	for {
		select {
		case ev := <-r.events:
			switch ev.kind {
			case evJoin:
				clients[ev.conn.ID()] = ev.conn
				// Replay the buffered log to the new joiner.
				// Doing this synchronously inside the run-loop
				// (rather than from the caller) means no
				// subsequent broadcast can race past the
				// replay — the new client always sees the log
				// before any frame that arrives after Join.
				// On the first Send error we evict the client
				// the same way the broadcast path does.
				for _, frame := range r.log {
					if err := ev.conn.Send(frame); err != nil {
						delete(clients, ev.conn.ID())
						_ = ev.conn.Close()
						break
					}
				}
			case evLeave:
				if c, ok := clients[ev.from]; ok {
					delete(clients, ev.from)
					_ = c.Close()
				}
				if len(clients) == 0 {
					r.shutdown()
					return
				}
			case evBroadcast:
				// Append to the replay buffer BEFORE fan-out so
				// a client that joins a microsecond later still
				// sees this frame in the replay (the join
				// would otherwise see "no log yet" because the
				// run-loop hadn't returned from the fan-out
				// path).
				r.appendLog(ev.payload)
				for id, c := range clients {
					if id == ev.from {
						continue
					}
					if err := c.Send(ev.payload); err != nil {
						// Evict misbehaving clients eagerly so a
						// stuck socket can't backpressure the rest
						// of the room.
						delete(clients, id)
						_ = c.Close()
					}
				}
				if len(clients) == 0 {
					r.shutdown()
					return
				}
			case evSize:
				if ev.reply != nil {
					ev.reply <- len(clients)
				}
			}
		case <-r.done:
			return
		}
	}
}

// appendLog records a frame in the replay buffer, evicting the
// oldest entries when either bound is exceeded. Called only from
// the run-loop goroutine — no synchronisation needed.
func (r *Room) appendLog(payload []byte) {
	// Copy the payload defensively: the caller (Broadcast) keeps
	// the underlying slice alive only until fan-out completes,
	// but the log retains a reference until eviction. Without
	// the copy, a later mutation by the caller could corrupt
	// previously-buffered frames.
	cp := make([]byte, len(payload))
	copy(cp, payload)
	r.log = append(r.log, cp)
	r.logBytes += len(cp)
	frameCap := int(maxLogFrames.Load())
	byteCap := int(maxLogBytes.Load())
	for len(r.log) > frameCap || r.logBytes > byteCap {
		r.logBytes -= len(r.log[0])
		r.log[0] = nil // GC hint — drop reference to evicted frame.
		r.log = r.log[1:]
	}
}

func (r *Room) shutdown() {
	r.stopOnce.Do(func() {
		// Persist the log BEFORE we drop visible-state markers —
		// the persister may take its sweet time on the wire, and
		// we'd rather hold the room handle valid (so a racing
		// FindOrCreate returns this dying room and waits) than
		// risk a fresh room being created mid-save.
		//
		// Empty logs are still passed through so an explicit
		// "this doc has no history" can be recorded by impls
		// that want to (the in-memory + noop impls don't).
		if r.persister != nil {
			r.persister.Save(r.docID, r.log)
		}
		// close(r.done) BEFORE onEmpty is important for correctness:
		// the hub map is updated inside onEmpty, so RoomCount==0
		// becomes visible to other goroutines. If close(r.done) ran
		// AFTER, a caller that observed RoomCount==0 could still
		// race into Join with r.done not yet closed — both arms of
		// Join's select would be ready and random selection could
		// queue the conn into a channel nobody will ever read.
		close(r.done)
		if r.onEmpty != nil {
			r.onEmpty()
		}
	})
}
