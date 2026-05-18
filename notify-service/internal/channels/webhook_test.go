package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhook_SendPOSTsBodyAndSignsWithHMAC(t *testing.T) {
	const secret = "super-secret-hmac-key"
	const payload = `{"event":"doc.signed","docId":"abc-123"}`

	received := struct {
		body      []byte
		signature string
		userID    string
		userAgent string
		method    string
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.method = r.Method
		received.signature = r.Header.Get("X-Fyredocs-Signature")
		received.userID = r.Header.Get("X-Fyredocs-User-Id")
		received.userAgent = r.Header.Get("User-Agent")
		body, _ := io.ReadAll(r.Body)
		received.body = body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := NewWebhook([]byte(secret))
	err := wh.Send(context.Background(), SendRequest{
		Target:  srv.URL,
		Payload: json.RawMessage(payload),
		UserID:  "u1",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if received.method != http.MethodPost {
		t.Errorf("method = %q, want POST", received.method)
	}
	if string(received.body) != payload {
		t.Errorf("body = %q, want %q", received.body, payload)
	}
	if received.userID != "u1" {
		t.Errorf("X-Fyredocs-User-Id = %q, want u1", received.userID)
	}
	if !strings.HasPrefix(received.signature, "sha256=") {
		t.Errorf("signature = %q, want sha256= prefix", received.signature)
	}
	if !VerifySignature(received.body, []byte(secret), received.signature) {
		t.Errorf("signature did not verify: %q", received.signature)
	}
}

func TestWebhook_SendOmitsSignatureWhenSecretIsEmpty(t *testing.T) {
	gotSig := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Fyredocs-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := NewWebhook(nil)
	if err := wh.Send(context.Background(), SendRequest{Target: srv.URL, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotSig != "" {
		t.Errorf("signature should be omitted when secret is empty; got %q", gotSig)
	}
}

func TestWebhook_SendReturnsErrorOnNon2xxResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream not ready"))
	}))
	defer srv.Close()

	wh := NewWebhook([]byte("k"))
	err := wh.Send(context.Background(), SendRequest{Target: srv.URL, Payload: []byte(`{}`)})
	if err == nil {
		t.Fatal("expected error on 503, got nil")
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "upstream not ready") {
		t.Errorf("error should include status + body; got %q", err.Error())
	}
}

func TestWebhook_SendRejectsEmptyTarget(t *testing.T) {
	wh := NewWebhook(nil)
	if err := wh.Send(context.Background(), SendRequest{Target: "", Payload: []byte(`{}`)}); err == nil {
		t.Error("expected error for empty target")
	}
}

func TestVerifySignature_RejectsTampered(t *testing.T) {
	secret := []byte("k")
	body := []byte(`{"v":1}`)
	// Compute a valid signature, then flip a byte in the body —
	// VerifySignature must return false.
	wh := NewWebhook(secret)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig := r.Header.Get("X-Fyredocs-Signature")
		// Tamper: VerifySignature against a different body must reject.
		if VerifySignature([]byte(`{"v":2}`), secret, gotSig) {
			t.Error("VerifySignature accepted a tampered body")
		}
		// And reject obviously-bad headers.
		if VerifySignature(body, secret, "not-a-signature") {
			t.Error("VerifySignature accepted a malformed header")
		}
		if VerifySignature(body, secret, "sha256=notHex") {
			t.Error("VerifySignature accepted non-hex digest")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := wh.Send(context.Background(), SendRequest{Target: srv.URL, Payload: body}); err != nil {
		t.Fatalf("Send: %v", err)
	}
}
