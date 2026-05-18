// Package wsconn adapts a [*websocket.Conn] to the
// [room.Connection] interface that the Hub speaks. It owns the
// read/write goroutine pair for a single editor tab.
//
// Lifecycle:
//
//	upgrade → New(conn, docID) → Run(room)
//	             ▲                  │ blocks until close
//	             │                  ▼
//	     handler defers   room invokes Send/Close from another goroutine
//
// The read pump runs on the goroutine that calls Run; the write
// pump runs on a goroutine started by Run. Both exit cleanly on
// peer-close, network error, or Close() from the hub side.
package wsconn

import (
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ErrSlowConsumer is returned by Send when the per-connection
// outbox is full. The hub treats this as fatal and evicts the
// conn — propagating backpressure into the room would let a slow
// peer stall everyone else.
var ErrSlowConsumer = errors.New("collab: peer outbox full")

// ErrClosed is returned by Send after Close has been called or
// the read pump has exited (peer disconnect).
var ErrClosed = errors.New("collab: connection closed")

// Tuning constants. These mirror the gorilla/websocket sample
// "chat" server but with budgets scoped to the collab use case:
// Yjs updates are usually small (< 4KB) but awareness payloads can
// burst. The hub already drops slow clients, so the per-conn
// outbox stays modest.
const (
	// writeWait is the per-frame write deadline. Beyond this the
	// peer is considered slow and the conn is torn down.
	writeWait = 10 * time.Second
	// pongWait is how long we wait for a pong reply before
	// declaring the connection dead.
	pongWait = 60 * time.Second
	// pingPeriod must be < pongWait. Sends a ping every pingPeriod
	// from the write pump.
	pingPeriod = (pongWait * 9) / 10
	// maxMessageSize caps an inbound payload. Yjs updates over our
	// star topology rarely exceed a few hundred KB; cap defensively.
	maxMessageSize = 1 << 20 // 1 MiB
	// outboxSize is the per-connection send queue depth. A peer
	// that can't drain at this rate is evicted.
	outboxSize = 64
)

// Receiver is the side of the contract that consumes inbound
// bytes from a single connection. The handler implements this by
// calling Room.Broadcast on its room. Decoupling lets us unit-test
// the read pump without dragging in a Hub.
type Receiver interface {
	// OnMessage is invoked once per inbound text/binary frame
	// from the peer. Implementations MUST NOT retain `payload`
	// past the call — the read pump reuses the underlying buffer.
	OnMessage(senderID string, payload []byte)
	// OnClose is invoked exactly once, after the read pump has
	// exited (peer close, network error, or local Close). Use it
	// to call Room.Leave.
	OnClose(senderID string)
}

// Conn wraps a *websocket.Conn so it satisfies room.Connection.
// Each Conn owns one read-pump goroutine and one write-pump
// goroutine; Send is non-blocking up to outboxSize, after which
// the hub will see an error and evict.
type Conn struct {
	id     string
	ws     *websocket.Conn
	out    chan []byte
	closed chan struct{}
	once   sync.Once
}

// New constructs a Conn around an already-upgraded websocket.
// `id` is the stable connection identifier reported to the hub
// (typically a v7 UUID minted by the upgrade handler).
func New(id string, ws *websocket.Conn) *Conn {
	return &Conn{
		id:     id,
		ws:     ws,
		out:    make(chan []byte, outboxSize),
		closed: make(chan struct{}),
	}
}

// ID satisfies room.Connection.
func (c *Conn) ID() string { return c.id }

// Send queues a payload for the write pump. Returns an error if
// the connection has been closed or the outbox is full — in either
// case the hub will treat the conn as dead and evict it. The hub
// must not block its run-loop on a slow peer.
func (c *Conn) Send(payload []byte) error {
	select {
	case <-c.closed:
		return ErrClosed
	default:
	}
	select {
	case c.out <- payload:
		return nil
	case <-c.closed:
		return ErrClosed
	default:
		// Outbox full — peer is too slow. Return an error so the
		// hub evicts us; the write pump will exit on the next
		// closed-channel signal.
		return ErrSlowConsumer
	}
}

// Close shuts down both pumps. Idempotent. Safe to call from any
// goroutine including the read pump itself.
func (c *Conn) Close() error {
	c.once.Do(func() {
		close(c.closed)
	})
	return c.ws.Close()
}

// Run starts the read + write pumps and BLOCKS the calling
// goroutine until the read pump exits. The handler defers a
// Close() so cleanup runs even on panics.
//
// Layout:
//
//	caller goroutine  → readPump (blocks here)
//	new goroutine     → writePump (exits when closed channel fires)
func (c *Conn) Run(rx Receiver) {
	go c.writePump()
	c.readPump(rx)
}

func (c *Conn) readPump(rx Receiver) {
	defer func() {
		_ = c.Close()
		rx.OnClose(c.id)
	}()
	c.ws.SetReadLimit(maxMessageSize)
	_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, payload, err := c.ws.ReadMessage()
		if err != nil {
			// Normal close, abnormal close, network error — all
			// route here. We don't distinguish; the room will be
			// notified via OnClose.
			return
		}
		rx.OnMessage(c.id, payload)
	}
}

func (c *Conn) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.ws.Close()
	}()
	for {
		select {
		case payload, ok := <-c.out:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.ws.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			// Binary frame: Yjs sync-protocol messages and
			// awareness payloads are both opaque bytes to us.
			if err := c.ws.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.closed:
			// Hub asked us to go away — write the close frame
			// best-effort and exit.
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			_ = c.ws.WriteMessage(websocket.CloseMessage, nil)
			return
		}
	}
}
