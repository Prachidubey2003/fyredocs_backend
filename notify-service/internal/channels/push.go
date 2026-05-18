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

// Push is the channel implementation for outbound mobile push
// notifications via Expo Push (https://docs.expo.dev/push-notifications/sending-notifications/).
//
// Expo Push wraps APNs (iOS) and FCM (Android) behind a single
// HTTPS API, so the notify-service doesn't have to learn either
// vendor's auth model in v0 — the cert / service-account
// plumbing lives once, inside Expo's infrastructure. Per plan
// §2.9 / §4.10 this is the preferred path even when the
// long-term goal is to swap in direct APNs/FCM transports for
// teams that want to remove Expo from the trust chain.
//
// Payload contract: caller passes a JSON object the channel
// forwards verbatim into Expo's message envelope, with the
// per-event ``Target`` (Expo push token, e.g.
// ``ExponentPushToken[xxxx-…]``) injected as ``to``. Common
// shapes Expo accepts::
//
//	{"title": "Build done", "body": "Fyredocs editor v2.1 shipped"}
//	{"title": "...", "body": "...", "data": {"docId": "doc_01HV…"},
//	 "sound": "default", "priority": "high", "channelId": "edits"}
//
// Channel does NOT massage the payload — caller assembles
// platform-specific fields (``ttl``, ``badge``, ``categoryId``,
// ``mutableContent``) directly. Same posture as the Slack
// channel: the transport stays a thin forwarder; product
// decisions live in publishers.
//
// Success criteria:
//   - HTTP 200 from Expo AND every ticket in the response array
//     has ``status == "ok"``. A 200 with a per-ticket ``status:
//     "error"`` is still a delivery failure (Expo's typical
//     ``DeviceNotRegistered`` / ``InvalidCredentials`` shape).
//     Surface the first error's ``message`` so the audit row
//     carries a usable diagnostic.
//   - Non-2xx → error with the truncated response body, same as
//     Slack/Webhook.
type Push struct {
	// Endpoint is the Expo Push HTTPS URL. Production:
	// ``https://exp.host/--/api/v2/push/send``. Overridden in
	// tests to point at httptest; left as a public field rather
	// than a constructor argument so dev environments can
	// rebind the URL without rebuilding the dispatcher.
	Endpoint string
	// AccessToken is the optional Expo access token. Most
	// projects don't need this — the push token itself is the
	// per-device secret. Projects that have enabled "Enhanced
	// Security for Push" require it as ``Authorization: Bearer``.
	AccessToken string
	HTTP        *http.Client
}

const defaultPushEndpoint = "https://exp.host/--/api/v2/push/send"

// NewPush returns a Push channel with the supplied access token
// and a 10-second HTTP client. Pass an empty token when the
// project doesn't require enhanced security (the common case).
func NewPush(accessToken string) *Push {
	return &Push{
		Endpoint:    defaultPushEndpoint,
		AccessToken: accessToken,
		HTTP:        &http.Client{Timeout: 10 * time.Second},
	}
}

// expoMessage is the request body Expo's API expects. We
// marshal field-by-field rather than embedding the raw payload
// so any unrecognised field the caller sent gets ignored at
// notify-service rather than rejected at Expo (and so we can
// safely set ``to`` from the channel without trusting the
// caller to leave it empty).
type expoMessage struct {
	To       string         `json:"to"`
	Title    string         `json:"title,omitempty"`
	Body     string         `json:"body,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Sound    string         `json:"sound,omitempty"`
	Badge    *int           `json:"badge,omitempty"`
	TTL      *int           `json:"ttl,omitempty"`
	Priority string         `json:"priority,omitempty"`
	// Android-only channel id (notification channel created by
	// the mobile app). iOS ignores it.
	ChannelID string `json:"channelId,omitempty"`
	// MutableContent enables iOS notification-service-extension
	// modifications. Defaults false on the wire.
	MutableContent *bool `json:"mutableContent,omitempty"`
}

// expoResponse is the shape Expo returns. ``data`` is either one
// ticket object or an array thereof depending on whether the
// request sent one or many messages. v0 only sends one per Send
// call so we accept either form and normalise to a slice.
type expoResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors,omitempty"`
}

type expoTicket struct {
	Status  string `json:"status"`  // "ok" or "error"
	ID      string `json:"id,omitempty"`
	Message string `json:"message,omitempty"`
	Details struct {
		Error string `json:"error,omitempty"`
	} `json:"details,omitempty"`
}

// Send POSTs the (target + payload) to Expo Push. Returns nil
// on per-ticket ``status: ok``; non-nil error otherwise.
func (p *Push) Send(ctx context.Context, req SendRequest) error {
	if req.Target == "" {
		return fmt.Errorf("push: empty target token")
	}
	if len(req.Payload) == 0 {
		return fmt.Errorf("push: empty payload")
	}

	// Decode the caller payload into a structured message, then
	// inject `to` from req.Target. Unknown fields are dropped —
	// this prevents a caller from setting `to` to a different
	// device by passing it inside the payload.
	var msg expoMessage
	if err := json.Unmarshal(req.Payload, &msg); err != nil {
		return fmt.Errorf("push: payload is not valid Expo message JSON: %w", err)
	}
	msg.To = req.Target

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("push: re-marshal message: %w", err)
	}

	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = defaultPushEndpoint
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("push: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Accept-Encoding", "gzip, deflate")
	if p.AccessToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.AccessToken)
	}

	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("push: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("push: %d %s: %s", resp.StatusCode, resp.Status, string(excerpt))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return fmt.Errorf("push: read response: %w", err)
	}

	var er expoResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return fmt.Errorf("push: decode Expo response: %w", err)
	}
	if len(er.Errors) > 0 {
		// Top-level error (validation, auth) — always treated
		// as a delivery failure even on 200, which is consistent
		// with Expo's documented contract.
		return fmt.Errorf("push: expo error %s: %s", er.Errors[0].Code, er.Errors[0].Message)
	}

	tickets := parseExpoTickets(er.Data)
	if len(tickets) == 0 {
		return fmt.Errorf("push: empty ticket array in Expo response")
	}
	for _, t := range tickets {
		if t.Status != "ok" {
			detail := t.Message
			if detail == "" {
				detail = t.Details.Error
			}
			if detail == "" {
				detail = "unknown error"
			}
			return fmt.Errorf("push: ticket status=%s: %s", t.Status, detail)
		}
	}
	return nil
}

// parseExpoTickets handles both shapes of Expo's `data` field:
// a single ticket object or an array of them. Returning a slice
// keeps Send's loop uniform.
func parseExpoTickets(raw json.RawMessage) []expoTicket {
	if len(raw) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '[' {
		var arr []expoTicket
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil
		}
		return arr
	}
	var single expoTicket
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil
	}
	return []expoTicket{single}
}
