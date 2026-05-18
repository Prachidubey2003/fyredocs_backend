package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"billing-service/internal/models"
	"billing-service/internal/plans"
	"billing-service/internal/stripeauth"
	"billing-service/internal/stripeclient"
)

const testStripeSecret = "whsec_test_3iQ0sQfk4mDe6n4U3iY"

// newStripeRouter spins up a minimal gin router with only the
// webhook route wired. Mirrors newRouter() but doesn't pull in
// the user-facing handlers — keeps test scope tight.
func newStripeRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/billing/stripe/webhook", StripeWebhook)
	return r
}

// signedStripeRequest builds an HTTP request with a valid
// Stripe-Signature header for `body`. Mirrors what Stripe's own
// SDK produces; uses stripeauth.ComputeHMAC so the signature
// format stays in lockstep with the verifier.
func signedStripeRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	ts := time.Now().Unix()
	sig := stripeauth.ComputeHMAC(testStripeSecret, ts, body)
	header := fmt.Sprintf("t=%d,v1=%s", ts, sig)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/webhook", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", header)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// subscriptionEventBody assembles a `customer.subscription.*`
// payload with the supplied parameters. Keeps each test focused
// on the field that matters rather than re-stating the entire
// envelope shape.
type subEventOpts struct {
	EventID        string
	Type           string
	SubID          string
	CustomerID     string
	Status         string
	PlanCode       string
	UserID         string
	PeriodStart    int64
	PeriodEnd      int64
	DropPlanCode   bool
	DropUserID     bool
}

func subscriptionEventBody(opts subEventOpts) []byte {
	if opts.PeriodStart == 0 {
		opts.PeriodStart = time.Now().Unix()
	}
	if opts.PeriodEnd == 0 {
		opts.PeriodEnd = opts.PeriodStart + 30*24*3600
	}
	meta := map[string]string{}
	if !opts.DropPlanCode {
		meta["plan_code"] = opts.PlanCode
	}
	if !opts.DropUserID {
		meta["user_id"] = opts.UserID
	}
	payload := map[string]any{
		"id":       opts.EventID,
		"type":     opts.Type,
		"livemode": false,
		"data": map[string]any{
			"object": map[string]any{
				"id":                   opts.SubID,
				"customer":             opts.CustomerID,
				"status":               opts.Status,
				"metadata":             meta,
				"current_period_start": opts.PeriodStart,
				"current_period_end":   opts.PeriodEnd,
			},
		},
	}
	out, _ := json.Marshal(payload)
	return out
}

// ---- bad-signature / config tests ----

func TestStripeWebhook_RejectsWhenSecretUnset(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, "")
	r := newStripeRouter()

	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/webhook", strings.NewReader(`{}`))
	req.Header.Set("Stripe-Signature", "t=1234567890,v1=deadbeef")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when WEBHOOK_DISABLED; body=%s", w.Code, w.Body.String())
	}
}

func TestStripeWebhook_RejectsBadSignature(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	req := httptest.NewRequest(http.MethodPost, "/v1/billing/stripe/webhook", strings.NewReader(`{"id":"evt_x"}`))
	req.Header.Set("Stripe-Signature", "t=1234567890,v1=0000000000000000000000000000000000000000000000000000000000000000")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on bad signature; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "BAD_SIGNATURE") {
		t.Errorf("expected BAD_SIGNATURE code in body; got %s", w.Body.String())
	}
}

func TestStripeWebhook_RejectsOversizedBody(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	// Body just over the cap. Sign it correctly so the size
	// gate is what trips, not the signature gate.
	body := bytes.Repeat([]byte("a"), stripeMaxBodyBytes+10)
	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on body cap; body=%s", w.Code, w.Body.String())
	}
}

// ---- subscription.created / updated ----

func TestStripeWebhook_SubscriptionCreated_NewRow(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	userID := uuid.Must(uuid.NewV7())
	body := subscriptionEventBody(subEventOpts{
		EventID:    "evt_sub_create_001",
		Type:       "customer.subscription.created",
		SubID:      "sub_test_001",
		CustomerID: "cus_test_001",
		Status:     "active",
		PlanCode:   plans.ProCode,
		UserID:     userID.String(),
	})

	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// Subscription row landed.
	var sub models.Subscription
	if err := models.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		t.Fatalf("subscription row not created: %v", err)
	}
	if sub.PlanCode != plans.ProCode {
		t.Errorf("plan_code = %q, want %q", sub.PlanCode, plans.ProCode)
	}
	if sub.StripeSubscriptionID == nil || *sub.StripeSubscriptionID != "sub_test_001" {
		t.Errorf("stripe_subscription_id = %v, want sub_test_001", sub.StripeSubscriptionID)
	}
	if sub.StripeCustomerID == nil || *sub.StripeCustomerID != "cus_test_001" {
		t.Errorf("stripe_customer_id = %v, want cus_test_001", sub.StripeCustomerID)
	}
	if sub.Status != models.SubStatusActive {
		t.Errorf("status = %q, want active", sub.Status)
	}

	// Idempotency row landed.
	var evt models.ProcessedStripeEvent
	if err := models.DB.Where("event_id = ?", "evt_sub_create_001").First(&evt).Error; err != nil {
		t.Errorf("processed event row not created: %v", err)
	}
}

func TestStripeWebhook_DuplicateEventIsAckedNotReprocessed(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	userID := uuid.Must(uuid.NewV7())
	body := subscriptionEventBody(subEventOpts{
		EventID:    "evt_dedup_001",
		Type:       "customer.subscription.created",
		SubID:      "sub_dedup_001",
		CustomerID: "cus_dedup_001",
		Status:     "active",
		PlanCode:   plans.ProCode,
		UserID:     userID.String(),
	})

	// First delivery: 200 + row created.
	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first delivery status = %d; body=%s", w.Code, w.Body.String())
	}

	// Mutate the row so we can prove the second delivery didn't
	// rerun the handler (which would clobber the mutation).
	if err := models.DB.Model(&models.Subscription{}).
		Where("user_id = ?", userID).
		Update("plan_code", plans.BusinessCode).Error; err != nil {
		t.Fatalf("mutate row: %v", err)
	}

	// Second delivery of the SAME event id: 200 + `duplicate=true`
	// + no row mutation.
	req2 := signedStripeRequest(t, body)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second delivery status = %d; body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"duplicate":true`) {
		t.Errorf("expected duplicate flag in body; got %s", w2.Body.String())
	}

	var sub models.Subscription
	if err := models.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if sub.PlanCode != plans.BusinessCode {
		t.Errorf("duplicate delivery clobbered the row (plan_code now %q, want %q)",
			sub.PlanCode, plans.BusinessCode)
	}
}

func TestStripeWebhook_SubscriptionUpdated_TogglesStatus(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	// Pre-seed an active subscription tied to a Stripe sub id.
	userID := uuid.Must(uuid.NewV7())
	stripeSubID := "sub_status_001"
	stripeCustID := "cus_status_001"
	must(t, models.DB.Create(&models.Subscription{
		UserID:               userID,
		PlanCode:             plans.ProCode,
		Status:               models.SubStatusActive,
		Seats:                1,
		CurrentPeriodStart:   time.Now().UTC(),
		CurrentPeriodEnd:     time.Now().Add(30 * 24 * time.Hour).UTC(),
		StripeSubscriptionID: &stripeSubID,
		StripeCustomerID:     &stripeCustID,
	}).Error)

	// Stripe says "past_due" — billing-service must mirror.
	body := subscriptionEventBody(subEventOpts{
		EventID:    "evt_upd_001",
		Type:       "customer.subscription.updated",
		SubID:      stripeSubID,
		CustomerID: stripeCustID,
		Status:     "past_due",
		PlanCode:   plans.ProCode,
		UserID:     userID.String(),
	})
	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var sub models.Subscription
	if err := models.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if sub.Status != models.SubStatusPastDue {
		t.Errorf("status = %q, want past_due", sub.Status)
	}
}

func TestStripeWebhook_SubscriptionDeleted_MarksCanceled(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	userID := uuid.Must(uuid.NewV7())
	stripeSubID := "sub_del_001"
	stripeCustID := "cus_del_001"
	must(t, models.DB.Create(&models.Subscription{
		UserID:               userID,
		PlanCode:             plans.ProCode,
		Status:               models.SubStatusActive,
		Seats:                1,
		CurrentPeriodStart:   time.Now().UTC(),
		CurrentPeriodEnd:     time.Now().Add(30 * 24 * time.Hour).UTC(),
		StripeSubscriptionID: &stripeSubID,
		StripeCustomerID:     &stripeCustID,
	}).Error)

	body := subscriptionEventBody(subEventOpts{
		EventID:    "evt_del_001",
		Type:       "customer.subscription.deleted",
		SubID:      stripeSubID,
		CustomerID: stripeCustID,
		Status:     "canceled",
		PlanCode:   plans.ProCode,
		UserID:     userID.String(),
	})
	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var sub models.Subscription
	if err := models.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if sub.Status != models.SubStatusCanceled {
		t.Errorf("status = %q, want canceled", sub.Status)
	}
}

// ---- missing-metadata + unknown-event paths ----

func TestStripeWebhook_MissingPlanCodeMetadata_SkipsRowMutation(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	userID := uuid.Must(uuid.NewV7())
	body := subscriptionEventBody(subEventOpts{
		EventID:      "evt_no_plan_001",
		Type:         "customer.subscription.created",
		SubID:        "sub_x",
		CustomerID:   "cus_x",
		Status:       "active",
		UserID:       userID.String(),
		DropPlanCode: true,
	})

	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (skipped, not failed)", w.Code)
	}

	// No row should have been created — without plan_code we
	// can't pick a tier and we don't guess.
	var count int64
	models.DB.Model(&models.Subscription{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 subscription rows; got %d", count)
	}

	// Idempotency row IS recorded so a retry of this exact
	// event doesn't reprocess.
	var evt models.ProcessedStripeEvent
	if err := models.DB.Where("event_id = ?", "evt_no_plan_001").First(&evt).Error; err != nil {
		t.Errorf("processed event row missing: %v", err)
	}
}

func TestStripeWebhook_UnknownEventType_AcksWithoutSideEffect(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	// An event type we don't handle (e.g., `charge.refunded`).
	// Must return 200 — Stripe will retry on any non-2xx.
	body := []byte(`{"id":"evt_unknown_001","type":"charge.refunded","livemode":false,"data":{"object":{}}}`)
	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 on unknown event type; body=%s", w.Code, w.Body.String())
	}

	// Subscription table untouched.
	var count int64
	models.DB.Model(&models.Subscription{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 subscription rows; got %d", count)
	}
}

// ---- mapStripeStatus unit tests ----

func TestMapStripeStatus_KnownValues(t *testing.T) {
	cases := map[string]string{
		"active":             models.SubStatusActive,
		"trialing":           models.SubStatusActive,
		"":                   models.SubStatusActive,
		"canceled":           models.SubStatusCanceled,
		"past_due":           models.SubStatusPastDue,
		"incomplete":         models.SubStatusPastDue,
		"unpaid":             models.SubStatusPastDue,
		"incomplete_expired": models.SubStatusPastDue,
		"some_future_value":  models.SubStatusActive, // defensive default
	}
	for in, want := range cases {
		if got := mapStripeStatus(in); got != want {
			t.Errorf("mapStripeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- isDuplicateKey unit tests ----

func TestIsDuplicateKey_RecognisesCommonShapes(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"UNIQUE constraint failed: processed_stripe_events.event_id", true},
		{"ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)", true},
		{"SQLSTATE 23505", true},
		{"connection refused", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isDuplicateKey(errString(tc.err))
		if got != tc.want {
			t.Errorf("isDuplicateKey(%q) = %v, want %v", tc.err, got, tc.want)
		}
	}
	if isDuplicateKey(nil) {
		t.Error("isDuplicateKey(nil) must be false")
	}
}

// errString is a tiny error type used only by the table above.
type errString string

func (e errString) Error() string { return string(e) }

// must fails the test on any non-nil error. Saves a line per
// fixture insertion.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}

// ---- charge.succeeded / revshare ----

// chargeEventBody assembles a `charge.succeeded` payload with
// optional marketplace metadata. The handler reads only id,
// amount, currency, customer, and metadata — Stripe's full
// schema is enormous but irrelevant to this test.
func chargeEventBody(eventID, chargeID string, amount int64, currency string, metadata map[string]string) []byte {
	payload := map[string]any{
		"id":       eventID,
		"type":     "charge.succeeded",
		"livemode": false,
		"data": map[string]any{
			"object": map[string]any{
				"id":       chargeID,
				"amount":   amount,
				"currency": currency,
				"customer": "cus_test",
				"metadata": metadata,
			},
		},
	}
	out, _ := json.Marshal(payload)
	return out
}

func TestStripeWebhook_ChargeSucceededRecordsMarketplaceEntry(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	dev := uuid.Must(uuid.NewV7())
	body := chargeEventBody("evt_charge_001", "ch_marketplace_001", 5000, "usd",
		map[string]string{"plugin_id": "plug_super", "developer_user_id": dev.String()})

	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// One entry persisted with 70/30 default split + USD upper.
	var entry models.RevshareEntry
	if err := models.DB.Where("transaction_id = ?", "ch_marketplace_001").First(&entry).Error; err != nil {
		t.Fatalf("entry not persisted: %v", err)
	}
	if entry.DeveloperUserID != dev {
		t.Errorf("developer_user_id = %v, want %v", entry.DeveloperUserID, dev)
	}
	if entry.GrossCents != 5000 {
		t.Errorf("gross_cents = %d, want 5000", entry.GrossCents)
	}
	if entry.DeveloperShareCents != 3500 || entry.PlatformShareCents != 1500 {
		t.Errorf("split = (%d, %d), want (3500, 1500)",
			entry.DeveloperShareCents, entry.PlatformShareCents)
	}
	if entry.Currency != "USD" {
		t.Errorf("currency = %q, want USD", entry.Currency)
	}
	if entry.Source != "stripe_charge" || entry.SourceRef != "ch_marketplace_001" {
		t.Errorf("(source, source_ref) = (%q, %q)", entry.Source, entry.SourceRef)
	}
}

func TestStripeWebhook_ChargeSucceededSkipsWhenNoMarketplaceMetadata(t *testing.T) {
	// A regular subscription charge has no plugin_id / no
	// developer_user_id — the handler ack's without writing
	// a revshare row.
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	body := chargeEventBody("evt_charge_sub", "ch_sub_001", 1500, "usd",
		map[string]string{}) // empty metadata

	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (skipped, not failed); body=%s", w.Code, w.Body.String())
	}

	var count int64
	models.DB.Model(&models.RevshareEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 revshare entries for non-marketplace charge; got %d", count)
	}
}

func TestStripeWebhook_ChargeSucceededDedupesOnRedelivery(t *testing.T) {
	// Same charge id delivered twice — must result in exactly
	// one revshare entry. The processed_stripe_events table
	// also dedupes at the event-id level, so we use distinct
	// event ids to exercise the row-level dedup specifically.
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	dev := uuid.Must(uuid.NewV7())
	meta := map[string]string{"plugin_id": "plug_super", "developer_user_id": dev.String()}

	for _, evtID := range []string{"evt_redeliver_a", "evt_redeliver_b"} {
		body := chargeEventBody(evtID, "ch_redeliver_001", 5000, "usd", meta)
		req := signedStripeRequest(t, body)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", evtID, w.Code)
		}
	}

	var count int64
	models.DB.Model(&models.RevshareEntry{}).
		Where("source_ref = ?", "ch_redeliver_001").
		Count(&count)
	if count != 1 {
		t.Errorf("expected 1 entry after redelivery; got %d", count)
	}
}

func TestStripeWebhook_ChargeSucceededSkipsMalformedDeveloperUUID(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	body := chargeEventBody("evt_bad_uuid", "ch_bad_uuid_001", 5000, "usd",
		map[string]string{"plugin_id": "plug_super", "developer_user_id": "not-a-uuid"})

	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (skipped, not failed)", w.Code)
	}
	var count int64
	models.DB.Model(&models.RevshareEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 entries on malformed UUID; got %d", count)
	}
}

func TestStripeWebhook_ChargeSucceededSkipsZeroAmount(t *testing.T) {
	// revshare.Calculate refuses zero / negative gross with
	// ErrInvalidGross. The handler logs + skips so a publisher
	// bug doesn't poison the queue.
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	r := newStripeRouter()

	dev := uuid.Must(uuid.NewV7())
	body := chargeEventBody("evt_zero", "ch_zero_001", 0, "usd",
		map[string]string{"plugin_id": "plug_super", "developer_user_id": dev.String()})

	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (skipped, not failed)", w.Code)
	}
	var count int64
	models.DB.Model(&models.RevshareEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 entries for zero-amount charge; got %d", count)
	}
}

// chargeEventBodyWithBT mirrors chargeEventBody but also sets
// the `balance_transaction` field on the charge object so the
// fee-lookup path engages.
func chargeEventBodyWithBT(eventID, chargeID string, amount int64, currency, balanceTxnID string, metadata map[string]string) []byte {
	payload := map[string]any{
		"id":       eventID,
		"type":     "charge.succeeded",
		"livemode": false,
		"data": map[string]any{
			"object": map[string]any{
				"id":                  chargeID,
				"amount":              amount,
				"currency":            currency,
				"customer":            "cus_test",
				"metadata":            metadata,
				"balance_transaction": balanceTxnID,
			},
		},
	}
	out, _ := json.Marshal(payload)
	return out
}

// installStripeBalanceTxnStub starts an httptest server that
// responds to GET /v1/balance_transactions/{id} with the
// supplied fee. Records the requested ids so the test can
// assert the lookup happened. The stripe client factory is
// swapped to point at this server for the duration of the
// test.
func installStripeBalanceTxnStub(t *testing.T, feeByID map[string]int64) *[]string {
	t.Helper()
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/v1/balance_transactions/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, prefix)
		hits = append(hits, id)
		fee, ok := feeByID[id]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing","message":"no such BT"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"id":%q,"fee":%d,"currency":"usd"}`, id, fee)))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { SetStripeClientFactoryForTest(nil) })
	SetStripeClientFactoryForTest(func() (*stripeclient.Client, error) {
		return &stripeclient.Client{SecretKey: "sk_test_dummy", BaseURL: srv.URL}, nil
	})
	return &hits
}

func TestStripeWebhook_ChargeSucceededPopulatesStripeFeeFromBalanceTransaction(t *testing.T) {
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	hits := installStripeBalanceTxnStub(t, map[string]int64{"btx_001": 175})
	r := newStripeRouter()

	dev := uuid.Must(uuid.NewV7())
	body := chargeEventBodyWithBT("evt_btx_001", "ch_btx_001", 5000, "usd", "btx_001",
		map[string]string{"plugin_id": "plug_super", "developer_user_id": dev.String()})

	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	if len(*hits) != 1 || (*hits)[0] != "btx_001" {
		t.Fatalf("expected BT lookup of btx_001; got %v", *hits)
	}

	var entry models.RevshareEntry
	if err := models.DB.Where("transaction_id = ?", "ch_btx_001").First(&entry).Error; err != nil {
		t.Fatalf("entry not persisted: %v", err)
	}
	if entry.StripeFeeCents != 175 {
		t.Errorf("stripe_fee_cents = %d, want 175", entry.StripeFeeCents)
	}
	// Fee allocation in DefaultSplit (per-share proportional)
	// keeps gross + currency intact; we just want to verify the
	// fee landed on the row.
	if entry.GrossCents != 5000 {
		t.Errorf("gross_cents = %d, want 5000", entry.GrossCents)
	}
}

func TestStripeWebhook_ChargeSucceededOmitsFeeLookupWhenNoBalanceTransaction(t *testing.T) {
	// Charge with no balance_transaction id (test event,
	// pre-posting) — the handler MUST NOT call Stripe and MUST
	// still record the row with fee=0.
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	hits := installStripeBalanceTxnStub(t, map[string]int64{})
	r := newStripeRouter()

	dev := uuid.Must(uuid.NewV7())
	// chargeEventBody (no BT id) — keeps the existing happy-
	// path body identical to pre-feature behaviour.
	body := chargeEventBody("evt_no_btx", "ch_no_btx", 5000, "usd",
		map[string]string{"plugin_id": "plug_super", "developer_user_id": dev.String()})

	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	if len(*hits) != 0 {
		t.Errorf("expected zero BT lookups when balance_transaction is empty; got %v", *hits)
	}

	var entry models.RevshareEntry
	if err := models.DB.Where("transaction_id = ?", "ch_no_btx").First(&entry).Error; err != nil {
		t.Fatalf("entry not persisted: %v", err)
	}
	if entry.StripeFeeCents != 0 {
		t.Errorf("stripe_fee_cents = %d, want 0 (no BT to look up)", entry.StripeFeeCents)
	}
}

func TestStripeWebhook_ChargeSucceededFallsBackToZeroFeeOnStripeLookupFailure(t *testing.T) {
	// Stripe returns 404 / 5xx for the BT — the handler MUST
	// record the entry with fee=0 anyway. Blocking on a flaky
	// BT lookup would cause permanent retries and "missing
	// earnings" UX; an under-reported fee is fixable later by
	// a reconciliation pass.
	defer setupTestDB(t)()
	t.Setenv(stripeSecretEnvVar, testStripeSecret)
	// Empty map → every lookup misses and returns 404.
	hits := installStripeBalanceTxnStub(t, map[string]int64{})
	r := newStripeRouter()

	dev := uuid.Must(uuid.NewV7())
	body := chargeEventBodyWithBT("evt_btx_404", "ch_btx_404", 5000, "usd", "btx_missing",
		map[string]string{"plugin_id": "plug_super", "developer_user_id": dev.String()})

	req := signedStripeRequest(t, body)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	if len(*hits) != 1 || (*hits)[0] != "btx_missing" {
		t.Errorf("expected exactly one BT lookup attempt; got %v", *hits)
	}

	var entry models.RevshareEntry
	if err := models.DB.Where("transaction_id = ?", "ch_btx_404").First(&entry).Error; err != nil {
		t.Fatalf("entry MUST persist even when fee lookup fails: %v", err)
	}
	if entry.StripeFeeCents != 0 {
		t.Errorf("stripe_fee_cents = %d, want 0 (lookup failed → fall open)", entry.StripeFeeCents)
	}
}
