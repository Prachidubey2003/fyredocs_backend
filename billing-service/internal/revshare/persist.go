package revshare

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"billing-service/internal/models"
)

// PersistOptions carries the per-call identity / source
// metadata that the calculator's pure Entry struct doesn't
// itself know about. The caller supplies them at INSERT time
// from the surrounding context (Stripe webhook payload,
// manual-credit API request, etc.).
type PersistOptions struct {
	// DeveloperUserID is the payee — UUID-shaped because we
	// JOIN to auth-service's users table on the audit side.
	// Required.
	DeveloperUserID uuid.UUID

	// Source identifies the external system that
	// authoritatively records the transaction. Defaults to
	// `stripe_charge` when empty — the common case.
	Source string

	// SourceRef is the external id (Stripe charge id `ch_...`,
	// PaymentIntent id `pi_...`, manual-credit note id). When
	// non-empty AND a row with the same (Source, SourceRef)
	// already exists, Record returns ErrDuplicateSource
	// instead of inserting — defends against a Stripe-webhook
	// redelivery double-recording the same charge.
	SourceRef string
}

// ErrDuplicateSource is returned by Record when the
// (Source, SourceRef) pair already has a row. The caller
// treats this as a successful no-op (the prior delivery
// already booked the entry).
var ErrDuplicateSource = errors.New("revshare: entry for this (source, source_ref) already exists")

// ErrMissingDeveloper is returned when PersistOptions doesn't
// supply a DeveloperUserID. The calculator's Entry carries a
// string-typed DeveloperUserID for backwards-compat with
// callers that don't have a UUID handle, but the persistence
// layer requires the real UUID — surface the mismatch loudly.
var ErrMissingDeveloper = errors.New("revshare: PersistOptions.DeveloperUserID is required")

// Record persists `entry` to the revshare_entries table. Pure
// from the caller's perspective: a duplicate (Source,
// SourceRef) returns ErrDuplicateSource instead of erroring,
// so the caller's flow doesn't have to know whether the
// dedup happened.
//
// Returns the persisted row's ID on success — needed when
// the caller wants to log it or correlate against the
// payout-run picking the entry up.
//
// Wraps the INSERT in `tx` if supplied (caller is in a
// transaction); otherwise opens its own short transaction
// for the existence check + insert. The two-step "look
// before you leap" preserves the dedup error without
// relying on string-matching the unique-constraint
// violation across drivers.
func Record(ctx context.Context, tx *gorm.DB, entry Entry, opts PersistOptions) (uuid.UUID, error) {
	if opts.DeveloperUserID == uuid.Nil {
		return uuid.Nil, ErrMissingDeveloper
	}
	if tx == nil {
		return uuid.Nil, errors.New("revshare: nil *gorm.DB passed to Record")
	}

	source := strings.TrimSpace(opts.Source)
	if source == "" {
		source = "stripe_charge"
	}
	sourceRef := strings.TrimSpace(opts.SourceRef)

	// Dedup check — only when we have a source_ref. Without
	// it the unique index doesn't fire, and the caller
	// presumably wants every call to land a row (manual
	// credits via operator action).
	if sourceRef != "" {
		var existing models.RevshareEntry
		err := tx.WithContext(ctx).
			Select("id").
			Where("source = ? AND source_ref = ?", source, sourceRef).
			First(&existing).Error
		if err == nil {
			return existing.ID, ErrDuplicateSource
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, err
		}
	}

	row := models.RevshareEntry{
		TransactionID:       entry.TransactionID,
		DeveloperUserID:     opts.DeveloperUserID,
		PluginID:            entry.PluginID,
		Source:              source,
		SourceRef:           sourceRef,
		GrossCents:          entry.GrossCents,
		DeveloperShareCents: entry.DeveloperShareCents,
		PlatformShareCents:  entry.PlatformShareCents,
		StripeFeeCents:      entry.StripeFeeCents,
		Currency:            strings.ToUpper(strings.TrimSpace(entry.Currency)),
		Status:              string(entry.Status),
	}
	if row.Status == "" {
		row.Status = string(StatusPending)
	}
	if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}
