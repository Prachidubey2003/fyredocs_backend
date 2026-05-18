package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"fyredocs/shared/queue"

	"notify-service/internal/channels"
	"notify-service/internal/dispatcher"
	"notify-service/internal/models"
)

// okChannel is a Channel that succeeds without side effects.
// Handler tests use it to exercise the dispatch path without
// touching real transports.
type okChannel struct{}

func (okChannel) Send(_ context.Context, _ channels.SendRequest) error { return nil }

func setupTest(t *testing.T) (*gin.Engine, *gorm.DB, func()) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Delivery{}, &models.WebhookSubscription{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prevDB := models.DB
	models.DB = db

	disp := dispatcher.New(db)
	disp.Register(queue.ChannelWebhook, okChannel{})
	disp.Register(queue.ChannelEmail, okChannel{})
	SetDeps(Deps{Disp: disp})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/healthz", HealthCheck)
	r.GET("/readyz", ReadyCheck)
	r.GET("/v1/notify/deliveries", ListMyDeliveries)
	r.POST("/internal/v1/notify/send", Send)

	restore := func() {
		models.DB = prevDB
		SetDeps(Deps{})
	}
	return r, db, restore
}

func TestSend_DispatchesAndReturnsDelivery(t *testing.T) {
	r, db, restore := setupTest(t)
	defer restore()

	body, _ := json.Marshal(SendRequest{
		Channel: queue.ChannelEmail,
		Target:  "user@example.com",
		UserID:  uuid.New().String(),
		Payload: json.RawMessage(`{"subject":"hi","text":"hello"}`),
	})
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/notify/send", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	// One row landed in the DB.
	var rows []models.Delivery
	_ = db.Find(&rows).Error
	if len(rows) != 1 {
		t.Errorf("expected 1 delivery row, got %d", len(rows))
	}
}

func TestSend_RejectsMissingChannelOrTarget(t *testing.T) {
	r, _, restore := setupTest(t)
	defer restore()

	cases := []SendRequest{
		{Target: "x"},
		{Channel: "email"},
		{},
	}
	for _, c := range cases {
		body, _ := json.Marshal(c)
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/notify/send", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 for input %+v", w.Code, c)
		}
	}
}

func TestListMyDeliveries_RequiresXUserIDHeader(t *testing.T) {
	r, _, restore := setupTest(t)
	defer restore()

	req := httptest.NewRequest(http.MethodGet, "/v1/notify/deliveries", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestListMyDeliveries_FiltersByUserAndChannel(t *testing.T) {
	r, db, restore := setupTest(t)
	defer restore()

	user := uuid.New()
	other := uuid.New()
	seed := []models.Delivery{
		{UserID: &user, Channel: queue.ChannelEmail, Target: "u@x.com", Status: models.StatusDelivered},
		{UserID: &user, Channel: queue.ChannelWebhook, Target: "https://x.com/hook", Status: models.StatusDelivered},
		{UserID: &other, Channel: queue.ChannelEmail, Target: "o@x.com", Status: models.StatusDelivered},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Without channel filter: 2 rows for `user`.
	req := httptest.NewRequest(http.MethodGet, "/v1/notify/deliveries", nil)
	req.Header.Set("X-User-ID", user.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "u@x.com") || !strings.Contains(w.Body.String(), "https://x.com/hook") {
		t.Errorf("expected both user rows; body: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "o@x.com") {
		t.Errorf("response leaked another user's deliveries")
	}

	// With channel filter: only the email row.
	req = httptest.NewRequest(http.MethodGet, "/v1/notify/deliveries?channel=email", nil)
	req.Header.Set("X-User-ID", user.String())
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "u@x.com") {
		t.Errorf("filtered response should contain the email row; got %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "https://x.com/hook") {
		t.Errorf("filtered response should NOT contain webhook row; got %s", w.Body.String())
	}
}

func TestParseLimit(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", 50},
		{"10", 10},
		{"999", 200}, // clamped
		{"abc", 50}, // non-numeric → default
		{"0", 50},   // 0 falls back to default
	}
	for _, c := range cases {
		got := parseLimit(c.raw, 50, 200)
		if got != c.want {
			t.Errorf("parseLimit(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}
