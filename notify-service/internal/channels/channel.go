// Package channels owns the transport-specific implementations of
// outbound notification delivery (email, webhook, push, slack).
//
// Each channel implements [Channel] and is registered with the
// dispatcher at startup. The dispatcher routes inbound
// NotifyEvents by their `channel` field; an event for an
// unregistered channel is persisted with status=`skipped` and
// LastError="unsupported channel".
package channels

import (
	"context"
	"encoding/json"
	"errors"
)

// Channel is the single point of dispatch for one transport
// (email, webhook, push, slack). Implementations are stateless or
// hold transport state (HTTP client, SMTP client) internally.
//
// Send must return:
//   - nil on successful delivery (the dispatcher marks the
//     Delivery row `delivered`).
//   - a non-nil error if the attempt failed. The error string is
//     persisted as Delivery.LastError; the dispatcher promotes the
//     row to status=`failed` and does NOT retry — JetStream retry
//     policy is the single retry mechanism.
type Channel interface {
	Send(ctx context.Context, req SendRequest) error
}

// SendRequest is the channel-agnostic payload Send takes. Each
// channel knows how to interpret `target` + `payload` for its own
// transport; the dispatcher is intentionally ignorant of the
// per-channel shape.
type SendRequest struct {
	// Target is the destination address. Channel-specific:
	//   - email   : RFC-5322 address
	//   - webhook : HTTPS URL
	//   - push    : device token
	//   - slack   : incoming-webhook URL
	Target string
	// Payload is the channel-specific body. Channels MAY error if
	// the payload doesn't deserialise to the shape they expect.
	Payload json.RawMessage
	// UserID is the destination user — informational only; some
	// channels (webhook with HMAC) include it in the signed
	// headers so subscribers can route per-user.
	UserID string
	// Secret is an optional per-send override for transports
	// that sign their payload (today: webhook HMAC). When
	// non-empty, the channel uses this key instead of its
	// configured default. The fanout dispatcher sets this to
	// the recovered per-subscription signing secret so every
	// subscriber verifies with the key it received at creation
	// time; the legacy `notify.send.*` path leaves it nil so
	// the channel falls back to its configured default (the
	// global NOTIFY_WEBHOOK_SECRET).
	Secret []byte
}

// ErrUnsupportedChannel is the sentinel returned by the
// dispatcher when an inbound NotifyEvent names a channel with no
// registered implementation.
var ErrUnsupportedChannel = errors.New("channels: unsupported channel")
