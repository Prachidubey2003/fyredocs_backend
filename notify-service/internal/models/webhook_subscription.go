package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// WebhookSubscription is one external party's standing
// registration to receive notifications by HTTP POST when a
// specific event fires for the owning user.
//
// The owner is the authenticated end-user (Zapier and similar
// integrations create one subscription per user-account-on-
// their-side, NOT per Fyredocs platform). Fanout: when an
// internal service publishes a domain event (e.g.,
// `job.completed`, `subscription.changed`), notify-service
// fetches every active subscription matching (event_type +
// user_id) and enqueues one delivery per row via the existing
// webhook channel.
//
// Secret storage uses envelope encryption (AES-256-GCM via
// shared/keystore) rather than one-way hashing. The fanout
// dispatcher needs to RECOVER the plaintext to HMAC-sign
// outbound payloads — bcrypt-style hashing would prevent that,
// since the comparison only goes one way. Every major
// webhook provider (Stripe, GitHub, Slack) stores recoverable
// signing secrets the same way.
//
// Plaintext is returned at creation only and never again —
// callers MUST persist it themselves on their side. The row
// keeps:
//   - secret_ciphertext: AES-256-GCM sealed bytes
//   - secret_wrapped_dek: the per-row DEK wrapped with the
//     service master KEK (nil for pass-through / KEK-off
//     deploys, in which case secret_ciphertext IS the
//     plaintext)
//   - secret_prefix: first 8 chars of the plaintext, shown in
//     the list response so users can identify a key during
//     rotation without exposing the full value
//
// Soft delete via DeletedAt + gorm scope; GET / DELETE filter
// it out. We keep the row instead of hard-deleting so an audit
// query can still attribute a past delivery.
type WebhookSubscription struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index:idx_webhook_user_event" json:"userId"`
	EventType string    `gorm:"type:text;not null;index:idx_webhook_user_event" json:"eventType"`
	TargetURL string    `gorm:"type:text;not null" json:"targetUrl"`

	// SecretCiphertext is the AES-256-GCM-sealed bytes of the
	// plaintext signing secret. When SecretWrappedDEK is nil
	// (pass-through mode) these bytes ARE the plaintext.
	SecretCiphertext []byte `gorm:"type:bytea;not null" json:"-"`

	// SecretWrappedDEK is the wrapped per-row Data Encryption
	// Key (exactly keystore.WrappedDEKSize bytes when set).
	// Nil means the row was written without a configured KEK
	// — `SecretCiphertext` is plaintext.
	SecretWrappedDEK []byte `gorm:"type:bytea" json:"-"`

	// SecretPrefix is the first 8 chars of the plaintext
	// secret. Useful when the user needs to identify which
	// stored copy of the secret a subscription corresponds to
	// (e.g., during rotation). Treating only 8 chars as visible
	// preserves brute-force resistance: the full secret is 64
	// chars of base64url entropy.
	SecretPrefix string `gorm:"type:text;not null" json:"secretPrefix"`

	// Status: `active` | `disabled`. Disabled subscriptions
	// stay in the table but the fanout dispatcher skips them
	// — used by the future auto-disable-after-N-failures
	// circuit breaker (tracked separately).
	Status string `gorm:"type:text;not null;default:'active';index:idx_webhook_status" json:"status"`

	// FailureCount tracks consecutive delivery failures. The
	// dispatcher increments on every non-2xx response and
	// resets to 0 on success. The circuit breaker flips Status
	// to `disabled` past a threshold — implementation in the
	// follow-up fanout work.
	FailureCount int `gorm:"not null;default:0" json:"failureCount"`

	// LastDeliveryAt is the wall-clock time of the most-recent
	// delivery attempt (success or failure). Surfaced in the
	// list response so users can spot a stuck subscription.
	LastDeliveryAt *time.Time `gorm:"" json:"lastDeliveryAt,omitempty"`

	CreatedAt time.Time      `gorm:"default:CURRENT_TIMESTAMP" json:"createdAt"`
	UpdatedAt time.Time      `gorm:"default:CURRENT_TIMESTAMP" json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// TableName pins the table name so a refactor of the Go struct
// doesn't silently migrate the table.
func (WebhookSubscription) TableName() string { return "webhook_subscriptions" }

// BeforeCreate assigns a v7 UUID + sensible defaults. Matches
// the convention used by every other model in this service.
func (w *WebhookSubscription) BeforeCreate(_ *gorm.DB) error {
	if w.ID == uuid.Nil {
		w.ID = uuid.Must(uuid.NewV7())
	}
	if w.Status == "" {
		w.Status = WebhookStatusActive
	}
	return nil
}

// Status constants. Public so handlers + future fanout dispatcher
// can reference them without hard-coding the strings.
const (
	WebhookStatusActive   = "active"
	WebhookStatusDisabled = "disabled"
)
