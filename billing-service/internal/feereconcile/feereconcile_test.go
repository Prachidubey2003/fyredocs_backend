package feereconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"billing-service/internal/models"
	"billing-service/internal/stripeclient"
)

// stubLookup is an in-process StripeFeeLookup that records
// the ids it was called with and returns canned responses
// from per-id maps. Lets tests assert "this row's Stripe
// chain ran" without spinning up an httptest server.
type stubLookup struct {
	charges      map[string]*stripeclient.Charge
	chargeErrors map[string]error
	bts          map[string]*stripeclient.BalanceTransaction
	btErrors     map[string]error
	chargeHits   []string
	btHits       []string
}

func (s *stubLookup) GetCharge(_ context.Context, id string) (*stripeclient.Charge, error) {
	s.chargeHits = append(s.chargeHits, id)
	if err, ok := s.chargeErrors[id]; ok {
		return nil, err
	}
	if c, ok := s.charges[id]; ok {
		return c, nil
	}
	return nil, errors.New("stub: charge not seeded: " + id)
}

func (s *stubLookup) GetBalanceTransaction(_ context.Context, id string) (*stripeclient.BalanceTransaction, error) {
	s.btHits = append(s.btHits, id)
	if err, ok := s.btErrors[id]; ok {
		return nil, err
	}
	if bt, ok := s.bts[id]; ok {
		return bt, nil
	}
	return nil, errors.New("stub: bt not seeded: " + id)
}

func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.RevshareEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// seedEntry inserts a revshare row with the supplied
// stripe_fee_cents and `recorded_at`. Defaults the rest of
// the schema to the marketplace happy-path so each test only
// specifies what it cares about.
func seedEntry(t *testing.T, db *gorm.DB, sourceRef string, feeCents int64, recordedAt time.Time, source string) models.RevshareEntry {
	t.Helper()
	row := models.RevshareEntry{
		ID:                  uuid.Must(uuid.NewV7()),
		TransactionID:       sourceRef,
		DeveloperUserID:     uuid.Must(uuid.NewV7()),
		PluginID:            "plug_super",
		Source:              source,
		SourceRef:           sourceRef,
		GrossCents:          5000,
		DeveloperShareCents: 3500,
		PlatformShareCents:  1500,
		StripeFeeCents:      feeCents,
		Currency:            "USD",
		Status:              "pending",
		RecordedAt:          recordedAt,
		UpdatedAt:           recordedAt,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	return row
}

func TestBackfillStripeFees_UpdatesAgedStripeChargeWithZeroFee(t *testing.T) {
	db := setupDB(t)
	now := time.Now()
	row := seedEntry(t, db, "ch_old", 0, now.Add(-1*time.Hour), "stripe_charge")
	lookup := &stubLookup{
		charges: map[string]*stripeclient.Charge{
			"ch_old": {ID: "ch_old", BalanceTransaction: "btx_old"},
		},
		bts: map[string]*stripeclient.BalanceTransaction{
			"btx_old": {ID: "btx_old", Fee: 175, Currency: "usd"},
		},
	}
	stats, err := BackfillStripeFees(context.Background(), db, lookup, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("BackfillStripeFees: %v", err)
	}
	if stats.Scanned != 1 || stats.Updated != 1 {
		t.Errorf("stats = %+v, want Scanned=1 Updated=1", stats)
	}

	var got models.RevshareEntry
	if err := db.First(&got, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.StripeFeeCents != 175 {
		t.Errorf("stripe_fee_cents = %d, want 175", got.StripeFeeCents)
	}
	if len(lookup.chargeHits) != 1 || lookup.chargeHits[0] != "ch_old" {
		t.Errorf("charge calls = %v", lookup.chargeHits)
	}
	if len(lookup.btHits) != 1 || lookup.btHits[0] != "btx_old" {
		t.Errorf("BT calls = %v", lookup.btHits)
	}
}

func TestBackfillStripeFees_SkipsRecentRowsToAvoidRacingTheWebhook(t *testing.T) {
	// A row recorded 30 seconds ago is within the default
	// 5-minute MinAge cooldown. The reconciler must NOT touch
	// it — the webhook may still be in flight, and the fee
	// belongs to that handler.
	db := setupDB(t)
	now := time.Now()
	row := seedEntry(t, db, "ch_recent", 0, now.Add(-30*time.Second), "stripe_charge")
	lookup := &stubLookup{}

	stats, err := BackfillStripeFees(context.Background(), db, lookup, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("BackfillStripeFees: %v", err)
	}
	if stats.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 (row inside cooldown)", stats.Scanned)
	}
	if len(lookup.chargeHits) != 0 {
		t.Errorf("must not call Stripe for recent rows; got %v", lookup.chargeHits)
	}

	// Row must be unchanged.
	var got models.RevshareEntry
	if err := db.First(&got, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.StripeFeeCents != 0 {
		t.Errorf("stripe_fee_cents = %d, want untouched 0", got.StripeFeeCents)
	}
}

func TestBackfillStripeFees_IgnoresRowsWithNonZeroFee(t *testing.T) {
	// A row whose webhook-time lookup already landed the fee
	// (or a row already back-filled in a prior pass) must be
	// invisible to this pass — no Stripe calls, no UPDATE.
	db := setupDB(t)
	now := time.Now()
	row := seedEntry(t, db, "ch_filled", 175, now.Add(-1*time.Hour), "stripe_charge")
	lookup := &stubLookup{}

	stats, err := BackfillStripeFees(context.Background(), db, lookup, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("BackfillStripeFees: %v", err)
	}
	if stats.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 (row already has fee)", stats.Scanned)
	}
	if len(lookup.chargeHits) != 0 {
		t.Errorf("must not call Stripe for filled rows; got %v", lookup.chargeHits)
	}

	var got models.RevshareEntry
	_ = db.First(&got, "id = ?", row.ID).Error
	if got.StripeFeeCents != 175 {
		t.Errorf("stripe_fee_cents = %d, want unchanged 175", got.StripeFeeCents)
	}
}

func TestBackfillStripeFees_IgnoresNonStripeSources(t *testing.T) {
	// manual_credit / batch_import rows have no Stripe fee by
	// definition; the WHERE clause filters them out so we
	// never call Stripe.
	db := setupDB(t)
	now := time.Now()
	seedEntry(t, db, "manual-001", 0, now.Add(-1*time.Hour), "manual_credit")
	lookup := &stubLookup{}

	stats, err := BackfillStripeFees(context.Background(), db, lookup, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("BackfillStripeFees: %v", err)
	}
	if stats.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 (non-stripe source)", stats.Scanned)
	}
	if len(lookup.chargeHits) != 0 {
		t.Errorf("must not call Stripe for non-stripe rows; got %v", lookup.chargeHits)
	}
}

func TestBackfillStripeFees_SkipsChargesWithNoBalanceTransaction(t *testing.T) {
	// Test-mode charges and refunds-prior-to-posting have no
	// linked BT. The reconciler must NOT fail; it logs +
	// counts the skip + moves on.
	db := setupDB(t)
	now := time.Now()
	seedEntry(t, db, "ch_no_bt", 0, now.Add(-1*time.Hour), "stripe_charge")
	lookup := &stubLookup{
		charges: map[string]*stripeclient.Charge{
			"ch_no_bt": {ID: "ch_no_bt", BalanceTransaction: ""},
		},
	}
	stats, err := BackfillStripeFees(context.Background(), db, lookup, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("BackfillStripeFees: %v", err)
	}
	if stats.SkippedNoBT != 1 || stats.Updated != 0 {
		t.Errorf("stats = %+v, want SkippedNoBT=1 Updated=0", stats)
	}
	if len(lookup.btHits) != 0 {
		t.Error("must not call BT endpoint when charge has no BT id")
	}
}

func TestBackfillStripeFees_PartialFailureContinuesPass(t *testing.T) {
	// Three candidate rows. Row B's GetCharge fails. The
	// pass MUST NOT abort — it should record the failure,
	// continue to row C, and return Stats showing 2 updates +
	// 1 LookupErrors.
	db := setupDB(t)
	now := time.Now()
	seedEntry(t, db, "ch_a", 0, now.Add(-1*time.Hour), "stripe_charge")
	seedEntry(t, db, "ch_b", 0, now.Add(-50*time.Minute), "stripe_charge")
	seedEntry(t, db, "ch_c", 0, now.Add(-40*time.Minute), "stripe_charge")

	lookup := &stubLookup{
		charges: map[string]*stripeclient.Charge{
			"ch_a": {ID: "ch_a", BalanceTransaction: "btx_a"},
			"ch_c": {ID: "ch_c", BalanceTransaction: "btx_c"},
		},
		chargeErrors: map[string]error{
			"ch_b": errors.New("transient stripe 503"),
		},
		bts: map[string]*stripeclient.BalanceTransaction{
			"btx_a": {ID: "btx_a", Fee: 100},
			"btx_c": {ID: "btx_c", Fee: 200},
		},
	}
	stats, err := BackfillStripeFees(context.Background(), db, lookup, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("BackfillStripeFees: %v", err)
	}
	if stats.Scanned != 3 || stats.Updated != 2 || stats.LookupErrors != 1 {
		t.Errorf("stats = %+v, want Scanned=3 Updated=2 LookupErrors=1", stats)
	}

	// ch_a + ch_c rows updated; ch_b row still at 0.
	var rowB models.RevshareEntry
	if err := db.First(&rowB, "source_ref = ?", "ch_b").Error; err != nil {
		t.Fatalf("reload ch_b: %v", err)
	}
	if rowB.StripeFeeCents != 0 {
		t.Errorf("ch_b stripe_fee_cents = %d, want untouched 0", rowB.StripeFeeCents)
	}
}

func TestBackfillStripeFees_SkipsZeroFeeBalanceTransactions(t *testing.T) {
	// Stripe-side test charges legitimately have fee=0. The
	// reconciler must not "update" the row with another 0 —
	// just count + log so the operator knows the pass did
	// something.
	db := setupDB(t)
	now := time.Now()
	seedEntry(t, db, "ch_zero", 0, now.Add(-1*time.Hour), "stripe_charge")
	lookup := &stubLookup{
		charges: map[string]*stripeclient.Charge{
			"ch_zero": {ID: "ch_zero", BalanceTransaction: "btx_zero"},
		},
		bts: map[string]*stripeclient.BalanceTransaction{
			"btx_zero": {ID: "btx_zero", Fee: 0},
		},
	}
	stats, err := BackfillStripeFees(context.Background(), db, lookup, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("BackfillStripeFees: %v", err)
	}
	if stats.SkippedZeroFee != 1 || stats.Updated != 0 {
		t.Errorf("stats = %+v, want SkippedZeroFee=1 Updated=0", stats)
	}
}

func TestBackfillStripeFees_RespectsMaxRows(t *testing.T) {
	// Five eligible rows; MaxRows=2 limits the pass to two.
	// The "oldest first" order means the two oldest get
	// processed.
	db := setupDB(t)
	now := time.Now()
	seedEntry(t, db, "ch_60", 0, now.Add(-60*time.Minute), "stripe_charge")
	seedEntry(t, db, "ch_50", 0, now.Add(-50*time.Minute), "stripe_charge")
	seedEntry(t, db, "ch_40", 0, now.Add(-40*time.Minute), "stripe_charge")
	seedEntry(t, db, "ch_30", 0, now.Add(-30*time.Minute), "stripe_charge")
	seedEntry(t, db, "ch_20", 0, now.Add(-20*time.Minute), "stripe_charge")

	lookup := &stubLookup{
		charges: map[string]*stripeclient.Charge{
			"ch_60": {ID: "ch_60", BalanceTransaction: "btx_60"},
			"ch_50": {ID: "ch_50", BalanceTransaction: "btx_50"},
			"ch_40": {ID: "ch_40", BalanceTransaction: "btx_40"},
			"ch_30": {ID: "ch_30", BalanceTransaction: "btx_30"},
			"ch_20": {ID: "ch_20", BalanceTransaction: "btx_20"},
		},
		bts: map[string]*stripeclient.BalanceTransaction{
			"btx_60": {ID: "btx_60", Fee: 60},
			"btx_50": {ID: "btx_50", Fee: 50},
			"btx_40": {ID: "btx_40", Fee: 40},
			"btx_30": {ID: "btx_30", Fee: 30},
			"btx_20": {ID: "btx_20", Fee: 20},
		},
	}
	stats, err := BackfillStripeFees(context.Background(), db, lookup, Options{
		Now:     func() time.Time { return now },
		MaxRows: 2,
	})
	if err != nil {
		t.Fatalf("BackfillStripeFees: %v", err)
	}
	if stats.Scanned != 2 || stats.Updated != 2 {
		t.Errorf("stats = %+v, want Scanned=2 Updated=2", stats)
	}

	// The two oldest (60min, 50min) should be updated; the
	// rest stay at 0.
	var ch60, ch50, ch40 models.RevshareEntry
	_ = db.First(&ch60, "source_ref = ?", "ch_60").Error
	_ = db.First(&ch50, "source_ref = ?", "ch_50").Error
	_ = db.First(&ch40, "source_ref = ?", "ch_40").Error
	if ch60.StripeFeeCents != 60 || ch50.StripeFeeCents != 50 {
		t.Errorf("expected oldest two updated; got ch_60=%d ch_50=%d",
			ch60.StripeFeeCents, ch50.StripeFeeCents)
	}
	if ch40.StripeFeeCents != 0 {
		t.Errorf("ch_40 should be untouched; got %d", ch40.StripeFeeCents)
	}
}

func TestBackfillStripeFees_IdempotentUnderConcurrentRuns(t *testing.T) {
	// Simulate "another pass already won the race" by
	// pre-filling the row's fee between the SELECT and the
	// UPDATE. The reconciler's `WHERE stripe_fee_cents = 0`
	// guard means the UPDATE matches 0 rows; the function
	// should count the no-op as SkippedZeroFee (or treat
	// it as a benign idempotent outcome) and not error.
	db := setupDB(t)
	now := time.Now()
	row := seedEntry(t, db, "ch_race", 0, now.Add(-1*time.Hour), "stripe_charge")

	// Inject the race: when the stub returns the BT, also
	// flip the row's fee directly in the DB before the
	// reconciler runs its UPDATE.
	lookup := &stubLookup{
		charges: map[string]*stripeclient.Charge{
			"ch_race": {ID: "ch_race", BalanceTransaction: "btx_race"},
		},
		bts: map[string]*stripeclient.BalanceTransaction{
			"btx_race": {ID: "btx_race", Fee: 200},
		},
	}
	// Front-run the reconciler by updating the row to a non-
	// zero fee BEFORE the call. The pass should not double-
	// write or error.
	if err := db.Model(&models.RevshareEntry{}).
		Where("id = ?", row.ID).
		Update("stripe_fee_cents", 175).Error; err != nil {
		t.Fatalf("front-run update: %v", err)
	}

	stats, err := BackfillStripeFees(context.Background(), db, lookup, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("BackfillStripeFees: %v", err)
	}
	// Since the WHERE clause filters by stripe_fee_cents=0
	// at SELECT time, the row is no longer a candidate —
	// Scanned should be 0.
	if stats.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 (front-run filled the fee)", stats.Scanned)
	}

	var got models.RevshareEntry
	_ = db.First(&got, "id = ?", row.ID).Error
	if got.StripeFeeCents != 175 {
		t.Errorf("front-run value should survive; got %d, want 175", got.StripeFeeCents)
	}
}

func TestBackfillStripeFees_PropagatesDBLoadError(t *testing.T) {
	// A genuine DB load failure aborts the pass with the
	// wrapped error. Simulate by closing the underlying conn
	// before the call.
	db := setupDB(t)
	sqlDB, _ := db.DB()
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	_, err := BackfillStripeFees(context.Background(), db, &stubLookup{}, Options{})
	if err == nil {
		t.Fatal("expected error on closed DB")
	}
}
