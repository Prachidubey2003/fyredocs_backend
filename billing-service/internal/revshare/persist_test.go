package revshare

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"billing-service/internal/models"
)

func setupPersistDB(t *testing.T) *gorm.DB {
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

// sampleEntry returns a calculated Entry from the standard
// 70/30 split — the canonical happy-path fixture for the
// persistence tests.
func sampleEntry(t *testing.T) Entry {
	t.Helper()
	got, err := Calculate(Transaction{
		ID:              "ch_test_abc",
		DeveloperUserID: "ignored-by-persist", // Record reads opts.DeveloperUserID, not Entry's string
		PluginID:        "plug_super",
		GrossCents:      10000,
		Currency:        "usd",
	}, DefaultSplit())
	if err != nil {
		t.Fatalf("sample Calculate: %v", err)
	}
	return got
}

// ---- happy path ----

func TestRecord_PersistsEntry(t *testing.T) {
	db := setupPersistDB(t)
	dev := uuid.Must(uuid.NewV7())
	entry := sampleEntry(t)

	id, err := Record(context.Background(), db, entry, PersistOptions{
		DeveloperUserID: dev,
		Source:          "stripe_charge",
		SourceRef:       "ch_test_abc",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if id == uuid.Nil {
		t.Error("returned id is zero")
	}

	var row models.RevshareEntry
	if err := db.Where("id = ?", id).First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.TransactionID != "ch_test_abc" {
		t.Errorf("transaction_id = %q", row.TransactionID)
	}
	if row.DeveloperUserID != dev {
		t.Errorf("developer_user_id = %v, want %v", row.DeveloperUserID, dev)
	}
	if row.PluginID != "plug_super" {
		t.Errorf("plugin_id = %q", row.PluginID)
	}
	if row.GrossCents != 10000 {
		t.Errorf("gross_cents = %d", row.GrossCents)
	}
	// 70/30 default → 7000 dev / 3000 platform.
	if row.DeveloperShareCents != 7000 {
		t.Errorf("developer_share_cents = %d", row.DeveloperShareCents)
	}
	if row.PlatformShareCents != 3000 {
		t.Errorf("platform_share_cents = %d", row.PlatformShareCents)
	}
	if row.Currency != "USD" {
		t.Errorf("currency = %q (must be uppercased)", row.Currency)
	}
	if row.Status != string(StatusPending) {
		t.Errorf("status = %q, want pending", row.Status)
	}
}

func TestRecord_DefaultsSourceToStripeChargeWhenEmpty(t *testing.T) {
	db := setupPersistDB(t)
	dev := uuid.Must(uuid.NewV7())
	entry := sampleEntry(t)

	id, err := Record(context.Background(), db, entry, PersistOptions{
		DeveloperUserID: dev,
		Source:          "", // empty
		SourceRef:       "ch_default_test",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	var row models.RevshareEntry
	_ = db.Where("id = ?", id).First(&row).Error
	if row.Source != "stripe_charge" {
		t.Errorf("source = %q, want default stripe_charge", row.Source)
	}
}

// ---- dedup ----

func TestRecord_DedupsBySourceAndSourceRef(t *testing.T) {
	db := setupPersistDB(t)
	dev := uuid.Must(uuid.NewV7())
	entry := sampleEntry(t)

	id1, err := Record(context.Background(), db, entry, PersistOptions{
		DeveloperUserID: dev,
		Source:          "stripe_charge",
		SourceRef:       "ch_dedup_test",
	})
	if err != nil {
		t.Fatalf("first Record: %v", err)
	}

	// Second call with the same (source, source_ref) returns
	// ErrDuplicateSource + the original id — the caller treats
	// this as a no-op success.
	id2, err := Record(context.Background(), db, entry, PersistOptions{
		DeveloperUserID: dev,
		Source:          "stripe_charge",
		SourceRef:       "ch_dedup_test",
	})
	if !errors.Is(err, ErrDuplicateSource) {
		t.Errorf("expected ErrDuplicateSource on duplicate; got %v", err)
	}
	if id2 != id1 {
		t.Errorf("dedup returned id %v, want original %v", id2, id1)
	}

	// Only one row in the table.
	var count int64
	_ = db.Model(&models.RevshareEntry{}).Count(&count).Error
	if count != 1 {
		t.Errorf("expected 1 row after dedup; got %d", count)
	}
}

func TestRecord_DoesNotDedupWhenSourceRefIsEmpty(t *testing.T) {
	// Manual credits via operator action don't have a stable
	// external ref. Two such calls MUST both persist —
	// otherwise the operator's second click would silently
	// vanish.
	db := setupPersistDB(t)
	dev := uuid.Must(uuid.NewV7())
	entry := sampleEntry(t)

	for i := 0; i < 2; i++ {
		_, err := Record(context.Background(), db, entry, PersistOptions{
			DeveloperUserID: dev,
			Source:          "manual_credit",
			SourceRef:       "", // empty — should NOT trigger dedup
		})
		if err != nil {
			t.Fatalf("Record #%d: %v", i, err)
		}
	}
	var count int64
	_ = db.Model(&models.RevshareEntry{}).Count(&count).Error
	if count != 2 {
		t.Errorf("expected 2 rows for empty SourceRef; got %d", count)
	}
}

func TestRecord_AllowsSameSourceRefAcrossDifferentSources(t *testing.T) {
	// `ch_foo` from `stripe_charge` and `ch_foo` from
	// `manual_credit` are distinct — the unique index is
	// (source, source_ref), not source_ref alone.
	db := setupPersistDB(t)
	dev := uuid.Must(uuid.NewV7())
	entry := sampleEntry(t)

	if _, err := Record(context.Background(), db, entry, PersistOptions{
		DeveloperUserID: dev,
		Source:          "stripe_charge",
		SourceRef:       "ch_collide",
	}); err != nil {
		t.Fatalf("first Record: %v", err)
	}
	if _, err := Record(context.Background(), db, entry, PersistOptions{
		DeveloperUserID: dev,
		Source:          "manual_credit",
		SourceRef:       "ch_collide",
	}); err != nil {
		t.Fatalf("second Record (different source): %v", err)
	}
	var count int64
	_ = db.Model(&models.RevshareEntry{}).Count(&count).Error
	if count != 2 {
		t.Errorf("expected 2 rows across sources; got %d", count)
	}
}

// ---- validation rejections ----

func TestRecord_RejectsMissingDeveloperUserID(t *testing.T) {
	db := setupPersistDB(t)
	entry := sampleEntry(t)
	_, err := Record(context.Background(), db, entry, PersistOptions{
		DeveloperUserID: uuid.Nil,
		SourceRef:       "ch_no_dev",
	})
	if !errors.Is(err, ErrMissingDeveloper) {
		t.Errorf("expected ErrMissingDeveloper; got %v", err)
	}
}

func TestRecord_RejectsNilDB(t *testing.T) {
	entry := sampleEntry(t)
	_, err := Record(context.Background(), nil, entry, PersistOptions{
		DeveloperUserID: uuid.Must(uuid.NewV7()),
	})
	if err == nil || !errors.Is(err, errors.New("revshare: nil *gorm.DB passed to Record")) && err.Error() != "revshare: nil *gorm.DB passed to Record" {
		t.Errorf("expected nil-db error; got %v", err)
	}
}

// ---- status handling ----

func TestRecord_DefaultsStatusToPending(t *testing.T) {
	db := setupPersistDB(t)
	dev := uuid.Must(uuid.NewV7())
	entry := sampleEntry(t)
	entry.Status = "" // explicitly blank — Record should default

	id, err := Record(context.Background(), db, entry, PersistOptions{
		DeveloperUserID: dev,
		SourceRef:       "ch_status_default",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	var row models.RevshareEntry
	_ = db.Where("id = ?", id).First(&row).Error
	if row.Status != string(StatusPending) {
		t.Errorf("status = %q, want %q", row.Status, StatusPending)
	}
}

func TestRecord_PreservesNonDefaultStatus(t *testing.T) {
	// Backfill paths sometimes record entries directly at
	// StatusPaid (replaying historical payouts). Record must
	// honour the caller's Status, not clobber to pending.
	db := setupPersistDB(t)
	dev := uuid.Must(uuid.NewV7())
	entry := sampleEntry(t)
	entry.Status = StatusPaid

	id, err := Record(context.Background(), db, entry, PersistOptions{
		DeveloperUserID: dev,
		SourceRef:       "ch_backfill_paid",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	var row models.RevshareEntry
	_ = db.Where("id = ?", id).First(&row).Error
	if row.Status != string(StatusPaid) {
		t.Errorf("status = %q, want %q (caller's value preserved)", row.Status, StatusPaid)
	}
}
