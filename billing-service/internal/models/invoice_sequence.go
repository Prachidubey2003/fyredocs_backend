package models

import "time"

// InvoiceSequence is the per-(prefix, period) counter table
// the [invoice.NextNumber] helper increments to emit unique
// invoice numbers like `FYR-2026-0042`.
//
// Schema choices:
//   - The primary key is the composite `(prefix, period)` —
//     enables `INSERT ... ON CONFLICT (prefix, period) DO
//     UPDATE ... RETURNING next_seq` for an atomic
//     increment-or-create in one statement. Concurrent
//     callers serialise on the row's lock, so two parallel
//     `NextNumber("FYR", "2026")` calls observe distinct
//     sequence values.
//   - `next_seq` is BIGINT — supports the lifetime of any
//     conceivable Fyredocs (2^63 invoices per period is
//     ~9 quintillion).
//   - No `created_at` / `updated_at` — the row's job is to
//     hand out monotonic values, not to be queried by time.
//     Adding them later is a trivial migration if needed.
//
// One row per `(prefix, period)`:
//   - `FYR`, `2026`     — yearly subscription invoices.
//   - `FYR-Q`, `2026Q4` — future quarterly billing.
//   - `MKT`, `2026`     — marketplace payout statements.
type InvoiceSequence struct {
	// Prefix is the identifier family (`FYR` for Fyredocs
	// subscriptions, `MKT` for marketplace payouts). Caller
	// supplies; the helper just glues it.
	Prefix string `gorm:"type:text;not null;primaryKey;index:idx_invseq_prefix_period,priority:1" json:"prefix"`

	// Period is the time bucket the sequence resets in
	// (e.g., `2026` for yearly, `2026-04` for monthly). The
	// caller decides the resolution; the helper just stores
	// the string.
	Period string `gorm:"type:text;not null;primaryKey;index:idx_invseq_prefix_period,priority:2" json:"period"`

	// NextSeq is the value the next NextNumber call will
	// return AND store as incremented. Starts at 1 on first
	// allocation; tests pre-set it to drive specific values.
	NextSeq int64 `gorm:"not null;default:1" json:"nextSeq"`

	UpdatedAt time.Time `gorm:"default:CURRENT_TIMESTAMP" json:"updatedAt"`
}

// TableName pins the table name across struct renames.
func (InvoiceSequence) TableName() string { return "invoice_sequences" }
