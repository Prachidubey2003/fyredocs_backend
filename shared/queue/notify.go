package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// NotifyEvent is a single delivery request consumed by
// notify-service. Publishers (any service that needs to ping a
// user / customer) push these onto the NOTIFY JetStream; the
// notify-service subscriber routes each one through the right
// channel (email / webhook / push / slack).
//
// One row in `notify_deliveries` is persisted per emitted event —
// success-or-fail — so the dev console can show the full audit
// of "what we tried to deliver and what happened".
//
// Channel semantics:
//   - `email`   : `target` is the destination address. `subject` and
//                 `body` (or `html`) live in `payload`.
//   - `webhook` : `target` is the destination URL. `payload` is
//                 JSON-marshalled and POSTed; an HMAC signature
//                 over the body lands in the `X-Fyredocs-Signature`
//                 header using the subscriber's shared secret.
//   - `push`    : `target` is the device token. (FCM/APNs — wired
//                 when mobile lands; v0 logs+persists only.)
//   - `slack`   : `target` is the webhook URL. (v0 logs+persists.)
//
// `idempotencyKey` is optional — when set, notify-service collapses
// repeat events with the same key into a single delivery row.
// Useful for "send this notification once even if the upstream
// service retries publishing it".
type NotifyEvent struct {
	Channel        string          `json:"channel"`
	Target         string          `json:"target"`
	UserID         string          `json:"userId,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
	OccurredAt     time.Time       `json:"occurredAt"`
}

// Notification channel identifiers. Stable strings — persisted in
// the `notify_deliveries.channel` column. Don't rename.
const (
	ChannelEmail   = "email"
	ChannelWebhook = "webhook"
	ChannelPush    = "push"
	ChannelSlack   = "slack"
)

// SubjectForNotify returns the NATS subject for a notification
// channel. Subscribers FilterSubject on `notify.send.>` so a
// per-channel suffix lets future fan-out (e.g., a separate retry
// consumer for webhook) target one channel without re-reading
// every other.
func SubjectForNotify(channel string) string {
	return "notify.send." + channel
}

// PublishNotifyEvent marshals + publishes a NotifyEvent onto the
// NOTIFY JetStream. Best-effort: notifications are not on the
// request-path latency budget; callers typically
// `go queue.PublishNotifyEvent(...)` and log on failure.
func PublishNotifyEvent(ctx context.Context, js jetstream.JetStream, event NotifyEvent) error {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = js.Publish(ctx, SubjectForNotify(event.Channel), data)
	return err
}
