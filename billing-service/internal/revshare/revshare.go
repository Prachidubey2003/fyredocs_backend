// Package revshare is the revenue-share calculation library for
// the Fyredocs plugin marketplace, SDK referral programs, and any
// future flow where a transaction's gross amount must be split
// between a third-party developer and Fyredocs the platform.
//
// Per plan §3.8 + §7.4, the default split is 70% developer / 30%
// platform, applied to the gross amount BEFORE Stripe processing
// fees. The platform absorbs the Stripe fee by default — that's
// the standard marketplace ergonomics customers expect from
// stripe/connect-style flows.
//
// Design constraints:
//   - All money is in integer cents. No floats. No fractional
//     cents. Rounding is deterministic + tested.
//   - The library is pure: no DB, no HTTP, no time. It's a
//     calculator. The ledger row (Entry below) is a value type
//     the billing-service Stripe-webhook handler writes after
//     calling Calculate.
//   - Splits sum to the gross to the cent. If the percentage
//     produces a non-integer share, the developer gets the
//     rounded-down portion and the platform absorbs the
//     remainder — favours predictability for the developer
//     (they can pre-compute their share from the gross with the
//     same rounding rule).
package revshare

import (
	"errors"
	"fmt"
	"strings"
)

// SplitPolicy describes how a single transaction is divided.
// Construct via DefaultSplit() or NewSplit(); fields are
// public for transparency but should be set through the
// constructors so validation lands in one place.
type SplitPolicy struct {
	// DeveloperBps is the developer's share in basis points
	// (1/100 of a percent). 7000 = 70%. Must be in [0, 10000].
	// The platform's share is implicit: 10000 - DeveloperBps.
	DeveloperBps int

	// MinimumDeveloperShareCents is a floor below which the
	// transaction is NOT split — the developer gets the full
	// gross share they earned, and the platform's share goes
	// to zero. Used to avoid penalising tiny one-off
	// transactions (e.g., a $0.99 SDK template purchase where
	// 30% rounds to 30¢ and feels heavy). Zero = no floor.
	MinimumDeveloperShareCents int

	// StripeFeeMode controls who absorbs the per-charge
	// payment-processing fee Stripe levies on the gross. See
	// FeeMode constants below for semantics.
	StripeFeeMode FeeMode
}

// FeeMode enumerates who pays the Stripe processing fee.
type FeeMode int

const (
	// FeePlatformAbsorbs — the default. Stripe fee comes out
	// of the platform's share. Developer's split is computed
	// against the full gross. Matches the ergonomics customers
	// expect from Stripe Connect "transfer" model.
	FeePlatformAbsorbs FeeMode = iota

	// FeeProRata — both parties absorb the Stripe fee
	// proportional to their share. A developer with a 70%
	// share absorbs 70% of the fee. Useful for high-fee
	// payment methods (international cards) where the
	// platform doesn't want to eat asymmetric cost.
	FeeProRata

	// FeeDeveloperAbsorbs — the developer pays the Stripe fee
	// in full. Platform gets a clean 30% of gross; developer
	// gets 70% of gross minus the fee. Rarely used.
	FeeDeveloperAbsorbs
)

const (
	// DefaultDeveloperBps codifies the 70/30 split in plan §7.4.
	DefaultDeveloperBps = 7000
	// BpsScale is the basis-points denominator (10000 = 100%).
	BpsScale = 10000
)

// DefaultSplit returns the canonical marketplace split (70% dev /
// 30% platform, platform absorbs Stripe fees). Sales agreements
// that override either are constructed via NewSplit.
func DefaultSplit() SplitPolicy {
	return SplitPolicy{
		DeveloperBps:               DefaultDeveloperBps,
		MinimumDeveloperShareCents: 0,
		StripeFeeMode:              FeePlatformAbsorbs,
	}
}

// NewSplit constructs a SplitPolicy and validates it. Returns an
// error if the basis-points value falls outside [0, 10000] —
// negative or super-100% would silently invert the split.
func NewSplit(developerBps, minimumDevCents int, feeMode FeeMode) (SplitPolicy, error) {
	if developerBps < 0 || developerBps > BpsScale {
		return SplitPolicy{}, fmt.Errorf("revshare: developer bps %d outside [0, %d]", developerBps, BpsScale)
	}
	if minimumDevCents < 0 {
		return SplitPolicy{}, fmt.Errorf("revshare: minimumDevCents %d must be >= 0", minimumDevCents)
	}
	if feeMode < FeePlatformAbsorbs || feeMode > FeeDeveloperAbsorbs {
		return SplitPolicy{}, fmt.Errorf("revshare: unknown FeeMode %d", feeMode)
	}
	return SplitPolicy{
		DeveloperBps:               developerBps,
		MinimumDeveloperShareCents: minimumDevCents,
		StripeFeeMode:              feeMode,
	}, nil
}

// Transaction is the input to Calculate. Mirrors the shape we
// pull from a Stripe `charge.succeeded` webhook (or any other
// money-source upstream): gross amount, Stripe fee, currency,
// the developer + plugin attribution. ID fields stay strings so
// the calculator doesn't need to know UUID vs Stripe charge-id
// conventions.
type Transaction struct {
	// ID is the upstream identifier (Stripe charge id, internal
	// payout-source id, etc.). Carried through to the Entry so
	// the ledger can dedupe replays.
	ID string

	// GrossCents is what the customer paid, in the smallest unit
	// of Currency (cents for USD, pence for GBP, …). Must be
	// > 0.
	GrossCents int64

	// StripeFeeCents is the upstream processor fee, in the same
	// unit. Used only when SplitPolicy.StripeFeeMode != FeePlatformAbsorbs.
	// Pass 0 for non-Stripe transactions.
	StripeFeeCents int64

	// Currency is the ISO-4217 code (USD, EUR, GBP, …). The
	// calculator doesn't care about FX — every value in this
	// struct is denominated in this currency. We persist
	// Currency on the Entry so downstream payout systems know
	// what to wire.
	Currency string

	// DeveloperUserID, PluginID identify the attribution. Both
	// are persisted verbatim on the Entry; revshare itself
	// doesn't validate them.
	DeveloperUserID string
	PluginID        string
}

// Entry is the ledger row produced by Calculate. The
// billing-service Stripe-webhook handler writes one of these
// per successful charge into a `revshare_entries` Postgres
// table (migration tracked separately, lands with the plugin
// marketplace work). Status is initialised to StatusPending —
// promotion to Payable / Paid happens in a separate payout
// pipeline that runs on a Stripe Connect transfer cadence.
type Entry struct {
	TransactionID       string
	DeveloperUserID     string
	PluginID            string
	GrossCents          int64
	DeveloperShareCents int64
	PlatformShareCents  int64
	StripeFeeCents      int64
	Currency            string
	Status              Status
}

// Status is the lifecycle position of an Entry.
type Status string

const (
	// StatusPending — Calculate produced the entry; not yet
	// committed to the developer's payable balance.
	StatusPending Status = "pending"
	// StatusPayable — the entry has cleared whatever grace
	// period the platform applies (typically the chargeback
	// window — 60 days for cards) and is eligible for the next
	// payout run.
	StatusPayable Status = "payable"
	// StatusPaid — the entry has been transferred to the
	// developer's Stripe Connect account.
	StatusPaid Status = "paid"
	// StatusReversed — the underlying transaction was refunded
	// or charged back. The developer's payable balance is
	// debited; if the entry was already StatusPaid, the
	// claw-back lands in the next payout run.
	StatusReversed Status = "reversed"
)

// ErrInvalidGross is returned by Calculate for a non-positive
// gross amount. Callers map to a 400 / log + drop.
var ErrInvalidGross = errors.New("revshare: gross amount must be > 0")

// ErrEmptyCurrency is returned when Transaction.Currency is the
// empty string. We don't default to USD — currency mismatches
// cause hard-to-debug payout bugs, so refuse to guess.
var ErrEmptyCurrency = errors.New("revshare: currency is required")

// Calculate splits `tx` according to `policy` and returns the
// resulting Entry. Pure function — no IO, deterministic for
// fixed inputs. Rounding rule: integer division toward zero
// for the developer's share; the platform absorbs the
// remainder so the two shares always sum exactly to gross.
// Stripe fees are applied per policy.StripeFeeMode AFTER the
// gross split.
func Calculate(tx Transaction, policy SplitPolicy) (Entry, error) {
	if tx.GrossCents <= 0 {
		return Entry{}, ErrInvalidGross
	}
	if strings.TrimSpace(tx.Currency) == "" {
		return Entry{}, ErrEmptyCurrency
	}
	if tx.StripeFeeCents < 0 {
		return Entry{}, fmt.Errorf("revshare: stripe fee %d must be >= 0", tx.StripeFeeCents)
	}
	if tx.StripeFeeCents > tx.GrossCents {
		return Entry{}, fmt.Errorf("revshare: stripe fee %d exceeds gross %d", tx.StripeFeeCents, tx.GrossCents)
	}

	// 1. Compute the gross split via basis points, with
	// integer-divide-toward-zero on the developer's side. The
	// platform takes the remainder so the two always sum to
	// gross to the cent.
	devShare := (tx.GrossCents * int64(policy.DeveloperBps)) / BpsScale
	platformShare := tx.GrossCents - devShare

	// 2. Apply the minimum-floor rule. If the developer's
	// computed share is below the floor, swap to "developer
	// gets full gross, platform gets zero" so a tiny
	// transaction doesn't get split into a fraction-of-a-cent
	// asymmetric burden.
	if policy.MinimumDeveloperShareCents > 0 &&
		devShare < int64(policy.MinimumDeveloperShareCents) {
		devShare = tx.GrossCents
		platformShare = 0
	}

	// 3. Apply Stripe-fee absorption per the policy. The fee
	// comes out of one or both shares; whichever side
	// absorbs gets reduced, but the SUM (devShare +
	// platformShare + fee) still equals gross.
	switch policy.StripeFeeMode {
	case FeePlatformAbsorbs:
		platformShare -= tx.StripeFeeCents
	case FeeDeveloperAbsorbs:
		devShare -= tx.StripeFeeCents
	case FeeProRata:
		// Same integer-divide-toward-zero rule on the
		// developer's portion; platform absorbs any rounding
		// remainder so the totals still reconcile.
		devFee := (tx.StripeFeeCents * int64(policy.DeveloperBps)) / BpsScale
		platFee := tx.StripeFeeCents - devFee
		devShare -= devFee
		platformShare -= platFee
	}

	// 4. Clamp shares to non-negative. A Stripe fee that's
	// larger than someone's gross share would otherwise
	// produce a negative — clamp at zero and let the platform
	// eat the difference (the alternative is putting a
	// developer in debt over a single transaction, which is
	// hostile UX). Track the absorbed amount on the platform
	// side so the totals still reconcile.
	if devShare < 0 {
		platformShare += devShare // devShare is negative; subtracting it from platform = adding the negative back
		devShare = 0
	}
	if platformShare < 0 {
		// In FeePlatformAbsorbs mode on a transaction where
		// the fee exceeds the platform's share, we've
		// already taken it from devShare in the clamp above
		// — but if we got here without that path, take it
		// here. Total still reconciles because devShare and
		// platformShare always sum to (gross - fee).
		devShare += platformShare
		platformShare = 0
		if devShare < 0 {
			devShare = 0
		}
	}

	return Entry{
		TransactionID:       tx.ID,
		DeveloperUserID:     tx.DeveloperUserID,
		PluginID:            tx.PluginID,
		GrossCents:          tx.GrossCents,
		DeveloperShareCents: devShare,
		PlatformShareCents:  platformShare,
		StripeFeeCents:      tx.StripeFeeCents,
		Currency:            strings.ToUpper(strings.TrimSpace(tx.Currency)),
		Status:              StatusPending,
	}, nil
}
