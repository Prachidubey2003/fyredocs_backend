package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Webhook is the channel implementation for outbound HTTP
// webhooks. Each Send POSTs the raw payload to the target URL
// with two headers:
//
//   - `Content-Type: application/json` — payload is always JSON.
//   - `X-Fyredocs-Signature: sha256=<hex>` — HMAC-SHA256 of the
//     body using the configured secret. Subscribers verify this
//     to prove the call came from Fyredocs and the body wasn't
//     tampered with in transit.
//
// The signature scheme is documented for customers in the
// developer docs; rotating the secret is a separate API call
// (not yet implemented) that flips the active key.
//
// Send returns non-2xx as an error, so the dispatcher persists
// LastError with the response status. The HTTP transport reuses
// keepalive connections so a busy webhook subscriber doesn't
// thrash the TCP handshake.
type Webhook struct {
	Secret []byte
	HTTP   *http.Client
}

// NewWebhook returns a Webhook channel with the given HMAC secret
// and a 10-second-timeout HTTP client. Pass an empty secret to
// disable signing — the header is omitted entirely in that case;
// subscribers verifying signatures will reject the request.
// Useful for dev / preview environments.
func NewWebhook(secret []byte) *Webhook {
	return &Webhook{
		Secret: secret,
		HTTP:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (w *Webhook) Send(ctx context.Context, req SendRequest) error {
	if req.Target == "" {
		return fmt.Errorf("webhook: empty target URL")
	}
	body := []byte(req.Payload)
	if len(body) == 0 {
		body = []byte(`null`) // explicit JSON null beats an empty body so subscribers can parse uniformly
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.Target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.UserID != "" {
		httpReq.Header.Set("X-Fyredocs-User-Id", req.UserID)
	}
	// Per-send override wins. Used by the fanout dispatcher so
	// each subscription's HMAC is generated with its own key
	// (set at subscription creation time and stored
	// envelope-encrypted on the row). When unset the channel
	// falls back to its configured default — preserves the
	// legacy `notify.send.webhook` path that has no per-row
	// secret to recover.
	sig := req.Secret
	if len(sig) == 0 {
		sig = w.Secret
	}
	if len(sig) > 0 {
		mac := hmac.New(sha256.New, sig)
		mac.Write(body)
		httpReq.Header.Set("X-Fyredocs-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	client := w.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("webhook: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read up to 512B of the response body for the error
		// message — subscribers commonly return a structured 4xx
		// explaining why; truncating keeps the audit row small.
		bodyExcerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook: %d %s: %s", resp.StatusCode, resp.Status, string(bodyExcerpt))
	}
	return nil
}

// VerifySignature is the helper subscribers use on their side to
// confirm a request's HMAC. Exported here so test fixtures can
// share the implementation with the production transport — a
// drift between the two would produce silent verification
// failures in real subscriber code.
func VerifySignature(body, secret []byte, header string) bool {
	const prefix = "sha256="
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	want, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}
