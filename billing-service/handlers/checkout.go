package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fyredocs/shared/response"

	"billing-service/internal/plans"
	"billing-service/internal/stripeclient"
)

// stripeAPIKeyEnv holds the `sk_test_…` / `sk_live_…` secret
// key billing-service uses to call Stripe's REST API. Empty =
// outbound Stripe is disabled (the checkout endpoint refuses
// with 503).
const stripeAPIKeyEnv = "STRIPE_API_KEY"

// stripePriceIDsEnv is a JSON map of plan_code → Stripe price
// id (`price_…`). Set per environment, e.g.:
//
//	STRIPE_PRICE_IDS='{"pro":"price_1Abc","teams":"price_1Def","business":"price_1Ghi"}'
//
// `enterprise` and `free` are NEVER in this map — Enterprise is
// sales-led (no self-serve checkout) and Free has no Stripe
// presence.
const stripePriceIDsEnv = "STRIPE_PRICE_IDS"

// stripeCheckoutSuccessURLEnv / stripeCheckoutCancelURLEnv are
// the absolute URLs Stripe will redirect to after the user
// completes / abandons the checkout. Must be set per env;
// defaults are intentionally absent so a misconfigured deploy
// fails loud rather than redirecting users to localhost.
const (
	stripeCheckoutSuccessURLEnv = "STRIPE_CHECKOUT_SUCCESS_URL"
	stripeCheckoutCancelURLEnv  = "STRIPE_CHECKOUT_CANCEL_URL"
)

// stripeClientFactory is what the handler calls to obtain its
// outbound Stripe client. Decoupled from the global env so
// tests can swap in a stub against an httptest server. Real
// code uses `defaultStripeClient`.
type stripeClientFactory func() (*stripeclient.Client, error)

// stripeFactory is the package-level injection point. main.go
// keeps the default; tests set it directly.
var stripeFactory stripeClientFactory = defaultStripeClient

func defaultStripeClient() (*stripeclient.Client, error) {
	key := strings.TrimSpace(os.Getenv(stripeAPIKeyEnv))
	if key == "" {
		return nil, errors.New("STRIPE_API_KEY is not set")
	}
	return &stripeclient.Client{SecretKey: key}, nil
}

// SetStripeClientFactoryForTest swaps the outbound Stripe
// client factory. Tests call this with a closure returning a
// pointer to a client backed by httptest.Server. Restore by
// calling with `defaultStripeClient`.
func SetStripeClientFactoryForTest(f stripeClientFactory) {
	if f == nil {
		stripeFactory = defaultStripeClient
		return
	}
	stripeFactory = f
}

// CheckoutSessionRequest is the inbound shape POSTed by the
// frontend.
type CheckoutSessionRequest struct {
	PlanCode string `json:"planCode"`
	Seats    int    `json:"seats,omitempty"`
}

// CheckoutSession handles POST /v1/billing/checkout/session.
//
// Flow:
//  1. Authenticate via X-User-ID (api-gateway already verified
//     the JWT and forwarded the user id).
//  2. Validate plan: must exist, must be SelfServe, seat count
//     must be compatible with the plan's PerSeat flag.
//  3. Resolve the Stripe price id from STRIPE_PRICE_IDS env.
//     Unknown plan_code → 500 (operator missed adding the
//     mapping when a new plan was launched).
//  4. Call Stripe to create a Checkout Session, passing our
//     user_id + plan_code in metadata so the eventual
//     subscription webhook can locate the right row.
//  5. Return the redirect URL — the frontend navigates to it.
//
// Idempotency: we send Stripe a UUIDv7 Idempotency-Key so a
// double-click from the SPA doesn't create two sessions.
func CheckoutSession(c *gin.Context) {
	userID, ok := requireUserID(c)
	if !ok {
		return
	}
	var req CheckoutSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", "Malformed request body")
		return
	}
	req.PlanCode = strings.TrimSpace(req.PlanCode)

	plan, ok := plans.Lookup(req.PlanCode)
	if !ok {
		response.BadRequest(c, "INVALID_PLAN", "Unknown plan code")
		return
	}
	if !plan.SelfServe {
		response.BadRequest(c, "INVALID_PLAN", "This plan is not available via self-serve; please contact sales.")
		return
	}
	// Free has no Stripe presence — the user can pick it via
	// the regular /me/subscribe endpoint without a card.
	if plan.Code == plans.FreeCode {
		response.BadRequest(c, "INVALID_PLAN", "Free plan does not require checkout; call /v1/billing/me/subscribe directly.")
		return
	}

	seats := req.Seats
	if seats < 1 {
		seats = 1
	}
	if !plan.PerSeat && seats > 1 {
		response.BadRequest(c, "INVALID_PLAN", "This plan does not support multiple seats")
		return
	}

	priceID, err := lookupStripePriceID(plan.Code)
	if err != nil {
		// Operator error (no mapping for this plan) — 500.
		// The frontend can't fix this; the deploy needs
		// STRIPE_PRICE_IDS updated.
		response.InternalErrorf(c, "STRIPE_NOT_CONFIGURED", "Stripe price mapping is not configured for this plan.",
			err, "plan_code", plan.Code)
		return
	}

	successURL := strings.TrimSpace(os.Getenv(stripeCheckoutSuccessURLEnv))
	cancelURL := strings.TrimSpace(os.Getenv(stripeCheckoutCancelURLEnv))
	if successURL == "" || cancelURL == "" {
		response.Err(c, http.StatusServiceUnavailable, "CHECKOUT_DISABLED",
			"Stripe checkout success/cancel URLs are not configured on this server.")
		return
	}

	client, err := stripeFactory()
	if err != nil {
		response.Err(c, http.StatusServiceUnavailable, "CHECKOUT_DISABLED",
			"Stripe outbound is not configured on this server.")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	idemKey := uuid.Must(uuid.NewV7()).String()
	session, err := client.CreateCheckoutSession(ctx, stripeclient.CheckoutSessionRequest{
		PriceID:    priceID,
		Quantity:   seats,
		SuccessURL: successURL,
		CancelURL:  cancelURL,
		Metadata: map[string]string{
			"user_id":   userID.String(),
			"plan_code": plan.Code,
		},
		IdempotencyKey: idemKey,
	})
	if err != nil {
		// Surface Stripe's structured error code so the SPA
		// can map to a useful message. Don't 500 — Stripe-side
		// problems often resolve with a retry, and the user
		// shouldn't see a generic "internal error" page.
		var se *stripeclient.StripeError
		if errors.As(err, &se) {
			slog.Warn("billing-service: stripe checkout session failed",
				"plan_code", plan.Code, "type", se.Type, "code", se.Code, "status", se.HTTPStatus)
			response.Err(c, http.StatusBadGateway, "STRIPE_ERROR",
				fmt.Sprintf("Stripe rejected the request: %s", se.Message))
			return
		}
		response.InternalErrorf(c, "STRIPE_ERROR", "Could not start Stripe checkout", err,
			"plan_code", plan.Code)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":   true,
		"message":   "checkout session created",
		"sessionId": session.ID,
		"url":       session.URL,
	})
}

// lookupStripePriceID parses STRIPE_PRICE_IDS at call time and
// returns the price for `planCode`. Parsing-on-each-call costs
// microseconds and means an operator can `kubectl set env` the
// mapping without restarting the pod. The map is small enough
// that re-parsing isn't a concern.
//
// Exposed at package level so tests can drive it without going
// through the HTTP handler.
func lookupStripePriceID(planCode string) (string, error) {
	raw := strings.TrimSpace(os.Getenv(stripePriceIDsEnv))
	if raw == "" {
		return "", errors.New("STRIPE_PRICE_IDS is not set")
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "", fmt.Errorf("STRIPE_PRICE_IDS is not valid JSON: %w", err)
	}
	id, ok := m[planCode]
	if !ok || strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("STRIPE_PRICE_IDS has no entry for plan_code=%s", planCode)
	}
	return id, nil
}
