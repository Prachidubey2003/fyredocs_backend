package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"analytics-service/internal/audit"
	"analytics-service/internal/models"
)

// setupAuditTestDB swaps `models.DB` for in-memory sqlite with
// the AuditEvent table migrated. Mirrors the pattern used in
// usage_test.go.
func setupAuditTestDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.AuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := models.DB
	models.DB = db
	return db, func() { models.DB = prev }
}

// seedChain writes `n` chained rows for `actor` directly via the
// model — bypasses the subscriber's AppendAudit so handler tests
// don't depend on the FOR UPDATE path.
func seedChain(t *testing.T, db *gorm.DB, actor string, n int) {
	t.Helper()
	prev := audit.GenesisPrevHash
	for i := 1; i <= n; i++ {
		md := []byte(`{}`)
		row := models.AuditEvent{
			Actor:      actor,
			Action:     "doc.edit",
			Resource:   "doc-1",
			Metadata:   md,
			PrevHash:   prev,
			Hash:       []byte{0}, // placeholder; rewritten after seq is assigned
			OccurredAt: time.Now().UTC(),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		row.Hash = audit.Compute(row.Seq, row.Actor, row.Action, row.Resource, md, row.PrevHash)
		if err := db.Model(&models.AuditEvent{}).Where("seq = ?", row.Seq).
			Update("hash", row.Hash).Error; err != nil {
			t.Fatalf("seed hash: %v", err)
		}
		prev = row.Hash
	}
}

func newAuditRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/audit/me", AuditMe)
	r.GET("/internal/v1/audit/verify", AuditVerify)
	return r
}

func TestAuditMe_RequiresXUserIDHeader(t *testing.T) {
	_, restore := setupAuditTestDB(t)
	defer restore()
	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAuditMe_FiltersToCallingActor(t *testing.T) {
	db, restore := setupAuditTestDB(t)
	defer restore()

	seedChain(t, db, "user-1", 2)
	seedChain(t, db, "user-2", 1)

	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/me", nil)
	req.Header.Set("X-User-ID", "user-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// Should contain user-1's rows but not user-2's.
	if !strings.Contains(body, `"actor":"user-1"`) {
		t.Errorf("user-1 rows missing: %s", body)
	}
	if strings.Contains(body, `"actor":"user-2"`) {
		t.Errorf("response leaked user-2's rows: %s", body)
	}
}

func TestAuditMe_FilterByAction(t *testing.T) {
	db, restore := setupAuditTestDB(t)
	defer restore()
	// Two actions for the same user.
	db.Create(&models.AuditEvent{Actor: "u1", Action: "auth.login", PrevHash: audit.GenesisPrevHash, Hash: []byte{1}})
	db.Create(&models.AuditEvent{Actor: "u1", Action: "doc.edit", PrevHash: audit.GenesisPrevHash, Hash: []byte{2}})

	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/me?action=auth.login", nil)
	req.Header.Set("X-User-ID", "u1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "auth.login") {
		t.Errorf("filter missed auth.login row: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "doc.edit") {
		t.Errorf("filter leaked doc.edit row: %s", w.Body.String())
	}
}

func TestAuditMe_HexEncodesDigests(t *testing.T) {
	db, restore := setupAuditTestDB(t)
	defer restore()
	seedChain(t, db, "u1", 1)

	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/me", nil)
	req.Header.Set("X-User-ID", "u1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env struct {
		Data struct {
			Items []struct {
				PrevHash string `json:"prevHash"`
				Hash     string `json:"hash"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(env.Data.Items))
	}
	// 32-byte sha256 → 64 hex chars; genesis prev is all zeros.
	if env.Data.Items[0].PrevHash != strings.Repeat("0", 64) {
		t.Errorf("genesis PrevHash = %q, want 64 zeros", env.Data.Items[0].PrevHash)
	}
	if len(env.Data.Items[0].Hash) != 64 {
		t.Errorf("Hash hex length = %d, want 64", len(env.Data.Items[0].Hash))
	}
}

func TestAuditVerify_ReturnsOKOnIntactChain(t *testing.T) {
	db, restore := setupAuditTestDB(t)
	defer restore()
	seedChain(t, db, "u1", 5)

	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/audit/verify", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("expected ok=true; got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"verified":5`) {
		t.Errorf("expected verified=5; got %s", w.Body.String())
	}
}

func TestAuditVerify_DetectsTamperedRow(t *testing.T) {
	db, restore := setupAuditTestDB(t)
	defer restore()
	seedChain(t, db, "u1", 5)

	// Tamper with row 3's actor without recomputing the hash —
	// the verifier's recompute must catch it.
	if err := db.Model(&models.AuditEvent{}).Where("seq = ?", 3).
		Update("actor", "imposter").Error; err != nil {
		t.Fatalf("tamper: %v", err)
	}

	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/audit/verify", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `"ok":false`) {
		t.Errorf("expected ok=false; got %s", body)
	}
	if !strings.Contains(body, `"brokenAtSeq":3`) {
		t.Errorf("expected brokenAtSeq=3; got %s", body)
	}
}

func TestAuditVerify_HandlesEmptyChain(t *testing.T) {
	_, restore := setupAuditTestDB(t)
	defer restore()
	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/audit/verify", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("empty chain should verify; got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"verified":0`) {
		t.Errorf("expected verified=0; got %s", w.Body.String())
	}
}

func TestAuditMe_HonoursExplicitLimit(t *testing.T) {
	// Seed 20 rows for the same actor; request limit=5 and
	// confirm exactly five surface. Pins the explicit-limit
	// path against a regression that drops the query into
	// the fallback branch.
	_, restore := setupAuditTestDB(t)
	defer restore()
	seedChain(t, models.DB, "u1", 20)

	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/me?limit=5", nil)
	req.Header.Set("X-User-ID", "u1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}

	var env struct {
		Data struct {
			Items []auditRow `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Items) != 5 {
		t.Errorf("items count = %d, want 5", len(env.Data.Items))
	}
}

func TestAuditMe_CapsLimitAt200(t *testing.T) {
	// Seed 250 rows; a `?limit=500` request must be capped
	// at 200 by the handler — protects the service from a
	// caller asking for an unbounded scan.
	_, restore := setupAuditTestDB(t)
	defer restore()
	seedChain(t, models.DB, "u1", 250)

	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/me?limit=500", nil)
	req.Header.Set("X-User-ID", "u1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var env struct {
		Data struct {
			Items []auditRow `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Items) != 200 {
		t.Errorf("items count = %d, want 200 (the hard cap)", len(env.Data.Items))
	}
}

func TestAuditMe_OrdersBySeqDescNewestFirst(t *testing.T) {
	// Newest first is documented contract; verify the rows
	// come back in descending seq order. A regression on
	// the ORDER BY clause (e.g., dropping it during a
	// refactor) would silently flip the UI's "Recent
	// activity" display to oldest-first.
	_, restore := setupAuditTestDB(t)
	defer restore()
	seedChain(t, models.DB, "u1", 5)

	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/me", nil)
	req.Header.Set("X-User-ID", "u1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env struct {
		Data struct {
			Items []auditRow `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Items) != 5 {
		t.Fatalf("expected 5 items, got %d", len(env.Data.Items))
	}
	for i := 1; i < len(env.Data.Items); i++ {
		if env.Data.Items[i-1].Seq <= env.Data.Items[i].Seq {
			t.Errorf("items not in DESC seq order at index %d: %d → %d",
				i-1, env.Data.Items[i-1].Seq, env.Data.Items[i].Seq)
		}
	}
}

func TestAuditMe_EmptyHistoryReturnsEmptyItems(t *testing.T) {
	// A user with no audit rows should get `items: []`
	// rather than `items: null` — the response shape MUST
	// stay stable so a frontend `.length` access doesn't
	// throw. Pins the make([]auditRow, 0, ...) allocation
	// against a refactor that drops the empty-slice init.
	_, restore := setupAuditTestDB(t)
	defer restore()
	// Don't seed anything.

	r := newAuditRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/me", nil)
	req.Header.Set("X-User-ID", "new-user")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"items":[]`) {
		t.Errorf("expected `items:[]` (empty JSON array, not null); got %s", body)
	}
}
