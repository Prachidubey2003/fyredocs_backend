// Package feereconcile back-fills `revshare_entries.stripe_fee_cents`
// for rows whose webhook-time balance_transaction lookup
// failed (4xx, 5xx, expired key, network blip). The webhook
// handler "falls open" on lookup failure — records the entry
// with fee=0 so the row is never lost — and this package is
// the second-pass that recovers the missed fee.
//
// Why a second pass at all (vs. blocking the webhook on the
// fee lookup): Stripe redelivers webhooks aggressively (every
// 1m for an hour, then exponentially backing off for 3 days).
// A flapping BT endpoint would cause permanent re-tries and a
// user-facing "earnings missing" experience — much worse than
// an under-stated fee that this pass corrects later. The
// webhook's job is to ANCHOR the entry; this pass's job is
// to ENRICH it.
//
// What this package does NOT do:
//   - Schedule itself. Ops invokes BackfillStripeFees (CLI
//     flag, admin endpoint, or — in a follow-up — a periodic
//     scheduler). Coupling the function to a cron schedule
//     would mix concerns and make the function untestable.
//   - Touch non-stripe revshare entries. `source = "manual_credit"`
//     etc. carry no Stripe fee by definition.
//   - Recover charge IDs from non-`ch_` source_refs. The
//     Stripe charge endpoint requires `ch_…` ids; rows whose
//     webhook recorded a PaymentIntent id (`pi_…`) need a
//     different chain that's tracked separately.
package feereconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"billing-service/internal/models"
	"billing-service/internal/stripeclient"
)

// Options tunes one reconciliation pass. Zero values map to
// safe defaults so callers can pass an empty Options struct
// in dev / smoke runs.
type Options struct {
	// MaxRows caps how many entries one pass will process.
	// Defaults to 100 if zero — keeps the worst-case Stripe
	// call count (2 × MaxRows) bounded per invocation. A
	// follow-up scheduler iterates passes when more work
	// remains.
	MaxRows int

	// MinAge is the lower bound on how old a row must be
	// before it's eligible for back-fill. Guards against
	// racing the webhook handler, which may still be in
	// flight when this pass runs. Defaults to 5 minutes —
	// well past Stripe's typical webhook delivery latency.
	MinAge time.Duration

	// Now is the reference time the MinAge cutoff is computed
	// from. Defaults to time.Now() — tests override it to
	// drive deterministic windows.
	Now func() time.Time
}

func (o Options) maxRows() int {
	if o.MaxRows <= 0 {
		return 100
	}
	return o.MaxRows
}

func (o Options) minAge() time.Duration {
	if o.MinAge <= 0 {
		return 5 * time.Minute
	}
	return o.MinAge
}

func (o Options) now() time.Time {
	if o.Now == nil {
		return time.Now()
	}
	return o.Now()
}

// StripeFeeLookup is the minimal Stripe surface the
// reconciler depends on. Lets tests inject an in-process
// stub without spinning up an httptest server — and lets a
// future refactor swap stripeclient.Client for a richer
// retrying wrapper without touching this package.
type StripeFeeLookup interface {
	GetCharge(ctx context.Context, chargeID string) (*stripeclient.Charge, error)
	GetBalanceTransaction(ctx context.Context, btxID string) (*stripeclient.BalanceTransaction, error)
}

// Stats is the per-pass summary returned by BackfillStripeFees.
// Logged + (in a future cycle) emitted as a metric.
type Stats struct {
	// Scanned is the number of candidate rows the SELECT
	// returned. Bounded by Options.MaxRows.
	Scanned int
	// Updated is rows where the back-fill landed a non-zero
	// fee.
	Updated int
	// SkippedNoBT is rows whose charge object had no
	// balance_transaction id (very old test charges,
	// refunds-prior-to-posting). Logged but not retried.
	SkippedNoBT int
	// SkippedZeroFee is rows where Stripe reported the fee
	// genuinely was zero (e.g. test-mode charges). The row's
	// stripe_fee_cents stays at 0 — we tag the audit log so a
	// future re-pass doesn't keep re-fetching.
	SkippedZeroFee int
	// LookupErrors is rows where one of the two Stripe calls
	// failed. The row is left as-is — the NEXT reconciliation
	// pass will retry. Logged per-row at Warn.
	LookupErrors int
}

// ErrUnsupportedSourceRef is returned when a candidate row's
// source_ref doesn't fit Stripe's charge-id shape (`ch_…`).
// The reconciler skips such rows in batch mode; callers using
// the per-row entry point see this error explicitly.
var ErrUnsupportedSourceRef = errors.New("feereconcile: source_ref is not a Stripe charge id")

// BackfillStripeFees runs one reconciliation pass.
//
// Algorithm:
//  1. SELECT up to Options.MaxRows revshare_entries where
//     source = "stripe_charge", stripe_fee_cents = 0,
//     source_ref starts with `ch_`, recorded_at < now -
//     Options.MinAge. Ordered by recorded_at ASC so the
//     oldest unresolved rows surface first.
//  2. For each row: GET /v1/charges/{ch_id} to recover the
//     balance_transaction id, then GET /v1/balance_transactions/{btx}
//     for the fee. Update the row with the recovered fee.
//  3. Return Stats summarising the pass.
//
// Failure isolation: a per-row Stripe error logs at Warn and
// continues; only DB-level failures abort the pass (returned
// as the function's error). This matches the webhook's
// fall-open stance — partial progress is better than blocking
// the whole pass.
func BackfillStripeFees(ctx context.Context, db *gorm.DB, lookup StripeFeeLookup, opts Options) (Stats, error) {
	var stats Stats
	cutoff := opts.now().Add(-opts.minAge())

	var rows []models.RevshareEntry
	err := db.WithContext(ctx).
		Where("source = ?", "stripe_charge").
		Where("stripe_fee_cents = ?", 0).
		Where("source_ref LIKE ?", "ch_%").
		Where("recorded_at < ?", cutoff).
		Order("recorded_at ASC").
		Limit(opts.maxRows()).
		Find(&rows).Error
	if err != nil {
		return stats, fmt.Errorf("feereconcile: load candidates: %w", err)
	}
	stats.Scanned = len(rows)

	for _, row := range rows {
		applied, err := backfillOne(ctx, db, lookup, row)
		switch {
		case err == nil:
			switch applied {
			case appliedFee:
				stats.Updated++
			case appliedSkippedNoBT:
				stats.SkippedNoBT++
			case appliedSkippedZeroFee:
				stats.SkippedZeroFee++
			}
		case errors.Is(err, ctx.Err()):
			// Caller canceled — return what we have, no error
			// (the cancellation is the caller's signal).
			return stats, nil
		default:
			stats.LookupErrors++
			slog.Warn("feereconcile: per-row back-fill failed; will retry on next pass",
				"entry_id", row.ID, "source_ref", row.SourceRef, "error", err)
		}
	}
	return stats, nil
}

// applied is the outcome of a single row's reconcile attempt.
// Internal type — collapses the per-row branches Stats counts.
type applied int

const (
	appliedFee applied = iota + 1
	appliedSkippedNoBT
	appliedSkippedZeroFee
)

func backfillOne(ctx context.Context, db *gorm.DB, lookup StripeFeeLookup, row models.RevshareEntry) (applied, error) {
	if !strings.HasPrefix(row.SourceRef, "ch_") {
		return 0, ErrUnsupportedSourceRef
	}

	charge, err := lookup.GetCharge(ctx, row.SourceRef)
	if err != nil {
		return 0, fmt.Errorf("get charge %s: %w", row.SourceRef, err)
	}
	if charge.BalanceTransaction == "" {
		// Test charges + refunds-prior-to-posting have no
		// linked BT. Nothing to back-fill; skip so the next
		// pass doesn't re-fetch.
		return appliedSkippedNoBT, nil
	}

	bt, err := lookup.GetBalanceTransaction(ctx, charge.BalanceTransaction)
	if err != nil {
		return 0, fmt.Errorf("get balance_transaction %s: %w", charge.BalanceTransaction, err)
	}
	if bt.Fee <= 0 {
		// Genuinely zero / negative fee (test-mode charges,
		// Stripe-side anomalies). Don't write 0 again — the
		// row already has stripe_fee_cents=0. Counted so an
		// operator can spot a pass that found nothing
		// payable.
		return appliedSkippedZeroFee, nil
	}

	// UPDATE with WHERE stripe_fee_cents = 0 makes the write
	// idempotent under concurrent reconciliation runs — a
	// second pass that races us sees fee != 0 and skips.
	res := db.WithContext(ctx).
		Model(&models.RevshareEntry{}).
		Where("id = ? AND stripe_fee_cents = ?", row.ID, 0).
		Updates(map[string]any{
			"stripe_fee_cents": bt.Fee,
			"updated_at":       time.Now(),
		})
	if res.Error != nil {
		return 0, fmt.Errorf("update entry %s: %w", row.ID, res.Error)
	}
	// res.RowsAffected == 0 means a concurrent run won the
	// race — treat as a successful idempotent no-op rather
	// than a counter bump.
	if res.RowsAffected == 0 {
		return appliedSkippedZeroFee, nil
	}
	return appliedFee, nil
}
