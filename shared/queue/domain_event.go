package queue

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
)

// DomainEvent is the public event envelope every webhook
// subscriber sees. Internal services publish domain events
// (NOT pre-routed delivery requests) and notify-service's
// fanout consumer expands each event into one delivery per
// matching subscription.
//
// The shape is intentionally Stripe/GitHub-like so customer
// integrations and Zapier translate naturally:
//
//	{
//	  "eventId":   "evt_01HW…",        // UUIDv7 — stable, time-ordered
//	  "eventType": "job.completed",     // dotted, matches subscription.event_type
//	  "userId":    "uuid",              // the user whose subscriptions should fire
//	  "occurredAt":"2026-05-17T...Z",   // wall-clock UTC of the upstream emit
//	  "data": { ... }                   // event-specific payload
//	}
//
// Subscribers MUST deduplicate on `eventId` — JetStream
// retries can deliver the same event id twice. Treating
// eventId as the dedupe key is standard practice (Stripe
// docs explicitly call this out).
type DomainEvent struct {
	EventID    string          `json:"eventId"`
	EventType  string          `json:"eventType"`
	UserID     string          `json:"userId"`
	OccurredAt time.Time       `json:"occurredAt"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// SubjectForDomainEvent returns the NATS subject for a domain
// event. Separates the domain-event stream from the legacy
// `notify.send.>` (delivery-request) stream so consumers
// FilterSubject on `notify.event.>` and never see pre-routed
// deliveries.
//
// One-per-event-type suffix lets per-type retry consumers
// land cleanly later without re-reading every event.
func SubjectForDomainEvent(eventType string) string {
	return "notify.event." + eventType
}

// ErrEventTypeRequired is returned by PublishDomainEvent when
// the caller didn't set EventType. A blank event type would
// publish to `notify.event.` (with trailing dot) which is a
// silent dead letter — fail loud at publish time instead.
var ErrEventTypeRequired = errors.New("queue: DomainEvent.EventType is required")

// PublishDomainEvent marshals + publishes a DomainEvent onto
// the NOTIFY JetStream. Sets EventID + OccurredAt when the
// caller left them zero so every publisher emits an
// auditable identifier even if it forgot.
//
// Best-effort: domain events are not on the request-path
// latency budget; callers typically `go
// queue.PublishDomainEvent(...)` and log on failure.
func PublishDomainEvent(ctx context.Context, js jetstream.JetStream, event DomainEvent) error {
	event.EventType = strings.TrimSpace(event.EventType)
	if event.EventType == "" {
		return ErrEventTypeRequired
	}
	if event.EventID == "" {
		event.EventID = uuid.Must(uuid.NewV7()).String()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = js.Publish(ctx, SubjectForDomainEvent(event.EventType), data)
	return err
}
