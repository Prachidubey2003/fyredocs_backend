package invoice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"billing-service/internal/models"
)

// _ keeps the models import referenced through TableName().
var _ = models.InvoiceSequence{}

// NextNumber returns the next invoice number for the given
// `(prefix, period)` pair and atomically advances the
// counter so concurrent callers observe distinct values.
//
// Output shape: `{prefix}-{period}-{NNNN}` zero-padded to at
// least 4 digits (`FYR-2026-0042`). Past 9999 the digit count
// expands naturally — no truncation, no wraparound. The
// padding makes the common-case identifier visually stable
// (sortable as a string, columns align) without capping
// volume.
//
// Concurrency model:
//   - One row per `(prefix, period)` in `invoice_sequences`.
//   - The function runs an UPSERT (`INSERT ... ON CONFLICT
//     ... DO UPDATE ... RETURNING`) that's atomic on
//     Postgres. SQLite (test-only) achieves the same via
//     `INSERT ... ON CONFLICT DO UPDATE ... RETURNING next_seq`.
//   - The returned `nextSeq` is the value to USE for THIS
//     call; the helper persisted it as "the value the next
//     call will use" — so two parallel callers observe
//     consecutive distinct numbers.
//
// `tx` must be a gorm handle (the package-level models.DB or
// a transaction). Caller wraps in a transaction when the
// invoice INSERT and the sequence allocation must atomically
// either both happen or both fail; for fire-and-forget
// number generation, passing models.DB directly is fine.
//
// Errors:
//   - ErrEmptyPrefix / ErrEmptyPeriod — caller bug; pin the
//     contract.
//   - non-nil DB error — surface verbatim.
func NextNumber(ctx context.Context, tx *gorm.DB, prefix, period string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "", ErrEmptyPrefix
	}
	period = strings.TrimSpace(period)
	if period == "" {
		return "", ErrEmptyPeriod
	}
	if tx == nil {
		return "", errors.New("invoice: NextNumber requires a non-nil *gorm.DB")
	}

	// Atomic single-statement allocation. The UPSERT-with-
	// RETURNING pattern serialises across concurrent callers
	// AND hands back the post-write value in one round trip —
	// no SELECT-after-UPDATE race window. Postgres + SQLite
	// 3.35+ both support `RETURNING` on `ON CONFLICT DO
	// UPDATE`.
	//
	// First call for (prefix, period): INSERT with next_seq=1;
	// RETURNING reads 1.
	// Subsequent calls: OnConflict fires `next_seq + 1`;
	// RETURNING reads the incremented value.
	now := time.Now().UTC()
	var used int64
	err := tx.WithContext(ctx).Raw(
		`INSERT INTO invoice_sequences (prefix, period, next_seq, updated_at)
		 VALUES (?, ?, 1, ?)
		 ON CONFLICT (prefix, period) DO UPDATE
		   SET next_seq = invoice_sequences.next_seq + 1, updated_at = ?
		 RETURNING next_seq`,
		prefix, period, now, now,
	).Scan(&used).Error
	if err != nil {
		return "", err
	}
	if used == 0 {
		// Defence: RETURNING must always hand back a value.
		// A zero suggests a driver returned no row — fail
		// loud rather than emit a `FYR-2026-0000`.
		return "", errors.New("invoice: NextNumber RETURNING produced no row")
	}

	// Format with zero-padding to 4 digits. Past 9999 the
	// `%04d` directive widens naturally without truncating.
	return fmt.Sprintf("%s-%s-%04d", prefix, period, used), nil
}

// ErrEmptyPrefix / ErrEmptyPeriod gate the helper against
// caller bugs that would otherwise emit malformed numbers
// like `-2026-0001`.
var (
	ErrEmptyPrefix = errors.New("invoice: NextNumber prefix is required")
	ErrEmptyPeriod = errors.New("invoice: NextNumber period is required")
)
