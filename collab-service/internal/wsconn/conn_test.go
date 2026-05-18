package wsconn

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// pair upgrades a fresh websocket pair and hands the SERVER side
// back to the caller. The handler goroutine returns immediately —
// nobody but the caller touches the server *websocket.Conn, so
// the test code has unique ownership of it (a hard race-detector
// requirement when our Conn's pumps start reading/writing).
func pair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	up := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	serverCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverCh <- ws
	}))
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	select {
	case s := <-serverCh:
		return s, c
	case <-time.After(2 * time.Second):
		t.Fatalf("server side never produced ws")
		return nil, nil
	}
}

func TestConn_SendThenRead(t *testing.T) {
	server, client := pair(t)
	defer client.Close()

	conn := New("c1", server)
	rx := &recordingReceiver{}
	done := make(chan struct{})
	go func() {
		conn.Run(rx)
		close(done)
	}()

	if err := conn.Send([]byte("payload-1")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	_, got, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(got) != "payload-1" {
		t.Errorf("got %q, want %q", got, "payload-1")
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run never returned after client close")
	}
}

func TestConn_SendAfterCloseReturnsErrClosed(t *testing.T) {
	server, client := pair(t)
	defer client.Close()

	conn := New("c1", server)
	// Drive Run + Close from the same goroutine — no pump is
	// active when Send is invoked, so the closed-channel select
	// arm is exercised deterministically.
	_ = conn.Close()
	if err := conn.Send([]byte("x")); err != ErrClosed {
		t.Errorf("Send after Close = %v, want ErrClosed", err)
	}
}

func TestConn_CloseIsIdempotent(t *testing.T) {
	server, client := pair(t)
	defer client.Close()

	conn := New("c1", server)
	_ = conn.Close()
	// Second call must not panic on the already-closed `closed`
	// channel — sync.Once guards the close.
	_ = conn.Close()
}

func TestConn_ReadDeliversToReceiver(t *testing.T) {
	server, client := pair(t)
	defer client.Close()

	rx := &recordingReceiver{}
	conn := New("server", server)

	done := make(chan struct{})
	go func() {
		conn.Run(rx)
		close(done)
	}()

	if err := client.WriteMessage(websocket.BinaryMessage, []byte("hello")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if err := rx.waitForMessages(1, time.Second); err != nil {
		t.Fatalf("receiver never got message: %v", err)
	}
	if got := rx.first(); string(got) != "hello" {
		t.Errorf("rx got %q, want %q", got, "hello")
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run never returned after client close")
	}
	if !rx.closedSeen() {
		t.Error("OnClose was never invoked")
	}
}

type recordingReceiver struct {
	mu       sync.Mutex
	messages [][]byte
	closed   atomic.Bool
}

func (r *recordingReceiver) OnMessage(_ string, payload []byte) {
	cp := make([]byte, len(payload))
	copy(cp, payload)
	r.mu.Lock()
	r.messages = append(r.messages, cp)
	r.mu.Unlock()
}

func (r *recordingReceiver) OnClose(string) { r.closed.Store(true) }

func (r *recordingReceiver) closedSeen() bool { return r.closed.Load() }

func (r *recordingReceiver) first() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.messages) == 0 {
		return nil
	}
	return r.messages[0]
}

func (r *recordingReceiver) waitForMessages(n int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := len(r.messages)
		r.mu.Unlock()
		if got >= n {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errReceiverTimeout
}

var errReceiverTimeout = &recvTimeout{}

type recvTimeout struct{}

func (*recvTimeout) Error() string { return "receiver timed out" }
