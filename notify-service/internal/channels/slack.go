package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Slack is the channel implementation for outbound Slack
// notifications via incoming webhooks. Each Send POSTs the payload
// JSON to req.Target (a Slack incoming-webhook URL — caller is
// responsible for storing it securely; the URL IS the secret).
//
// Slack's incoming-webhook API accepts any JSON body that
// deserialises to a "message" shape. Common payloads:
//
//	{"text": "Hello from Fyredocs"}                              // simplest
//	{"text": "Build failed", "username": "fyredocs-bot"}         // sender override
//	{"text": "fallback", "blocks": [ ... ]}                      // rich Block Kit
//
// The channel does NOT massage the payload — it forwards bytes
// verbatim. If the caller wants Block Kit, they assemble it; if
// they just want a string, they wrap it in `{"text": "..."}`. This
// matches Slack's own API contract and avoids smuggling product
// decisions into the transport layer.
//
// Success criteria:
//   - HTTP 200 from Slack. Their happy-path body is the literal
//     string `ok` but we don't gate on it — any 2xx is treated as
//     success so future API tweaks (e.g., `ok\n`) don't trip the
//     dispatcher.
//   - Non-2xx returns an error with the truncated response body so
//     the audit row carries a usable diagnostic ("invalid_payload",
//     "channel_is_archived", "no_text", etc.).
type Slack struct {
	HTTP *http.Client
}

// NewSlack returns a Slack channel with a 10-second HTTP client.
// The Slack API is generally fast (sub-100ms p95); 10s is the same
// budget the webhook channel uses, kept aligned so retry policies
// don't need per-channel tuning.
func NewSlack() *Slack {
	return &Slack{HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Send POSTs req.Payload to req.Target. Returns nil on 2xx, an
// error wrapping the truncated response body otherwise.
func (s *Slack) Send(ctx context.Context, req SendRequest) error {
	if req.Target == "" {
		return fmt.Errorf("slack: empty target URL")
	}
	body := []byte(req.Payload)
	if len(body) == 0 {
		return fmt.Errorf("slack: empty payload")
	}
	// Reject payloads that aren't valid JSON early — Slack would
	// 400 anyway, but failing here lets the dispatcher record a
	// clearer LastError than "Slack: 400 invalid_payload".
	if !json.Valid(body) {
		return fmt.Errorf("slack: payload is not valid JSON")
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.Target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("slack: send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Slack's 4xx bodies are short error strings
		// ("invalid_payload", "no_service", "channel_not_found", …)
		// so a 512B excerpt is more than enough.
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack: %d %s: %s", resp.StatusCode, resp.Status, string(excerpt))
	}
	return nil
}
