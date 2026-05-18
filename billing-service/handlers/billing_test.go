package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"billing-service/internal/models"
	"billing-service/internal/plans"
	"billing-service/internal/usageclient"
)

// stubUsage is a tiny UsageFetcher impl tests can plug in to
// drive the /v1/billing/me usage section without touching
// analytics-service.
type stubUsage struct {
	resp *usageclient.RollupResponse
	err  error
}

func (s stubUsage) GetRollup(ctx context.Context, userID, period string) (*usageclient.RollupResponse, error) {
	return s.resp, s.err
}

// setupTestDB swaps models.DB for in-memory sqlite + migrates
// the Subscription table. Returns the restorer.
func setupTestDB(t *testing.T) func() {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Subscription{}, &models.ProcessedStripeEvent{}, &models.RevshareEntry{}); err != nil {
		t.Fatalf("migrate Subscription: %v", err)
	}
	prev := models.DB
	models.DB = db
	return func() {
		models.DB = prev
		SetDeps(Deps{}) // clear injected deps after every test
	}
}

func newRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/billing/plans", ListPlans)
	r.GET("/v1/billing/me", Me)
	r.POST("/v1/billing/me/subscribe", Subscribe)
	return r
}

func TestListPlans_ReturnsRegistry(t *testing.T) {
	defer setupTestDB(t)() // sets up + tears down

	r := newRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/plans", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	// Spot-check that every tier code appears in the response.
	for _, code := range []string{plans.FreeCode, plans.ProCode, plans.TeamsCode, plans.BusinessCode, plans.EnterpriseCode} {
		if !strings.Contains(body, `"`+code+`"`) {
			t.Errorf("response missing plan code %q: %s", code, body)
		}
	}
}

func TestMe_RequiresXUserIDHeader(t *testing.T) {
	defer setupTestDB(t)()
	r := newRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMe_DefaultsToFreePlanWhenNoSubscriptionExists(t *testing.T) {
	defer setupTestDB(t)()

	r := newRouter()
	uid := uuid.New().String()
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me", nil)
	req.Header.Set("X-User-ID", uid)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	got := decodeMe(t, w.Body.String())
	if got.Plan.Code != plans.FreeCode {
		t.Errorf("Plan.Code = %q, want free", got.Plan.Code)
	}
	if got.Subscription != nil {
		t.Errorf("Subscription should be nil for un-subscribed user; got %+v", got.Subscription)
	}
}

func TestMe_ReturnsPlanFromSubscription(t *testing.T) {
	defer setupTestDB(t)()

	uid := uuid.New()
	if err := models.DB.Create(&models.Subscription{
		UserID:   uid,
		PlanCode: plans.ProCode,
		Status:   models.SubStatusActive,
	}).Error; err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	r := newRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me", nil)
	req.Header.Set("X-User-ID", uid.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	got := decodeMe(t, w.Body.String())
	if got.Plan.Code != plans.ProCode {
		t.Errorf("Plan.Code = %q, want pro", got.Plan.Code)
	}
	if got.Subscription == nil || got.Subscription.PlanCode != plans.ProCode {
		t.Errorf("Subscription.PlanCode = %+v, want pro", got.Subscription)
	}
}

func TestMe_IncludesUsageWhenFetcherWired(t *testing.T) {
	defer setupTestDB(t)()
	SetDeps(Deps{Usage: stubUsage{
		resp: &usageclient.RollupResponse{
			UserID: "abc",
			Period: "2026-05",
			Items: []usageclient.RollupRow{
				{EventType: "op.merge", Unit: "ops", TotalQuantity: 12, EventCount: 12},
			},
		},
	}})

	r := newRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me", nil)
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	got := decodeMe(t, w.Body.String())
	if got.Usage == nil {
		t.Fatal("Usage was nil; expected stub rollup to be merged in")
	}
	if len(got.Usage.Items) != 1 || got.Usage.Items[0].EventType != "op.merge" {
		t.Errorf("Usage.Items = %+v", got.Usage.Items)
	}
}

func TestMe_UsageFetcherFailureDoesNotFailRequest(t *testing.T) {
	defer setupTestDB(t)()
	SetDeps(Deps{Usage: stubUsage{err: errors.New("analytics-service unreachable")}})

	r := newRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/me", nil)
	req.Header.Set("X-User-ID", uuid.New().String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("usage failure should not fail the request; got status %d", w.Code)
	}
	got := decodeMe(t, w.Body.String())
	if got.Usage != nil {
		t.Errorf("Usage should be nil when fetcher errors; got %+v", got.Usage)
	}
	if got.Plan.Code != plans.FreeCode {
		t.Errorf("Plan should still resolve; got %+v", got.Plan)
	}
}

func TestSubscribe_CreatesRowForFirstTimeSubscriber(t *testing.T) {
	defer setupTestDB(t)()

	r := newRouter()
	uid := uuid.New()
	body, _ := json.Marshal(SubscribeRequest{PlanCode: plans.ProCode})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/me/subscribe", bytes.NewReader(body))
	req.Header.Set("X-User-ID", uid.String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var sub models.Subscription
	if err := models.DB.Where("user_id = ?", uid).First(&sub).Error; err != nil {
		t.Fatalf("subscription not persisted: %v", err)
	}
	if sub.PlanCode != plans.ProCode {
		t.Errorf("PlanCode = %q, want pro", sub.PlanCode)
	}
	if sub.Status != models.SubStatusActive {
		t.Errorf("Status = %q, want active", sub.Status)
	}
}

func TestSubscribe_UpdatesExistingSubscription(t *testing.T) {
	defer setupTestDB(t)()

	uid := uuid.New()
	if err := models.DB.Create(&models.Subscription{
		UserID: uid, PlanCode: plans.FreeCode, Status: models.SubStatusActive,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := newRouter()
	body, _ := json.Marshal(SubscribeRequest{PlanCode: plans.ProCode})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/me/subscribe", bytes.NewReader(body))
	req.Header.Set("X-User-ID", uid.String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (update); body: %s", w.Code, w.Body.String())
	}

	// Exactly one row remains, on the new plan.
	var subs []models.Subscription
	_ = models.DB.Find(&subs).Error
	if len(subs) != 1 {
		t.Fatalf("expected 1 row after update, got %d", len(subs))
	}
	if subs[0].PlanCode != plans.ProCode {
		t.Errorf("PlanCode = %q, want pro after upgrade", subs[0].PlanCode)
	}
}

func TestSubscribe_RejectsEnterpriseAsSelfServe(t *testing.T) {
	defer setupTestDB(t)()

	r := newRouter()
	body, _ := json.Marshal(SubscribeRequest{PlanCode: plans.EnterpriseCode})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/me/subscribe", bytes.NewReader(body))
	req.Header.Set("X-User-ID", uuid.New().String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("Enterprise self-serve should be rejected; got status %d", w.Code)
	}
}

func TestSubscribe_RejectsUnknownPlan(t *testing.T) {
	defer setupTestDB(t)()

	r := newRouter()
	body, _ := json.Marshal(SubscribeRequest{PlanCode: "fake-tier"})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/me/subscribe", bytes.NewReader(body))
	req.Header.Set("X-User-ID", uuid.New().String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown plan", w.Code)
	}
}

func TestSubscribe_RejectsMultipleSeatsOnNonPerSeatPlan(t *testing.T) {
	defer setupTestDB(t)()

	r := newRouter()
	body, _ := json.Marshal(SubscribeRequest{PlanCode: plans.ProCode, Seats: 5})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/me/subscribe", bytes.NewReader(body))
	req.Header.Set("X-User-ID", uuid.New().String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (Pro is not per-seat)", w.Code)
	}
}

// --- subscriptionFanoutPayload JSON shape ----------------------------------

func TestSubscriptionFanoutPayload_OmitsZeroFields(t *testing.T) {
	// `subscription.created` carries newPlan + seats but NOT
	// oldPlan; `subscription.canceled` carries oldPlan but NOT
	// newPlan. omitempty makes the same struct serve all three
	// events — pin the contract so a future field rename
	// doesn't quietly bleed wrong fields onto subscribers.
	created := subscriptionFanoutPayload{NewPlan: "pro", Seats: 1}
	bytes, _ := json.Marshal(created)
	s := string(bytes)
	for _, want := range []string{`"newPlan":"pro"`, `"seats":1`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in created payload: %s", want, s)
		}
	}
	if strings.Contains(s, "oldPlan") {
		t.Errorf("created payload must not include oldPlan: %s", s)
	}

	canceled := subscriptionFanoutPayload{OldPlan: "pro", Seats: 1}
	bytes2, _ := json.Marshal(canceled)
	s2 := string(bytes2)
	for _, want := range []string{`"oldPlan":"pro"`, `"seats":1`} {
		if !strings.Contains(s2, want) {
			t.Errorf("missing %q in canceled payload: %s", want, s2)
		}
	}
	if strings.Contains(s2, "newPlan") {
		t.Errorf("canceled payload must not include newPlan: %s", s2)
	}

	changed := subscriptionFanoutPayload{OldPlan: "pro", NewPlan: "teams", Seats: 5}
	bytes3, _ := json.Marshal(changed)
	s3 := string(bytes3)
	for _, want := range []string{`"oldPlan":"pro"`, `"newPlan":"teams"`, `"seats":5`} {
		if !strings.Contains(s3, want) {
			t.Errorf("missing %q in changed payload: %s", want, s3)
		}
	}
}

func TestPublishSubscriptionDomainEvent_NoOpWhenNATSDown(t *testing.T) {
	// natsconn.JS is nil in the test binary — must log + return,
	// never panic. Defends against a regression where a missing
	// JS handle takes down the subscribe handler goroutine.
	publishSubscriptionDomainEvent(context.Background(),
		uuid.Must(uuid.NewV7()), "subscription.created",
		subscriptionFanoutPayload{NewPlan: "pro", Seats: 1})
}

// --- helpers ---------------------------------------------------------------

func decodeMe(t *testing.T, body string) MeResponse {
	t.Helper()
	var envelope struct {
		Success bool       `json:"success"`
		Message string     `json:"message"`
		Data    MeResponse `json:"data"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v; body: %s", err, body)
	}
	if !envelope.Success {
		t.Fatalf("response.Success = false; body: %s", body)
	}
	return envelope.Data
}
