package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"collab-service/internal/room"
)

func TestDocIDFromPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/v1/docs/abc/connect", "abc"},
		{"/v1/docs/doc_01HV/connect", "doc_01HV"},
		{"/v1/docs//connect", ""},
		{"/v1/docs/abc", ""},
		{"/v1/docs/abc/connect/extra", ""},
		{"/healthz", ""},
		{"/v1/docs/a/b/connect", ""}, // path traversal-ish
	}
	for _, tc := range cases {
		if got := docIDFromPath(tc.in); got != tc.want {
			t.Errorf("docIDFromPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOriginChecker_EmptyMeansAllowAny(t *testing.T) {
	check := originChecker(nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	if !check(req) {
		t.Error("empty allowlist should accept any origin (dev mode)")
	}
}

func TestOriginChecker_AllowlistMatches(t *testing.T) {
	check := originChecker([]string{"https://app.fyredocs.com"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://app.fyredocs.com")
	if !check(req) {
		t.Error("allowed origin was rejected")
	}
}

func TestOriginChecker_AllowlistRejects(t *testing.T) {
	check := originChecker([]string{"https://app.fyredocs.com"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	if check(req) {
		t.Error("non-allowlisted origin was accepted")
	}
}

func TestOriginChecker_NoOriginHeaderIsAllowed(t *testing.T) {
	// Non-browser clients (CLI, server-to-server) omit Origin.
	// JWT middleware is the real gate; the origin policy only
	// constrains browser traffic.
	check := originChecker([]string{"https://app.fyredocs.com"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if !check(req) {
		t.Error("request without Origin header should be allowed")
	}
}

func TestConnect_RejectsMissingDocID(t *testing.T) {
	hub := room.NewHub()
	srv := httptest.NewServer(Connect(hub, nil, nil))
	defer srv.Close()

	// Hit a URL that doesn't match `/v1/docs/{id}/connect` —
	// pass a real HTTP GET (no Upgrade) so we get the 400 path.
	resp, err := http.Get(srv.URL + "/v1/docs//connect")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestConnect_BroadcastReachesOtherClient(t *testing.T) {
	hub := room.NewHub()
	srv := httptest.NewServer(Connect(hub, nil, nil))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/docs/doc1/connect"

	clientA, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer clientA.Close()

	clientB, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer clientB.Close()

	// Wait briefly for both Join events to drain through the
	// run-loop so the room has two members when A broadcasts.
	if err := waitForRoomSize(hub, "doc1", 2, time.Second); err != nil {
		t.Fatalf("room never reached size 2: %v", err)
	}

	payload := []byte("hello from A")
	if err := clientA.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		t.Fatalf("write A: %v", err)
	}

	// B should receive A's payload.
	_ = clientB.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, got, err := clientB.ReadMessage()
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("B received %q, want %q", got, payload)
	}
}

func TestConnect_SenderDoesNotReceiveOwnBroadcast(t *testing.T) {
	hub := room.NewHub()
	srv := httptest.NewServer(Connect(hub, nil, nil))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/docs/doc2/connect"

	clientA, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer clientA.Close()

	clientB, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer clientB.Close()

	if err := waitForRoomSize(hub, "doc2", 2, time.Second); err != nil {
		t.Fatalf("room never reached size 2: %v", err)
	}

	if err := clientA.WriteMessage(websocket.BinaryMessage, []byte("ping")); err != nil {
		t.Fatalf("write A: %v", err)
	}

	// Drain B's incoming so the broadcast cycle completes
	// before we time-out A's read.
	var bGot sync.WaitGroup
	bGot.Add(1)
	go func() {
		defer bGot.Done()
		_ = clientB.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, _ = clientB.ReadMessage()
	}()
	bGot.Wait()

	// A should NOT receive its own broadcast. Set a tight
	// deadline; expecting a timeout error.
	_ = clientA.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := clientA.ReadMessage(); err == nil {
		t.Error("sender unexpectedly received its own broadcast")
	}
}

func TestConnect_LeaveDecrementsRoom(t *testing.T) {
	hub := room.NewHub()
	srv := httptest.NewServer(Connect(hub, nil, nil))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/docs/doc3/connect"

	client, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := waitForRoomSize(hub, "doc3", 1, time.Second); err != nil {
		t.Fatalf("never reached 1: %v", err)
	}
	_ = client.Close()
	// After the read pump exits, the room self-destructs (was
	// the only member). Verify the room is gone — Find returns
	// nil.
	if err := waitForRoomGone(hub, "doc3", time.Second); err != nil {
		t.Fatalf("room never cleaned up: %v", err)
	}
}

func waitForRoomSize(hub *room.Hub, docID string, want int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r := hub.Find(docID); r != nil && r.Size() == want {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errTimeoutWaiting
}

func waitForRoomGone(hub *room.Hub, docID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if hub.Find(docID) == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errTimeoutWaiting
}

var errTimeoutWaiting = &timeoutErr{}

type timeoutErr struct{}

func (*timeoutErr) Error() string { return "timed out waiting" }
