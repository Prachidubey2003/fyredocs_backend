package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"analytics-service/internal/models"
)

// setupUsageTestDB swaps `models.DB` for an in-memory sqlite
// instance with UsageEvent migrated. Returns the DB handle so
// tests can seed it directly, and a restore func to put back
// the prior `models.DB` value (nil in CI; a real Postgres
// connection in dev).
func setupUsageTestDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.UsageEvent{}); err != nil {
		t.Fatalf("migrate UsageEvent: %v", err)
	}
	prev := models.DB
	models.DB = db
	return db, func() { models.DB = prev }
}

// newGinRouter returns a Gin engine wired with the usage routes
// under test. Avoids pulling in `routes.SetupRouter` (which adds
// the admin auth chain we don't need here).
func newGinRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/usage/me", UsageMe)
	r.GET("/internal/v1/usage/:userID", UsageByUser)
	return r
}

func TestResolvePeriod_DefaultsToCurrentMonth(t *testing.T) {
	got, err := resolvePeriod("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Now().UTC().Format("2006-01")
	if got != want {
		t.Errorf("resolvePeriod(\"\") = %q, want %q", got, want)
	}
}

func TestResolvePeriod_AcceptsValidYYYYMM(t *testing.T) {
	for _, s := range []string{"2026-01", "2026-05", "2025-12", "2024-02"} {
		got, err := resolvePeriod(s)
		if err != nil {
			t.Errorf("resolvePeriod(%q) returned error: %v", s, err)
			continue
		}
		if got != s {
			t.Errorf("resolvePeriod(%q) = %q, want %q (no normalisation should happen)", s, got, s)
		}
	}
}

func TestResolvePeriod_RejectsMalformedInput(t *testing.T) {
	bad := []string{
		"2026",        // missing month
		"2026-5",      // single-digit month
		"2026-13",     // invalid month
		"2026-00",     // zero month
		"26-05",       // 2-digit year
		"2026/05",     // wrong separator
		"2026-05-01",  // a full date, not a period
		"abc",         // gibberish
		"2026-05/etc", // path-traversal attempt
	}
	for _, s := range bad {
		if _, err := resolvePeriod(s); err == nil {
			t.Errorf("resolvePeriod(%q): got nil error, want non-nil", s)
		}
	}
}

func TestUsageMe_RequiresXUserIDHeader(t *testing.T) {
	_, restore := setupUsageTestDB(t)
	defer restore()

	r := newGinRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/usage/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (no X-User-ID header)", w.Code)
	}
}

func TestUsageMe_RejectsMalformedUserID(t *testing.T) {
	_, restore := setupUsageTestDB(t)
	defer restore()

	r := newGinRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/usage/me", nil)
	req.Header.Set("X-User-ID", "not-a-uuid")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (malformed user header)", w.Code)
	}
}

func TestUsageMe_RejectsMalformedPeriod(t *testing.T) {
	_, restore := setupUsageTestDB(t)
	defer restore()

	r := newGinRouter()
	uid := uuid.New().String()
	req := httptest.NewRequest(http.MethodGet, "/v1/usage/me?period=abc", nil)
	req.Header.Set("X-User-ID", uid)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (malformed period)", w.Code)
	}
}

func TestUsageMe_AggregatesEventsForCallingUser(t *testing.T) {
	db, restore := setupUsageTestDB(t)
	defer restore()

	user := uuid.New()
	other := uuid.New()
	period := "2026-05"
	periodStart, _ := time.Parse("2006-01", period)

	// Seed: 3 merge ops + 2 ocr ops (50pp each) for our user, all
	// in May 2026. Plus 1 merge for `other` to ensure we don't
	// bleed across users.
	seed := []models.UsageEvent{
		{UserID: user, EventType: "op.merge", Quantity: 1, Unit: "ops", OccurredAt: periodStart.AddDate(0, 0, 1)},
		{UserID: user, EventType: "op.merge", Quantity: 1, Unit: "ops", OccurredAt: periodStart.AddDate(0, 0, 5)},
		{UserID: user, EventType: "op.merge", Quantity: 1, Unit: "ops", OccurredAt: periodStart.AddDate(0, 0, 9)},
		{UserID: user, EventType: "op.ocr", Quantity: 50, Unit: "pages", OccurredAt: periodStart.AddDate(0, 0, 2)},
		{UserID: user, EventType: "op.ocr", Quantity: 50, Unit: "pages", OccurredAt: periodStart.AddDate(0, 0, 10)},
		{UserID: other, EventType: "op.merge", Quantity: 1, Unit: "ops", OccurredAt: periodStart.AddDate(0, 0, 3)},
	}
	for _, e := range seed {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	r := newGinRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/usage/me?period="+period, nil)
	req.Header.Set("X-User-ID", user.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	got := decodeRollup(t, w.Body.String())
	if got.UserID != user.String() {
		t.Errorf("UserID echo = %q, want %q", got.UserID, user.String())
	}
	if got.Period != period {
		t.Errorf("Period echo = %q, want %q", got.Period, period)
	}
	// Items are ordered by (event_type, unit). With our seed:
	// op.merge/ops + op.ocr/pages.
	if len(got.Items) != 2 {
		t.Fatalf("Items = %d entries, want 2; got: %+v", len(got.Items), got.Items)
	}
	mergeRow := got.Items[0]
	if mergeRow.EventType != "op.merge" || mergeRow.Unit != "ops" {
		t.Errorf("Items[0] = %+v, want op.merge/ops", mergeRow)
	}
	if mergeRow.TotalQuantity != 3 || mergeRow.EventCount != 3 {
		t.Errorf("Items[0] TotalQuantity/EventCount = %d/%d, want 3/3", mergeRow.TotalQuantity, mergeRow.EventCount)
	}
	ocrRow := got.Items[1]
	if ocrRow.EventType != "op.ocr" || ocrRow.Unit != "pages" {
		t.Errorf("Items[1] = %+v, want op.ocr/pages", ocrRow)
	}
	if ocrRow.TotalQuantity != 100 || ocrRow.EventCount != 2 {
		t.Errorf("Items[1] TotalQuantity/EventCount = %d/%d, want 100/2", ocrRow.TotalQuantity, ocrRow.EventCount)
	}
}

func TestUsageMe_DoesNotLeakAcrossPeriods(t *testing.T) {
	db, restore := setupUsageTestDB(t)
	defer restore()

	user := uuid.New()
	may, _ := time.Parse("2006-01", "2026-05")
	apr, _ := time.Parse("2006-01", "2026-04")

	// One op in April + one in May for the same user. Querying
	// May must NOT include the April event.
	for _, e := range []models.UsageEvent{
		{UserID: user, EventType: "op.merge", Quantity: 1, Unit: "ops", OccurredAt: apr.AddDate(0, 0, 5)},
		{UserID: user, EventType: "op.merge", Quantity: 1, Unit: "ops", OccurredAt: may.AddDate(0, 0, 5)},
	} {
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	r := newGinRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/usage/me?period=2026-05", nil)
	req.Header.Set("X-User-ID", user.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	got := decodeRollup(t, w.Body.String())
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 item for May; got %d (%+v)", len(got.Items), got.Items)
	}
	if got.Items[0].TotalQuantity != 1 || got.Items[0].EventCount != 1 {
		t.Errorf("May rollup should be 1/1; got %d/%d", got.Items[0].TotalQuantity, got.Items[0].EventCount)
	}
}

func TestUsageByUser_InternalEndpointEchoesUserID(t *testing.T) {
	db, restore := setupUsageTestDB(t)
	defer restore()

	target := uuid.New()
	may, _ := time.Parse("2006-01", "2026-05")
	if err := db.Create(&models.UsageEvent{
		UserID: target, EventType: "op.merge", Quantity: 1, Unit: "ops",
		OccurredAt: may.AddDate(0, 0, 1),
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := newGinRouter()
	// NO X-User-ID header — internal endpoint reads from the path
	// param. This is the contract billing-service relies on.
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/usage/"+target.String()+"?period=2026-05", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	got := decodeRollup(t, w.Body.String())
	if got.UserID != target.String() {
		t.Errorf("UserID echo = %q, want %q", got.UserID, target.String())
	}
	if len(got.Items) != 1 || got.Items[0].TotalQuantity != 1 {
		t.Errorf("expected one rollup row with quantity 1; got %+v", got.Items)
	}
}

func TestUsageByUser_RejectsMalformedUserID(t *testing.T) {
	_, restore := setupUsageTestDB(t)
	defer restore()

	r := newGinRouter()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/usage/not-a-uuid", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (malformed userID path param)", w.Code)
	}
}

// --- helpers ---------------------------------------------------------------

// decodeRollup pulls the UsageRollupResponse out of the
// standard `{success, message, data}` envelope. Tests assert
// against the data payload directly.
func decodeRollup(t *testing.T, body string) UsageRollupResponse {
	t.Helper()
	var envelope struct {
		Success bool                `json:"success"`
		Message string              `json:"message"`
		Data    UsageRollupResponse `json:"data"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, body)
	}
	if !envelope.Success {
		t.Fatalf("response.Success = false; body: %s", body)
	}
	return envelope.Data
}
