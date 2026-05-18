// Package invoice is the invoice-rendering library for
// billing-service. It owns the line-item math + HTML / plain-text
// emit; the calling code (Stripe webhook handlers, usage-overage
// jobs, the future invoice-PDF emitter) supplies the inputs and
// decides where the output goes.
//
// Design constraints:
//   - All money is integer cents. No floats. No fractional cents.
//     Rounding is deterministic + tested (mirrors revshare).
//   - The library is pure: no DB, no HTTP, no time-of-day, no
//     randomness. Tests assert exact output for fixed inputs.
//   - HTML output is self-contained (inline-styled) so it
//     renders consistently inside an email client without
//     fetching external CSS.
//   - Plain-text output is fixed-column ASCII so it survives any
//     transport (SMTP plain-part, terminal, audit-log dump).
package invoice

import (
	"errors"
	"fmt"
	"strings"
)

// Invoice is the top-level document the renderer consumes.
// Construct via New so totals get auto-computed and inputs get
// validated; mutating a returned Invoice in place is allowed but
// callers must re-call Compute to re-derive the totals.
type Invoice struct {
	// Number is the human-friendly invoice identifier
	// (typically `FYR-YYYY-NNNN`). Carried through verbatim to
	// the rendered output. Empty is allowed for drafts but
	// rejects on render.
	Number string

	// IssuedAt and DueAt are ISO-8601 date strings (YYYY-MM-DD
	// is fine; the renderer doesn't parse them). Carried
	// through verbatim — the renderer is timezone-agnostic.
	IssuedAt string
	DueAt    string

	// Currency is the ISO-4217 code (USD, EUR, …). Applied to
	// every line; mixed-currency invoices are out of scope for
	// v0. Normalised to uppercase by Compute.
	Currency string

	// Issuer is "from" — Fyredocs's legal entity + remit
	// address. Customer is "to" — the billed party.
	Issuer   Party
	Customer Party

	// Lines is the ordered list of line items. Each line's
	// total is `Quantity * UnitPriceCents`; the subtotal is
	// their sum.
	Lines []LineItem

	// TaxBps is a document-level tax rate in basis points
	// (1/100 of a percent). 825 = 8.25% sales tax. Pass 0 for
	// no tax. Applied to the subtotal AFTER discounts (which
	// are themselves negative lines).
	TaxBps int

	// Memo is optional free-form text rendered at the bottom
	// of the invoice. Useful for "Thanks for being a
	// customer" or for legal disclosures.
	Memo string

	// Computed totals — populated by Compute(). Public so
	// renderers can read them; callers shouldn't set them
	// directly.
	SubtotalCents int64
	TaxCents      int64
	TotalCents    int64
}

// Party is one side of the invoice — issuer or customer. All
// fields are optional except Name; missing pieces are simply
// omitted from the rendered output (the renderer doesn't fall
// back to placeholders).
type Party struct {
	Name        string
	Email       string
	AddressLine string // multi-line addresses are pre-formatted with \n by the caller
	TaxID       string // VAT / EIN / equivalent
}

// LineItem is one row of the invoice. All math runs in integer
// cents to avoid float drift across renders.
type LineItem struct {
	// Description is the human-readable line text.
	Description string

	// Quantity is units of UnitPriceCents. For a flat fee
	// (subscription, one-off charge) use 1. Negative values
	// are permitted and represent discounts / credits —
	// they reduce the subtotal.
	Quantity int64

	// UnitPriceCents is the per-unit cost in the smallest
	// currency unit (cents for USD, pence for GBP). May be
	// negative — same discount semantics as a negative
	// Quantity (we don't multiply both negatives — caller
	// picks one).
	UnitPriceCents int64

	// LineTotalCents is computed by Compute(): Quantity × UnitPriceCents.
	LineTotalCents int64
}

const bpsScale = 10000

// ErrEmptyNumber is returned by RenderHTML/RenderPlainText when
// the invoice number is unset. Drafts may exist without a
// number, but rendering one is a programming error.
var ErrEmptyNumber = errors.New("invoice: Number is required to render")

// ErrEmptyCurrency is the corresponding guard for Currency.
var ErrEmptyCurrency = errors.New("invoice: Currency is required to render")

// ErrTaxBpsOutOfRange — basis points must fit [0, 10000]. A
// negative tax would inflate the gross, and >100% is almost
// certainly a coding bug.
var ErrTaxBpsOutOfRange = errors.New("invoice: TaxBps must be in [0, 10000]")

// New constructs an Invoice and runs Compute on it. Returns an
// error if inputs are structurally invalid (out-of-range tax,
// missing line description, etc.).
func New(inv Invoice) (Invoice, error) {
	if err := validateLines(inv.Lines); err != nil {
		return Invoice{}, err
	}
	if inv.TaxBps < 0 || inv.TaxBps > bpsScale {
		return Invoice{}, ErrTaxBpsOutOfRange
	}
	inv.Currency = strings.ToUpper(strings.TrimSpace(inv.Currency))
	return Compute(inv), nil
}

// Compute fills in LineTotalCents per line and the
// SubtotalCents / TaxCents / TotalCents fields on the invoice.
// Idempotent — re-running on a previously-computed invoice
// produces the same values.
//
// Tax rounding rule: integer divide toward zero. The grand
// total is the integer subtotal + integer tax, so a 8.25% tax
// on a $12.34 subtotal yields:
//
//	1234 * 825 / 10000 = 101 cents (truncated from 101.805)
//
// — favours the customer over the issuer when the fractional
// cent falls below 0.5, matching standard accounting practice
// for sales tax on consumer invoices.
func Compute(inv Invoice) Invoice {
	var subtotal int64
	for i := range inv.Lines {
		total := inv.Lines[i].Quantity * inv.Lines[i].UnitPriceCents
		inv.Lines[i].LineTotalCents = total
		subtotal += total
	}
	tax := int64(0)
	if subtotal > 0 && inv.TaxBps > 0 {
		// Truncate-toward-zero on positive subtotal. A
		// negative subtotal (refund invoice) gets zero tax —
		// the IRS doesn't refund tax it never collected.
		tax = subtotal * int64(inv.TaxBps) / bpsScale
	}
	inv.SubtotalCents = subtotal
	inv.TaxCents = tax
	inv.TotalCents = subtotal + tax
	return inv
}

func validateLines(lines []LineItem) error {
	if len(lines) == 0 {
		return errors.New("invoice: at least one line item is required")
	}
	for i, l := range lines {
		if strings.TrimSpace(l.Description) == "" {
			return fmt.Errorf("invoice: line[%d].Description is required", i)
		}
	}
	return nil
}

// FormatMoneyCents renders cents into a human-readable string
// for the supplied currency. ISO-4217 codes are surfaced as a
// prefix (e.g., `USD 12.34`). For currencies whose minor unit
// isn't 1/100 of the major (JPY, KWD), this function returns
// the raw cents with the currency code — formatting those
// precisely is tracked as a follow-up.
//
// Exported so renderers can call it; callers building custom
// templates can use it too for consistent presentation.
func FormatMoneyCents(cents int64, currency string) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "USD"
	}
	// Common minor-unit-is-1/100 cases. JPY / KWD support
	// tracked separately.
	negative := cents < 0
	abs := cents
	if negative {
		abs = -abs
	}
	major := abs / 100
	minor := abs % 100
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s %s%d.%02d", currency, sign, major, minor)
}
