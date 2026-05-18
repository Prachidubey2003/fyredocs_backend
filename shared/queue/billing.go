package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// BillableEvent represents a single user-attributable action that
// should count toward billing. Publishers across the platform
// (api-gateway, editor-service, organize-pdf, optimize-pdf, etc.)
// emit one BillableEvent per metered op; analytics-service is the
// consumer that persists them into the `usage_events` table for
// later rollup by billing-service.
//
// Field semantics:
//   - UserID is required. Guest / anonymous ops are NOT metered
//     (free-tier limits are enforced at api-gateway via the
//     existing rate limiter, not via this stream).
//   - APIKeyID is optional. When present, it lets billing
//     attribute usage to a specific developer credential —
//     useful for per-key spending caps and a future "usage by
//     API key" tab in the dev console.
//   - EventType identifies the metered op family. Examples:
//     `op.merge`, `op.split`, `op.ocr`, `op.edit`, `doc.parse`,
//     `ai.tokens` (when AI ships). The string is stable; pricing
//     in billing-service joins on it.
//   - Quantity + Unit decouples the count from its dimension.
//     1 ops, 50 pages, 12500 tokens, 1048576 bytes all fit.
//   - OccurredAt: when the underlying op completed (NOT when
//     the event was published). Lets out-of-order publishes
//     still land in the right billing period.
type BillableEvent struct {
	UserID     string    `json:"userId"`
	APIKeyID   string    `json:"apiKeyId,omitempty"`
	EventType  string    `json:"eventType"`
	Quantity   int64     `json:"quantity"`
	Unit       string    `json:"unit"`
	OccurredAt time.Time `json:"occurredAt"`
}

// SubjectForBillable returns the NATS subject for a billable
// event type. Mirrors SubjectForAnalytics — the consumer's
// FilterSubject is `billable.events.>` so a wildcard per
// event-type lets us add finer-grained per-type fan-out
// (e.g., a per-type dead-letter queue) without a migration.
func SubjectForBillable(eventType string) string {
	return "billable.events." + eventType
}

// PublishBillableEvent marshals and publishes a BillableEvent to
// the BILLABLE_EVENTS JetStream.
//
// Publishers should treat this as a best-effort emission —
// billing is not on the request-path latency budget. Callers
// typically `go queue.PublishBillableEvent(...)` and log on
// failure rather than failing the user-facing request when the
// metering write doesn't land. The InterestPolicy on the stream
// drops messages when no consumer is subscribed, so a missing
// analytics-service doesn't grow a queue indefinitely.
func PublishBillableEvent(ctx context.Context, js jetstream.JetStream, event BillableEvent) error {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = js.Publish(ctx, SubjectForBillable(event.EventType), data)
	return err
}
