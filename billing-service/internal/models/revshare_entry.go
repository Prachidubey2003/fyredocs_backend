package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// RevshareEntry is the persisted form of one
// [revshare.Entry](../../revshare/revshare.go) — a single
// gross-to-developer/platform split for one marketplace
// transaction. The Stripe-webhook handler runs
// `revshare.Calculate` on each successful charge then INSERTs
// one of these rows.
//
// Schema choices:
//   - `(source, source_ref)` is uniquely indexed where both
//     are non-empty so a Stripe-webhook redelivery doesn't
//     double-record an entry. The processed_stripe_events
//     table also dedupes at the event-id level; this is a
//     belt-and-suspenders guard at the row level for non-
//     Stripe sources (manual credits, batch imports).
//   - `status` is the lifecycle from [revshare.Status]: starts
//     at `pending`, promotes to `payable` after the
//     chargeback window, lands at `paid` after the Stripe
//     Connect transfer, can reverse to `reversed` on refund.
//   - All amounts are integer cents in the entry's currency
//     — matches the calculator's invariant that splits sum
//     exactly to gross (the platform absorbs sub-cent
//     rounding).
type RevshareEntry struct {
	ID uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`

	// TransactionID is the developer-supplied identifier for
	// the source transaction (typically the Stripe charge id
	// `ch_...` or PaymentIntent id `pi_...`). Carried through
	// from revshare.Transaction. Indexed because lookups by
	// transaction are the common audit path.
	TransactionID string `gorm:"type:text;not null;index:idx_revshare_tx" json:"transactionId"`

	// DeveloperUserID is the payee — the marketplace plugin
	// owner who earned the share. UUID-shaped on the wire +
	// in the model so we can JOIN to the users table on the
	// auth-service side without a string-cast.
	DeveloperUserID uuid.UUID `gorm:"type:uuid;not null;index:idx_revshare_dev" json:"developerUserId"`

	// PluginID identifies which marketplace plugin earned the
	// share. Plain text — the plugin marketplace's own id
	// scheme. Indexed alongside developer for the per-plugin
	// payout breakdown query.
	PluginID string `gorm:"type:text;not null;index:idx_revshare_dev_plugin" json:"pluginId"`

	// Source + SourceRef identify the external system that
	// authoritatively records this transaction. Used for
	// dedup on retry:
	//   - source = "stripe_charge", source_ref = "ch_..."
	//   - source = "manual_credit", source_ref = "<operator note id>"
	// Uniquely indexed together where source_ref is non-empty.
	Source    string `gorm:"type:text;not null;default:'stripe_charge';uniqueIndex:idx_revshare_source_ref,priority:1" json:"source"`
	SourceRef string `gorm:"type:text;uniqueIndex:idx_revshare_source_ref,priority:2,where:source_ref <> ''" json:"sourceRef,omitempty"`

	GrossCents          int64 `gorm:"not null" json:"grossCents"`
	DeveloperShareCents int64 `gorm:"not null" json:"developerShareCents"`
	PlatformShareCents  int64 `gorm:"not null" json:"platformShareCents"`
	StripeFeeCents      int64 `gorm:"not null;default:0" json:"stripeFeeCents"`

	// Currency is the ISO-4217 code (USD, EUR, …). Normalised
	// to uppercase by the persistence helper. Future per-
	// currency payout sums groupby on this.
	Currency string `gorm:"type:text;not null" json:"currency"`

	// Status: pending | payable | paid | reversed (see
	// revshare.Status doc-block for the lifecycle). Indexed
	// for the "what's payable right now" payout-run query.
	Status string `gorm:"type:text;not null;default:'pending';index:idx_revshare_status" json:"status"`

	RecordedAt time.Time `gorm:"default:CURRENT_TIMESTAMP;index:idx_revshare_recorded" json:"recordedAt"`
	UpdatedAt  time.Time `gorm:"default:CURRENT_TIMESTAMP" json:"updatedAt"`
}

// TableName pins the table name so a refactor of the Go
// struct doesn't silently migrate the table.
func (RevshareEntry) TableName() string { return "revshare_entries" }

// BeforeCreate assigns a v7 UUID + sensible defaults. Matches
// the convention used by every other model in this service.
func (r *RevshareEntry) BeforeCreate(_ *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.Must(uuid.NewV7())
	}
	if r.Source == "" {
		r.Source = "stripe_charge"
	}
	if r.Status == "" {
		r.Status = "pending"
	}
	return nil
}
