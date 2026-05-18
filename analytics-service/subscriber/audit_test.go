package subscriber

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"fyredocs/shared/queue"

	"analytics-service/internal/audit"
	"analytics-service/internal/models"
)

// setupAuditDB swaps models.DB for in-memory sqlite with the
// AuditEvent table migrated. SQLite doesn't honour `FOR UPDATE`
// row locks (it's a single-writer DB) — that's fine for the
// chain logic tests, which exercise the Compute / link math, not
// the serialisation primitive itself. The Postgres production
// path's lock behaviour is left to integration tests.
func setupAuditDB(t *testing.T) func() {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.AuditEvent{}); err != nil {
		t.Fatalf("migrate AuditEvent: %v", err)
	}
	prev := models.DB
	models.DB = db
	return func() { models.DB = prev }
}

func TestAppendAudit_WritesGenesisRowWithZeroPrevHash(t *testing.T) {
	defer setupAuditDB(t)()
	err := AppendAudit(models.DB, queue.AuditEvent{
		Actor:      "user-1",
		Action:     "auth.login",
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	var rows []models.AuditEvent
	if err := models.DB.Find(&rows).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if len(rows[0].PrevHash) != 32 {
		t.Errorf("PrevHash length = %d, want 32", len(rows[0].PrevHash))
	}
	// Genesis prev_hash is all zero.
	for _, b := range rows[0].PrevHash {
		if b != 0 {
			t.Errorf("genesis PrevHash has non-zero byte: %x", rows[0].PrevHash)
			break
		}
	}
}

func TestAppendAudit_ChainsPrevHashAcrossAppends(t *testing.T) {
	defer setupAuditDB(t)()
	for _, action := range []string{"auth.login", "doc.edit", "key.revoke"} {
		if err := AppendAudit(models.DB, queue.AuditEvent{
			Actor: "user-1", Action: action,
		}); err != nil {
			t.Fatalf("AppendAudit(%s): %v", action, err)
		}
	}
	var rows []models.AuditEvent
	_ = models.DB.Order("seq ASC").Find(&rows).Error
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// Row 2's prev_hash must equal row 1's hash; row 3's
	// prev_hash must equal row 2's hash.
	for i := 1; i < len(rows); i++ {
		if string(rows[i].PrevHash) != string(rows[i-1].Hash) {
			t.Errorf("row %d prev_hash != row %d hash", rows[i].Seq, rows[i-1].Seq)
		}
	}
}

func TestAppendAudit_ChainPassesVerifier(t *testing.T) {
	// End-to-end: append three rows, then Verify the chain.
	defer setupAuditDB(t)()
	for i := 0; i < 3; i++ {
		md, _ := json.Marshal(map[string]int{"i": i})
		_ = AppendAudit(models.DB, queue.AuditEvent{
			Actor:    "user-1",
			Action:   "doc.edit",
			Resource: "doc-abc",
			Metadata: md,
		})
	}
	var rows []models.AuditEvent
	_ = models.DB.Order("seq ASC").Find(&rows).Error
	verifyRows := make([]audit.Row, len(rows))
	for i, r := range rows {
		verifyRows[i] = audit.Row{
			Seq: r.Seq, Actor: r.Actor, Action: r.Action,
			Resource: r.Resource, Metadata: []byte(r.Metadata),
			PrevHash: r.PrevHash, Hash: r.Hash,
		}
	}
	res := audit.Verify(verifyRows)
	if !res.OK {
		t.Errorf("freshly-appended chain failed Verify: %+v", res)
	}
}

func TestAppendAudit_PreservesActorAndActionVerbatim(t *testing.T) {
	defer setupAuditDB(t)()
	_ = AppendAudit(models.DB, queue.AuditEvent{
		Actor:    "11111111-1111-1111-1111-111111111111",
		Action:   "plan.changed",
		Resource: "user-1",
		Metadata: []byte(`{"oldPlan":"free","newPlan":"pro"}`),
	})
	var r models.AuditEvent
	_ = models.DB.First(&r).Error
	if r.Actor != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("Actor = %q, want UUID-format", r.Actor)
	}
	if r.Action != "plan.changed" {
		t.Errorf("Action = %q, want plan.changed", r.Action)
	}
	if !strings.Contains(string(r.Metadata), "newPlan") {
		t.Errorf("Metadata roundtrip lost newPlan: %s", r.Metadata)
	}
}

func TestAppendAudit_HashStableAcrossReadback(t *testing.T) {
	// Append a row, read it back, recompute the hash from the
	// stored fields. The recomputed value must equal the stored
	// Hash — i.e., the persistence path is lossless for the
	// fields that feed into the digest.
	defer setupAuditDB(t)()
	_ = AppendAudit(models.DB, queue.AuditEvent{
		Actor: "u1", Action: "doc.edit",
		Resource: "doc-1", Metadata: []byte(`{"v":1}`),
	})
	var r models.AuditEvent
	_ = models.DB.First(&r).Error
	want := audit.Compute(r.Seq, r.Actor, r.Action, r.Resource, []byte(r.Metadata), r.PrevHash)
	if string(want) != string(r.Hash) {
		t.Errorf("stored Hash %x diverges from recomputed %x", r.Hash, want)
	}
}
