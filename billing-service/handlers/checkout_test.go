package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"billing-service/internal/plans"
	"billing-service/internal/stripeclient"
)

const (
	testStripeAPIKey  = "sk_test_dummy_for_handler_tests"
	testStripePriceID = "price_test_pro_monthly"
)

// newCheckoutRouter wires only the checkout route. Mirrors the
// other handler-test routers in this package.
func newCheckoutRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/billing/checkout/session", CheckoutSession)
	return r
}

// setupCheckoutEnv applies the env vars the handler needs +
// returns the success/cancel URL pair so tests can assert on
// forwarded values without re-stating constants. t.Setenv
// handles teardown automatically.
func setupCheckoutEnv(t *testing.T) {
	t.Helper()
	t.Setenv(stripeAPIKeyEnv, testStripeAPIKey)
	t.Setenv(stripePriceIDsEnv, `{"pro":"`+testStripePriceID+`","teams":"price_test_teams"}`)
	t.Setenv(stripeCheckoutSuccessURLEnv, "https://fyredocs.com/billing/success")
	t.Setenv(stripeCheckoutCancelURLEnv, "https://fyredocs.com/billing/cancel")
}

// installStripeStub points the handler at an httptest server
// running `h`. Returns the captured request body across all
// calls so tests can inspect what we sent Stripe.
func installStripeStub(t *testing.T, h http.HandlerFunc) *[]string {
	t.Helper()
	bodies := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		h(w, r)
	}))
	t.Cleanup(server.Close)

	SetStripeClientFactoryForTest(func() (*stripeclient.Client, error) {
		return &stripeclient.Client{
			SecretKey: testStripeAPIKey,
			BaseURL:   server.URL,
		}, nil
	})
	t.Cleanup(func() { SetStripeClientFactoryForTest(nil) })
	return &bodies
}

// ---- happy path ----

func TestCheckoutSession_CreatesSessionAndReturnsURL(t *testing.T) {
	defer setupTestDB(t)()
	setupCheckoutEnv(t)
	bodies := installStripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_xyz","url":"https://checkout.stripe.com/c/pay/cs_test_xyz"}`))
	})

	body, _ := json.Marshal(CheckoutSessionRequest{PlanCode: plans.ProCode})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout/session", bytes.NewReader(body))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newCheckoutRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	respBody := w.Body.String()
	if !strings.Contains(respBody, `"sessionId":"cs_test_xyz"`) {
		t.Errorf("response missing sessionId; got %s", respBody)
	}
	if !strings.Contains(respBody, `"url":"https://checkout.stripe.com/c/pay/cs_test_xyz"`) {
		t.Errorf("response missing url; got %s", respBody)
	}

	// One outbound call to Stripe with the right price + URLs +
	// metadata.
	if len(*bodies) != 1 {
		t.Fatalf("expected 1 outbound stripe call; got %d", len(*bodies))
	}
	out := (*bodies)[0]
	mustContain := []string{
		"line_items%5B0%5D%5Bprice%5D=" + testStripePriceID,
		"success_url=https%3A%2F%2Ffyredocs.com%2Fbilling%2Fsuccess",
		"cancel_url=https%3A%2F%2Ffyredocs.com%2Fbilling%2Fcancel",
		"metadata%5Bplan_code%5D=pro",
		"metadata%5Buser_id%5D=",
		"subscription_data%5Bmetadata%5D%5Bplan_code%5D=pro",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("outbound body missing %q\n%s", want, out)
		}
	}
}

func TestCheckoutSession_ForwardsSeatCountForPerSeatPlan(t *testing.T) {
	defer setupTestDB(t)()
	setupCheckoutEnv(t)
	bodies := installStripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"cs_t","url":"https://checkout.stripe.com/cs_t"}`))
	})

	body, _ := json.Marshal(CheckoutSessionRequest{PlanCode: plans.TeamsCode, Seats: 5})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout/session", bytes.NewReader(body))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newCheckoutRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains((*bodies)[0], "line_items%5B0%5D%5Bquantity%5D=5") {
		t.Errorf("expected quantity=5 forwarded; body=%s", (*bodies)[0])
	}
}

// ---- validation errors (no outbound call) ----

func TestCheckoutSession_RejectsUnauthenticated(t *testing.T) {
	defer setupTestDB(t)()
	setupCheckoutEnv(t)
	installStripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Stripe should not be called when caller is unauthenticated")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout/session", strings.NewReader(`{"planCode":"pro"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newCheckoutRouter().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestCheckoutSession_RejectsUnknownPlan(t *testing.T) {
	defer setupTestDB(t)()
	setupCheckoutEnv(t)
	installStripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Stripe should not be called for unknown plan")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout/session", strings.NewReader(`{"planCode":"platinum"}`))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newCheckoutRouter().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "INVALID_PLAN") {
		t.Errorf("expected INVALID_PLAN code; got %s", w.Body.String())
	}
}

func TestCheckoutSession_RejectsEnterpriseAsNonSelfServe(t *testing.T) {
	defer setupTestDB(t)()
	setupCheckoutEnv(t)
	installStripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Stripe should not be called for non-selfserve plan")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout/session", strings.NewReader(`{"planCode":"enterprise"}`))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newCheckoutRouter().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCheckoutSession_RejectsFreePlan(t *testing.T) {
	// Free has no Stripe presence. Route the user to
	// /me/subscribe instead.
	defer setupTestDB(t)()
	setupCheckoutEnv(t)
	installStripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Stripe should not be called for free plan")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout/session", strings.NewReader(`{"planCode":"free"}`))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newCheckoutRouter().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (Free plan should redirect to /me/subscribe)", w.Code)
	}
}

func TestCheckoutSession_RejectsMultipleSeatsOnNonPerSeatPlan(t *testing.T) {
	defer setupTestDB(t)()
	setupCheckoutEnv(t)
	installStripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Stripe should not be called for invalid seat count")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout/session", strings.NewReader(`{"planCode":"pro","seats":4}`))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newCheckoutRouter().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ---- env / config failure modes ----

func TestCheckoutSession_ReturnsServiceUnavailableWhenSuccessURLMissing(t *testing.T) {
	defer setupTestDB(t)()
	setupCheckoutEnv(t)
	// Drop one of the URLs to simulate a half-configured env.
	t.Setenv(stripeCheckoutSuccessURLEnv, "")
	installStripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Stripe should not be called when URLs are missing")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout/session", strings.NewReader(`{"planCode":"pro"}`))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newCheckoutRouter().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when success/cancel URLs missing; body=%s", w.Code, w.Body.String())
	}
}

func TestCheckoutSession_ReturnsServerErrorWhenPlanHasNoPriceMapping(t *testing.T) {
	defer setupTestDB(t)()
	setupCheckoutEnv(t)
	// Map exists but doesn't include Business. Asking for
	// business → 500 (operator forgot the mapping when a new
	// plan was launched; this is unrecoverable from the
	// client side).
	t.Setenv(stripePriceIDsEnv, `{"pro":"price_pro"}`)
	installStripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Stripe should not be called when price mapping is missing")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout/session", strings.NewReader(`{"planCode":"business"}`))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newCheckoutRouter().ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "STRIPE_NOT_CONFIGURED") {
		t.Errorf("expected STRIPE_NOT_CONFIGURED code; got %s", w.Body.String())
	}
}

func TestCheckoutSession_PropagatesStripeError(t *testing.T) {
	// Stripe says no — surface as 502 with STRIPE_ERROR code so
	// the SPA can show a meaningful message + the user can
	// retry without thinking it's our outage.
	defer setupTestDB(t)()
	setupCheckoutEnv(t)
	installStripeStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{
			"error": {
				"type": "invalid_request_error",
				"code": "resource_missing",
				"message": "No such price: price_test_pro_monthly"
			}
		}`))
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout/session", strings.NewReader(`{"planCode":"pro"}`))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newCheckoutRouter().ServeHTTP(w, req)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
	if !strings.Contains(w.Body.String(), "STRIPE_ERROR") {
		t.Errorf("expected STRIPE_ERROR code in body; got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "No such price") {
		t.Errorf("expected Stripe message to be surfaced; got %s", w.Body.String())
	}
}

// ---- lookupStripePriceID unit tests ----

func TestLookupStripePriceID_Variants(t *testing.T) {
	cases := []struct {
		name    string
		envJSON string
		plan    string
		wantID  string
		wantErr bool
	}{
		{"valid", `{"pro":"price_x","teams":"price_y"}`, "pro", "price_x", false},
		{"unknown plan", `{"pro":"price_x"}`, "business", "", true},
		{"empty value", `{"pro":""}`, "pro", "", true},
		{"unset env", ``, "pro", "", true},
		{"bad json", `not-json`, "pro", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(stripePriceIDsEnv, tc.envJSON)
			got, err := lookupStripePriceID(tc.plan)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != tc.wantID {
				t.Errorf("id = %q, want %q", got, tc.wantID)
			}
		})
	}
}
