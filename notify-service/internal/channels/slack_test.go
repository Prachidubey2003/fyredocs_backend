package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSlack_PostsPayloadVerbatimOn2xx(t *testing.T) {
	const payload = `{"text":"Hello from Fyredocs","username":"fyredocs-bot"}`

	received := struct {
		body        []byte
		contentType string
		method      string
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.method = r.Method
		received.contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		received.body = body
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	s := NewSlack()
	err := s.Send(context.Background(), SendRequest{
		Target:  srv.URL,
		Payload: json.RawMessage(payload),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if received.method != http.MethodPost {
		t.Errorf("method = %q, want POST", received.method)
	}
	if received.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", received.contentType)
	}
	if string(received.body) != payload {
		t.Errorf("body = %q, want verbatim payload", received.body)
	}
}

func TestSlack_NonJSONPayloadRejectedLocally(t *testing.T) {
	// Slack would return 400 invalid_payload, but we catch it
	// before the round-trip so the audit row has a clear message.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer srv.Close()

	s := NewSlack()
	err := s.Send(context.Background(), SendRequest{
		Target:  srv.URL,
		Payload: json.RawMessage("not-json-at-all"),
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON payload")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("err = %v, want a hint about valid JSON", err)
	}
	if called {
		t.Error("server should never have been called for invalid JSON")
	}
}

func TestSlack_EmptyTargetRejected(t *testing.T) {
	s := NewSlack()
	err := s.Send(context.Background(), SendRequest{
		Payload: json.RawMessage(`{"text":"x"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "empty target") {
		t.Errorf("err = %v, want empty-target error", err)
	}
}

func TestSlack_EmptyPayloadRejected(t *testing.T) {
	s := NewSlack()
	err := s.Send(context.Background(), SendRequest{Target: "https://example.invalid/hook"})
	if err == nil || !strings.Contains(err.Error(), "empty payload") {
		t.Errorf("err = %v, want empty-payload error", err)
	}
}

func TestSlack_Non2xxReturnsErrorWithBodyExcerpt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid_payload"))
	}))
	defer srv.Close()

	s := NewSlack()
	err := s.Send(context.Background(), SendRequest{
		Target:  srv.URL,
		Payload: json.RawMessage(`{"text":"hi"}`),
	})
	if err == nil {
		t.Fatal("expected non-2xx to produce an error")
	}
	// The audit row's LastError needs to carry both the status
	// and Slack's own error string so on-call can grep for either.
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("err missing status code: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid_payload") {
		t.Errorf("err missing Slack's body excerpt: %v", err)
	}
}

func TestSlack_ContextCancellationPropagates(t *testing.T) {
	// A long-stalled Slack endpoint must surface as an error
	// rather than hang forever — both the HTTP client timeout
	// and ctx cancellation should kill it. We verify ctx
	// cancellation here; the HTTP timeout is an implementation
	// detail tested via NewSlack's default.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the request context is cancelled.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	s := NewSlack()
	err := s.Send(ctx, SendRequest{
		Target:  srv.URL,
		Payload: json.RawMessage(`{"text":"hi"}`),
	})
	if err == nil {
		t.Error("expected error when ctx is cancelled")
	}
}

func TestSlack_NewHasReasonableTimeout(t *testing.T) {
	// Smoke-check the production constructor — a 0 timeout
	// would let one bad Slack hook stall the dispatcher.
	s := NewSlack()
	if s.HTTP == nil {
		t.Fatal("NewSlack must populate HTTP")
	}
	if s.HTTP.Timeout < 1*time.Second {
		t.Errorf("HTTP timeout = %v, want a sane non-zero default", s.HTTP.Timeout)
	}
}
