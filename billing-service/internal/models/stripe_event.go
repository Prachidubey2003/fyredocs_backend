package models

import "time"

// ProcessedStripeEvent records the IDs of Stripe webhook
// events the handler has already applied, so a retry (Stripe
// retries on any non-2xx response) doesn't double-process. The
// event id is the primary key; an INSERT-IGNORE pattern in the
// handler ("did we just insert it?") drives the dedup.
//
// Schema-wise this is tiny by design: id + timestamp. Stripe's
// own payload is the source of truth for everything else; we
// don't archive it here (the Stripe Dashboard + their API are
// the audit trail for the raw events).
type ProcessedStripeEvent struct {
	// EventID is Stripe's event identifier, e.g.
	// `evt_1NXyZK2eZvKYlo2C…`. Stored verbatim — uniqueness is
	// guaranteed by Stripe.
	EventID string `gorm:"type:text;primaryKey" json:"eventId"`

	// EventType mirrors `event.type` from the payload (e.g.
	// `customer.subscription.created`). Captured so an operator
	// looking at the table without the Stripe Dashboard can
	// see what kinds of events have flowed through recently.
	EventType string `gorm:"type:text;not null;index:idx_stripe_event_type" json:"eventType"`

	ProcessedAt time.Time `gorm:"not null;default:CURRENT_TIMESTAMP;index:idx_stripe_event_processed" json:"processedAt"`
}

// TableName pins the table name so a refactor (renaming the Go
// struct) doesn't silently migrate the table.
func (ProcessedStripeEvent) TableName() string { return "processed_stripe_events" }
