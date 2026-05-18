package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UsageEvent is the per-operation billable record. Each row
// represents one user-attributable action that should count
// toward billing (e.g., a PDF merge, an OCR page, an AI token
// charge) — distinct from AnalyticsEvent which is the broader
// product-analytics stream and may include non-billable signals
// (page views, A/B exposure, error tracking).
//
// We keep usage events in a separate table so:
//   - Schema can evolve independently (billing has stricter
//     immutability/audit needs than analytics).
//   - Rollups and invoice generation can scan a focused index
//     without joining against the much larger analytics_events
//     table.
//   - When billing-service lands as its own service (per plan
//     §4.3.1), the table moves with billing's bounded context
//     and analytics-service exposes it via a stable internal
//     HTTP read endpoint rather than a cross-service DB query.
//
// Quantity + Unit is intentionally generic so the same row
// shape supports ops-counted ops (`1 ops`), page-counted ops
// (`50 pages`), token-counted AI ops (`12500 tokens`), and
// bytes-counted storage ops. Pricing happens in billing-service;
// this table is the source of truth for "what happened".
//
// BillingPeriod is denormalised from OccurredAt as `YYYY-MM` so
// the GET /v1/usage/me?period= query hits an index without a
// date_trunc() expression on every row.
type UsageEvent struct {
	ID            uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	UserID        uuid.UUID  `gorm:"type:uuid;not null;index:idx_usage_user_period,priority:1" json:"userId"`
	APIKeyID      *uuid.UUID `gorm:"type:uuid;index:idx_usage_apikey" json:"apiKeyId,omitempty"`
	EventType     string     `gorm:"type:text;not null;index:idx_usage_event_type" json:"eventType"`
	Quantity      int64      `gorm:"not null;default:1" json:"quantity"`
	Unit          string     `gorm:"type:text;not null;default:'ops'" json:"unit"`
	BillingPeriod string     `gorm:"type:char(7);not null;index:idx_usage_user_period,priority:2" json:"billingPeriod"`
	OccurredAt    time.Time  `gorm:"not null;index:idx_usage_occurred" json:"occurredAt"`
	CreatedAt     time.Time  `gorm:"default:CURRENT_TIMESTAMP" json:"createdAt"`
}

// BeforeCreate fills in the primary key + denormalised billing
// period when GORM commits a row. UUIDv7 keeps inserts roughly
// time-ordered, which matters for the date-range queries the
// billing rollup runs.
func (u *UsageEvent) BeforeCreate(tx *gorm.DB) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.Must(uuid.NewV7())
	}
	if u.OccurredAt.IsZero() {
		u.OccurredAt = time.Now().UTC()
	}
	if u.BillingPeriod == "" {
		u.BillingPeriod = u.OccurredAt.UTC().Format("2006-01")
	}
	return nil
}

// FormatBillingPeriod renders a time as the canonical YYYY-MM
// billing-period key. Exported so handlers/subscribers can match
// the on-disk format exactly when filtering.
func FormatBillingPeriod(t time.Time) string {
	return t.UTC().Format("2006-01")
}
