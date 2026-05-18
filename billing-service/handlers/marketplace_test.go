package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"billing-service/internal/models"
)

// newMarketplaceRouter spins up a router with just the
// earnings route wired. Mirrors the per-feature router
// helpers used in this package's other test files.
func newMarketplaceRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/billing/me/marketplace-earnings", MarketplaceEarnings)
	return r
}

// seedEntry persists a revshare entry for the supplied dev
// + plugin + status. Tests use it to stage the table state
// before exercising the endpoint.
func seedEntry(t *testing.T, dev uuid.UUID, plugin, txID string, gross, devShare int64, status string, recordedAt time.Time) uuid.UUID {
	t.Helper()
	row := models.RevshareEntry{
		TransactionID:       txID,
		DeveloperUserID:     dev,
		PluginID:            plugin,
		Source:              "stripe_charge",
		SourceRef:           txID,
		GrossCents:          gross,
		DeveloperShareCents: devShare,
		PlatformShareCents:  gross - devShare,
		Currency:            "USD",
		Status:              status,
		RecordedAt:          recordedAt,
	}
	if err := models.DB.Create(&row).Error; err != nil {
		t.Fatalf("seedEntry: %v", err)
	}
	return row.ID
}

// envelope unwraps the {success, message, data} response.
type earningsEnvelope struct {
	Success bool                         `json:"success"`
	Message string                       `json:"message"`
	Data    MarketplaceEarningsResponse  `json:"data"`
}

// ---- happy path ----

func TestMarketplaceEarnings_ReturnsCallerEntriesOnly(t *testing.T) {
	defer setupTestDB(t)()
	r := newMarketplaceRouter()

	caller := uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7())

	now := time.Now().UTC()
	seedEntry(t, caller, "plug_a", "ch_1", 1000, 700, "pending", now)
	seedEntry(t, caller, "plug_a", "ch_2", 2000, 1400, "paid", now.Add(-time.Hour))
	seedEntry(t, other, "plug_a", "ch_3", 500, 350, "pending", now) // must NOT appear

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me/marketplace-earnings", nil)
	req.Header.Set("X-User-ID", caller.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var env earningsEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Data.Items) != 2 {
		t.Errorf("expected 2 caller items; got %d", len(env.Data.Items))
	}
	for _, item := range env.Data.Items {
		if item.TransactionID == "ch_3" {
			t.Errorf("leaked another user's entry: %v", item.ID)
		}
	}
	// Page-scoped total: 700 + 1400 = 2100.
	if env.Data.TotalEarnedCents != 2100 {
		t.Errorf("totalEarnedCents = %d, want 2100", env.Data.TotalEarnedCents)
	}
}

func TestMarketplaceEarnings_NewestFirst(t *testing.T) {
	defer setupTestDB(t)()
	r := newMarketplaceRouter()

	caller := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	// Seed out of order to defend against insert-order
	// coincidence — the ORDER BY must do the work.
	seedEntry(t, caller, "plug_a", "ch_old", 1000, 700, "paid", now.Add(-48*time.Hour))
	seedEntry(t, caller, "plug_a", "ch_new", 1000, 700, "paid", now)
	seedEntry(t, caller, "plug_a", "ch_mid", 1000, 700, "paid", now.Add(-24*time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me/marketplace-earnings", nil)
	req.Header.Set("X-User-ID", caller.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env earningsEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	want := []string{"ch_new", "ch_mid", "ch_old"}
	if len(env.Data.Items) != len(want) {
		t.Fatalf("expected %d items; got %d", len(want), len(env.Data.Items))
	}
	for i, w := range want {
		if env.Data.Items[i].TransactionID != w {
			t.Errorf("item %d transaction = %q, want %q", i, env.Data.Items[i].TransactionID, w)
		}
	}
}

func TestMarketplaceEarnings_CuratedShapeDoesNotLeakInternalFields(t *testing.T) {
	// platform_share_cents / stripe_fee_cents / source /
	// source_ref / developer_user_id MUST NOT appear in the
	// public response. A developer reading the endpoint
	// should see only their own monetary cut + lifecycle
	// state.
	defer setupTestDB(t)()
	r := newMarketplaceRouter()

	caller := uuid.Must(uuid.NewV7())
	seedEntry(t, caller, "plug_a", "ch_curated", 1000, 700, "paid", time.Now().UTC())

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me/marketplace-earnings", nil)
	req.Header.Set("X-User-ID", caller.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	for _, leak := range []string{"platformShareCents", "stripeFeeCents", "developerUserId", "sourceRef", `"source"`} {
		if strings.Contains(body, leak) {
			t.Errorf("response leaked internal field %q: %s", leak, body)
		}
	}
}

// ---- filters ----

func TestMarketplaceEarnings_FiltersByStatus(t *testing.T) {
	defer setupTestDB(t)()
	r := newMarketplaceRouter()

	caller := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	seedEntry(t, caller, "plug_a", "ch_pending", 1000, 700, "pending", now)
	seedEntry(t, caller, "plug_a", "ch_paid", 1000, 700, "paid", now)
	seedEntry(t, caller, "plug_a", "ch_payable", 1000, 700, "payable", now)

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me/marketplace-earnings?status=paid", nil)
	req.Header.Set("X-User-ID", caller.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env earningsEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data.Items) != 1 {
		t.Fatalf("expected 1 paid entry; got %d", len(env.Data.Items))
	}
	if env.Data.Items[0].Status != "paid" {
		t.Errorf("status = %q, want paid", env.Data.Items[0].Status)
	}
}

func TestMarketplaceEarnings_RejectsInvalidStatus(t *testing.T) {
	// Silently ignoring an unknown status value would mislead
	// developers — they'd see total earnings when they think
	// they're filtering. 400 is the loud signal.
	defer setupTestDB(t)()
	r := newMarketplaceRouter()

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me/marketplace-earnings?status=bogus", nil)
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INVALID_STATUS") {
		t.Errorf("expected INVALID_STATUS error code; got %s", w.Body.String())
	}
}

func TestMarketplaceEarnings_LimitDefault(t *testing.T) {
	defer setupTestDB(t)()
	r := newMarketplaceRouter()

	caller := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	// Seed 60 entries to verify the default 50-limit kicks in.
	for i := 0; i < 60; i++ {
		seedEntry(t, caller, "plug_a", "ch_"+strconv.Itoa(i), 100, 70, "paid",
			now.Add(-time.Duration(i)*time.Minute))
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me/marketplace-earnings", nil)
	req.Header.Set("X-User-ID", caller.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env earningsEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data.Items) != marketplaceEarningsDefaultLimit {
		t.Errorf("default limit not applied: got %d, want %d",
			len(env.Data.Items), marketplaceEarningsDefaultLimit)
	}
}

func TestMarketplaceEarnings_HonoursExplicitLimit(t *testing.T) {
	defer setupTestDB(t)()
	r := newMarketplaceRouter()

	caller := uuid.Must(uuid.NewV7())
	now := time.Now().UTC()
	for i := 0; i < 15; i++ {
		seedEntry(t, caller, "plug_a", "ch_"+strconv.Itoa(i), 100, 70, "paid",
			now.Add(-time.Duration(i)*time.Minute))
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me/marketplace-earnings?limit=5", nil)
	req.Header.Set("X-User-ID", caller.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env earningsEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data.Items) != 5 {
		t.Errorf("explicit limit not honoured: got %d, want 5", len(env.Data.Items))
	}
}

func TestMarketplaceEarnings_ClampsLimitToMax(t *testing.T) {
	// `?limit=10000` must clamp to the configured max so a
	// caller can't yank the entire ledger in one query.
	defer setupTestDB(t)()
	r := newMarketplaceRouter()

	caller := uuid.Must(uuid.NewV7())
	// Seed past the max so we'd see the clamp kick in even
	// if the limit were respected literally.
	now := time.Now().UTC()
	for i := 0; i < marketplaceEarningsMaxLimit+50; i++ {
		seedEntry(t, caller, "plug_a", "ch_"+strconv.Itoa(i), 100, 70, "paid",
			now.Add(-time.Duration(i)*time.Second))
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me/marketplace-earnings?limit=10000", nil)
	req.Header.Set("X-User-ID", caller.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env earningsEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data.Items) != marketplaceEarningsMaxLimit {
		t.Errorf("limit clamp didn't fire: got %d, want %d",
			len(env.Data.Items), marketplaceEarningsMaxLimit)
	}
}

// ---- auth ----

func TestMarketplaceEarnings_RejectsUnauthenticated(t *testing.T) {
	defer setupTestDB(t)()
	r := newMarketplaceRouter()

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me/marketplace-earnings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMarketplaceEarnings_EmptyResultForUserWithNoEntries(t *testing.T) {
	// Not an error — just a developer who hasn't earned
	// anything yet. Empty array + zero total.
	defer setupTestDB(t)()
	r := newMarketplaceRouter()

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me/marketplace-earnings", nil)
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var env earningsEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data.Items) != 0 {
		t.Errorf("expected empty items; got %d", len(env.Data.Items))
	}
	if env.Data.TotalEarnedCents != 0 {
		t.Errorf("expected zero total; got %d", env.Data.TotalEarnedCents)
	}
}
