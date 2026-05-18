package revshare

import (
	"errors"
	"strings"
	"testing"
)

// assertReconciles is the single invariant that holds across
// every Calculate output: developer + platform shares plus the
// Stripe fee that came off the top equal gross. If a split ever
// violates this, money has gone missing or duplicated — a hard
// regression. Used in every test that exercises Calculate.
func assertReconciles(t *testing.T, entry Entry) {
	t.Helper()
	got := entry.DeveloperShareCents + entry.PlatformShareCents + entry.StripeFeeCents
	if got != entry.GrossCents {
		t.Errorf("split does not reconcile: dev=%d + plat=%d + fee=%d = %d, want gross=%d",
			entry.DeveloperShareCents, entry.PlatformShareCents,
			entry.StripeFeeCents, got, entry.GrossCents)
	}
}

func newTx(gross, fee int64) Transaction {
	return Transaction{
		ID:              "ch_test_1",
		GrossCents:      gross,
		StripeFeeCents:  fee,
		Currency:        "USD",
		DeveloperUserID: "user_1",
		PluginID:        "plugin_a",
	}
}

// ---- Policy constructors ----------------------------------------------

func TestDefaultSplit_Is70_30PlatformAbsorbs(t *testing.T) {
	p := DefaultSplit()
	if p.DeveloperBps != 7000 {
		t.Errorf("DefaultSplit DeveloperBps = %d, want 7000", p.DeveloperBps)
	}
	if p.StripeFeeMode != FeePlatformAbsorbs {
		t.Errorf("DefaultSplit StripeFeeMode = %v, want FeePlatformAbsorbs", p.StripeFeeMode)
	}
}

func TestNewSplit_RejectsOutOfRangeBps(t *testing.T) {
	cases := []int{-1, 10001, -10000}
	for _, bps := range cases {
		if _, err := NewSplit(bps, 0, FeePlatformAbsorbs); err == nil {
			t.Errorf("NewSplit(%d) should reject; got nil error", bps)
		}
	}
}

func TestNewSplit_RejectsNegativeFloor(t *testing.T) {
	if _, err := NewSplit(7000, -1, FeePlatformAbsorbs); err == nil {
		t.Error("NewSplit with negative floor should reject")
	}
}

func TestNewSplit_RejectsUnknownFeeMode(t *testing.T) {
	if _, err := NewSplit(7000, 0, FeeMode(99)); err == nil {
		t.Error("NewSplit with unknown FeeMode should reject")
	}
}

func TestNewSplit_AcceptsBoundaryBps(t *testing.T) {
	for _, bps := range []int{0, 7000, 10000} {
		if _, err := NewSplit(bps, 0, FeePlatformAbsorbs); err != nil {
			t.Errorf("NewSplit(%d) returned %v, want ok", bps, err)
		}
	}
}

// ---- Calculate happy path --------------------------------------------

func TestCalculate_Default70_30NoFee(t *testing.T) {
	// $10.00 gross, no Stripe fee, default 70/30 split.
	got, err := Calculate(newTx(1000, 0), DefaultSplit())
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if got.DeveloperShareCents != 700 || got.PlatformShareCents != 300 {
		t.Errorf("got dev=%d plat=%d, want 700/300", got.DeveloperShareCents, got.PlatformShareCents)
	}
	if got.Status != StatusPending {
		t.Errorf("Status = %q, want pending", got.Status)
	}
	assertReconciles(t, got)
}

func TestCalculate_NormalisesCurrencyToUpper(t *testing.T) {
	tx := newTx(1000, 0)
	tx.Currency = " usd "
	got, err := Calculate(tx, DefaultSplit())
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if got.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", got.Currency)
	}
}

func TestCalculate_PropagatesAttributionFields(t *testing.T) {
	tx := newTx(2500, 0)
	tx.ID = "ch_xyz"
	tx.DeveloperUserID = "user_42"
	tx.PluginID = "plugin_workflow_recorder"
	got, _ := Calculate(tx, DefaultSplit())
	if got.TransactionID != "ch_xyz" {
		t.Errorf("TransactionID = %q, want ch_xyz", got.TransactionID)
	}
	if got.DeveloperUserID != "user_42" {
		t.Errorf("DeveloperUserID = %q", got.DeveloperUserID)
	}
	if got.PluginID != "plugin_workflow_recorder" {
		t.Errorf("PluginID = %q", got.PluginID)
	}
}

// ---- Rounding --------------------------------------------------------

func TestCalculate_OddCentRoundingFavoursPlatform(t *testing.T) {
	// $0.99 gross at 70%: 99 * 7000 / 10000 = 69.3 → 69 cents
	// to the developer (integer divide toward zero); platform
	// absorbs the 30¢ remainder. Sums to 99.
	got, _ := Calculate(newTx(99, 0), DefaultSplit())
	if got.DeveloperShareCents != 69 || got.PlatformShareCents != 30 {
		t.Errorf("got dev=%d plat=%d, want 69/30", got.DeveloperShareCents, got.PlatformShareCents)
	}
	assertReconciles(t, got)
}

func TestCalculate_ManyOddSumsAllReconcile(t *testing.T) {
	// Property-shaped: every gross from 1¢ to $9.99 reconciles
	// under the default split. Catches any rounding regression
	// that would leak fractional cents.
	for gross := int64(1); gross <= 999; gross++ {
		got, err := Calculate(newTx(gross, 0), DefaultSplit())
		if err != nil {
			t.Fatalf("gross=%d: %v", gross, err)
		}
		assertReconciles(t, got)
	}
}

// ---- Floor / minimum dev share ---------------------------------------

func TestCalculate_FloorBelowsDevToFullGross(t *testing.T) {
	// $0.99 gross, but the policy says "if the developer's
	// share would be below 100 cents, give them the whole
	// gross". Their share goes to 99; platform takes 0.
	policy, _ := NewSplit(7000, 100, FeePlatformAbsorbs)
	got, _ := Calculate(newTx(99, 0), policy)
	if got.DeveloperShareCents != 99 {
		t.Errorf("DeveloperShareCents = %d, want 99 (floor swap)", got.DeveloperShareCents)
	}
	if got.PlatformShareCents != 0 {
		t.Errorf("PlatformShareCents = %d, want 0 (floor swap)", got.PlatformShareCents)
	}
	assertReconciles(t, got)
}

func TestCalculate_FloorDoesNotTriggerWhenShareMeetsThreshold(t *testing.T) {
	// $2.00 gross, floor at $1.00. 70% = 140¢, well above
	// the floor — normal split applies.
	policy, _ := NewSplit(7000, 100, FeePlatformAbsorbs)
	got, _ := Calculate(newTx(200, 0), policy)
	if got.DeveloperShareCents != 140 {
		t.Errorf("DeveloperShareCents = %d, want 140 (normal split, above floor)", got.DeveloperShareCents)
	}
	assertReconciles(t, got)
}

// ---- Stripe fee absorption modes -------------------------------------

func TestCalculate_PlatformAbsorbsFee(t *testing.T) {
	// $10.00 gross, 50¢ Stripe fee, default policy
	// (platform absorbs). Developer gets clean 70% = $7.00;
	// platform gets $3.00 - $0.50 = $2.50; fee = $0.50.
	got, _ := Calculate(newTx(1000, 50), DefaultSplit())
	if got.DeveloperShareCents != 700 {
		t.Errorf("dev share = %d, want 700 (platform absorbs fee)", got.DeveloperShareCents)
	}
	if got.PlatformShareCents != 250 {
		t.Errorf("platform share = %d, want 250 (300 - 50 fee)", got.PlatformShareCents)
	}
	assertReconciles(t, got)
}

func TestCalculate_DeveloperAbsorbsFee(t *testing.T) {
	policy, _ := NewSplit(7000, 0, FeeDeveloperAbsorbs)
	got, _ := Calculate(newTx(1000, 50), policy)
	if got.DeveloperShareCents != 650 {
		t.Errorf("dev share = %d, want 650 (700 - 50 fee)", got.DeveloperShareCents)
	}
	if got.PlatformShareCents != 300 {
		t.Errorf("platform share = %d, want 300 (clean 30%%)", got.PlatformShareCents)
	}
	assertReconciles(t, got)
}

func TestCalculate_ProRataFee(t *testing.T) {
	// 50¢ Stripe fee, 70/30 split. Dev absorbs 70% of 50 = 35;
	// platform absorbs the remainder = 15.
	// Dev = 700 - 35 = 665; Platform = 300 - 15 = 285.
	policy, _ := NewSplit(7000, 0, FeeProRata)
	got, _ := Calculate(newTx(1000, 50), policy)
	if got.DeveloperShareCents != 665 {
		t.Errorf("dev share = %d, want 665 (700 - 35 pro-rata fee)", got.DeveloperShareCents)
	}
	if got.PlatformShareCents != 285 {
		t.Errorf("platform share = %d, want 285 (300 - 15 pro-rata fee)", got.PlatformShareCents)
	}
	assertReconciles(t, got)
}

func TestCalculate_ProRataFee_OddFeeRoundingFavoursPlatform(t *testing.T) {
	// 7¢ fee at 70/30: dev fee share = 7 * 7000 / 10000 = 4
	// (integer truncate); platform absorbs the remaining 3.
	policy, _ := NewSplit(7000, 0, FeeProRata)
	got, _ := Calculate(newTx(1000, 7), policy)
	// Dev = 700 - 4 = 696; Plat = 300 - 3 = 297; fee = 7.
	if got.DeveloperShareCents != 696 {
		t.Errorf("dev = %d, want 696", got.DeveloperShareCents)
	}
	if got.PlatformShareCents != 297 {
		t.Errorf("plat = %d, want 297", got.PlatformShareCents)
	}
	assertReconciles(t, got)
}

// ---- Clamps ----------------------------------------------------------

func TestCalculate_DeveloperShareClampsToZeroWhenFeeExceeds(t *testing.T) {
	// $1.00 gross, 80¢ Stripe fee, developer absorbs the fee.
	// 70% of gross = 70¢; minus 80¢ fee = -10¢. Clamp to 0;
	// platform absorbs the negative so totals reconcile.
	// Platform was getting 30¢; now eats the -10¢ → 20¢.
	policy, _ := NewSplit(7000, 0, FeeDeveloperAbsorbs)
	got, _ := Calculate(newTx(100, 80), policy)
	if got.DeveloperShareCents != 0 {
		t.Errorf("dev share = %d, want 0 (clamp)", got.DeveloperShareCents)
	}
	if got.PlatformShareCents != 20 {
		t.Errorf("platform share = %d, want 20 (absorbed -10 deficit)", got.PlatformShareCents)
	}
	assertReconciles(t, got)
}

func TestCalculate_PlatformShareClampsToZeroWhenFeeExceeds(t *testing.T) {
	// $1.00 gross, 50¢ Stripe fee, platform absorbs. Platform
	// share = 30 - 50 = -20. Clamp; developer absorbs the
	// shortfall: 70 + (-20) = 50.
	got, _ := Calculate(newTx(100, 50), DefaultSplit())
	if got.PlatformShareCents != 0 {
		t.Errorf("platform = %d, want 0 (clamp)", got.PlatformShareCents)
	}
	if got.DeveloperShareCents != 50 {
		t.Errorf("dev = %d, want 50 (absorbed deficit)", got.DeveloperShareCents)
	}
	assertReconciles(t, got)
}

// ---- Validation errors ----------------------------------------------

func TestCalculate_RejectsZeroGross(t *testing.T) {
	if _, err := Calculate(newTx(0, 0), DefaultSplit()); !errors.Is(err, ErrInvalidGross) {
		t.Errorf("err = %v, want ErrInvalidGross", err)
	}
}

func TestCalculate_RejectsNegativeGross(t *testing.T) {
	if _, err := Calculate(newTx(-100, 0), DefaultSplit()); !errors.Is(err, ErrInvalidGross) {
		t.Errorf("err = %v, want ErrInvalidGross", err)
	}
}

func TestCalculate_RejectsEmptyCurrency(t *testing.T) {
	tx := newTx(1000, 0)
	tx.Currency = ""
	if _, err := Calculate(tx, DefaultSplit()); !errors.Is(err, ErrEmptyCurrency) {
		t.Errorf("err = %v, want ErrEmptyCurrency", err)
	}
	tx.Currency = "   "
	if _, err := Calculate(tx, DefaultSplit()); !errors.Is(err, ErrEmptyCurrency) {
		t.Errorf("whitespace currency: err = %v, want ErrEmptyCurrency", err)
	}
}

func TestCalculate_RejectsNegativeFee(t *testing.T) {
	_, err := Calculate(newTx(1000, -1), DefaultSplit())
	if err == nil || !strings.Contains(err.Error(), "stripe fee") {
		t.Errorf("err = %v, want negative-fee rejection", err)
	}
}

func TestCalculate_RejectsFeeAboveGross(t *testing.T) {
	_, err := Calculate(newTx(100, 200), DefaultSplit())
	if err == nil || !strings.Contains(err.Error(), "exceeds gross") {
		t.Errorf("err = %v, want fee-exceeds-gross rejection", err)
	}
}

// ---- Boundary splits ------------------------------------------------

func TestCalculate_ZeroBpsGivesEverythingToPlatform(t *testing.T) {
	policy, _ := NewSplit(0, 0, FeePlatformAbsorbs)
	got, _ := Calculate(newTx(1000, 0), policy)
	if got.DeveloperShareCents != 0 || got.PlatformShareCents != 1000 {
		t.Errorf("0 bps split: dev=%d plat=%d, want 0/1000", got.DeveloperShareCents, got.PlatformShareCents)
	}
}

func TestCalculate_TenThousandBpsGivesEverythingToDeveloper(t *testing.T) {
	policy, _ := NewSplit(10000, 0, FeePlatformAbsorbs)
	got, _ := Calculate(newTx(1000, 0), policy)
	if got.DeveloperShareCents != 1000 || got.PlatformShareCents != 0 {
		t.Errorf("10000 bps split: dev=%d plat=%d, want 1000/0", got.DeveloperShareCents, got.PlatformShareCents)
	}
}

// ---- Determinism ----------------------------------------------------

func TestCalculate_IsDeterministic(t *testing.T) {
	// Same inputs → same outputs across many calls. Pure
	// function, no time-dep, no randomness — verify.
	tx := newTx(12345, 67)
	policy := DefaultSplit()
	first, _ := Calculate(tx, policy)
	for i := 0; i < 50; i++ {
		again, err := Calculate(tx, policy)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if again != first {
			t.Fatalf("iteration %d: entry diverged from first run (non-determinism): %+v vs %+v",
				i, again, first)
		}
	}
}
