package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Subscription is the durable record of which plan a user is on.
// Plans themselves live in code (see [billing-service/internal/plans]);
// this table stores the `plan_code` reference plus billing-cycle
// metadata.
//
// Status semantics:
//   - active: subscription is in good standing; user has plan
//     entitlements until current_period_end.
//   - canceled: user asked to end. Entitlements remain until
//     current_period_end; the row is kept for audit + reactivation.
//   - past_due: payment failed. Entitlements depend on retry
//     policy; v0 keeps the user on their plan for a 7-day grace
//     window enforced by Stripe + a billing-service consumer
//     (not yet implemented).
//
// One row per user_id (uniquely indexed) — a user can't be on
// two plans simultaneously. To switch plans, billing-service
// UPDATES the row in place and records the change in an
// `analytics-service` audit event (per plan §3.10).
type Subscription struct {
	ID                 uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	UserID             uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_sub_user" json:"userId"`
	PlanCode           string    `gorm:"type:text;not null;index:idx_sub_plan" json:"planCode"`
	Status             string    `gorm:"type:text;not null;default:'active'" json:"status"`
	Seats              int       `gorm:"not null;default:1" json:"seats"`
	CurrentPeriodStart time.Time `gorm:"not null" json:"currentPeriodStart"`
	CurrentPeriodEnd   time.Time `gorm:"not null" json:"currentPeriodEnd"`
	// StripeSubscriptionID is populated when the Stripe
	// integration lands. Nullable in v0 (self-serve plan picks
	// are recorded without a payment intent).
	StripeSubscriptionID *string `gorm:"type:text;uniqueIndex:idx_sub_stripe,where:stripe_subscription_id IS NOT NULL" json:"stripeSubscriptionId,omitempty"`
	// StripeCustomerID is the Stripe-side customer identifier
	// (`cus_...`). Indexed because webhook handlers receive
	// events keyed by customer and need to look the row up
	// quickly. Nullable for the same reason as
	// StripeSubscriptionID — self-serve free-plan rows have
	// no Stripe presence.
	StripeCustomerID *string   `gorm:"type:text;index:idx_sub_stripe_customer,where:stripe_customer_id IS NOT NULL" json:"stripeCustomerId,omitempty"`
	CreatedAt        time.Time `gorm:"default:CURRENT_TIMESTAMP" json:"createdAt"`
	UpdatedAt        time.Time `gorm:"default:CURRENT_TIMESTAMP" json:"updatedAt"`
}

// BeforeCreate fills in the primary key + sets a sensible
// default billing period when the caller forgot. The default is
// "now" → "first day of next UTC month" — matches the standard
// monthly billing cycle for self-serve subscriptions.
func (s *Subscription) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.Must(uuid.NewV7())
	}
	if s.CurrentPeriodStart.IsZero() {
		s.CurrentPeriodStart = time.Now().UTC()
	}
	if s.CurrentPeriodEnd.IsZero() {
		s.CurrentPeriodEnd = nextMonthStart(s.CurrentPeriodStart)
	}
	if s.Seats < 1 {
		s.Seats = 1
	}
	return nil
}

// nextMonthStart returns 00:00 UTC on the first day of the month
// after `t`. Calendar-arithmetic correct: handles Dec→Jan + year
// rollover and DST transitions (UTC has none).
func nextMonthStart(t time.Time) time.Time {
	y, m, _ := t.UTC().Date()
	return time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
}

const (
	SubStatusActive   = "active"
	SubStatusCanceled = "canceled"
	SubStatusPastDue  = "past_due"
)
