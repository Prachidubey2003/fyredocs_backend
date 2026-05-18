// Package stripeclient is the minimal outbound HTTP client
// billing-service uses to call Stripe's REST API. We deliberately
// do NOT pull in the official `stripe-go` SDK — it's a ~30k-LOC
// dependency that surfaces every Stripe resource (90% unused),
// auto-retries with its own backoff (conflicts with our
// circuit-breaker plan), and ties us to a Stripe-controlled
// release cadence. Rolling a focused wrapper keeps the dep
// surface zero and the testable surface tiny.
//
// Scope (v0):
//   - CreateCheckoutSession — the only outbound call
//     billing-service makes today. The Stripe-side checkout flow
//     handles cards / Apple Pay / SEPA without us touching PCI.
//
// Future endpoints (CreateCustomer, CancelSubscription, etc.)
// add methods here. The shape of the client (BaseURL +
// http.Client + secret-key auth) is what the rest of the package
// can rely on.
//
// What this package does NOT do:
//   - Auto-retry. Stripe's idempotency-key contract is sound,
//     but retry policy belongs to the caller (which can wrap
//     with shared/backoff at the handler level).
//   - Webhook signature verification. That lives in
//     [billing-service/internal/stripeauth] — separate concern.
//   - Mock the whole Stripe surface for tests. Tests inject a
//     custom BaseURL pointing at httptest.NewServer.
package stripeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is Stripe's production API endpoint. Tests
// override it with an httptest server URL. Test-mode vs live-
// mode is decided by the SecretKey prefix (`sk_test_…` vs
// `sk_live_…`), not by the base URL.
const DefaultBaseURL = "https://api.stripe.com"

// DefaultTimeout caps every outbound Stripe request. Stripe's
// docs target sub-second response times; 15s is well above
// their p99 + leaves time for the caller's own retry without
// blocking the gin handler for too long.
const DefaultTimeout = 15 * time.Second

// Client is the configured Stripe HTTP wrapper. Safe for
// concurrent use across goroutines — the embedded http.Client
// owns its own connection pool.
type Client struct {
	// SecretKey is the `sk_test_…` / `sk_live_…` auth bearer.
	// Required. The client refuses to operate without one.
	SecretKey string

	// BaseURL is the API origin. Defaults to DefaultBaseURL
	// when empty.
	BaseURL string

	// HTTPClient is the transport. Optional; defaults to an
	// http.Client with DefaultTimeout. Tests inject their own
	// to attach inspection hooks.
	HTTPClient *http.Client
}

// ErrEmptySecretKey is returned when a Client is invoked
// without a SecretKey. Indicates a config bug at the caller —
// failing closed is the right default (the alternative,
// silently making requests with no auth, is much worse).
var ErrEmptySecretKey = errors.New("stripeclient: SecretKey is empty")

// CheckoutSessionRequest captures everything CreateCheckoutSession
// needs to assemble Stripe's form-encoded body. Exposed as a
// struct so handlers can build it from inbound HTTP request
// data without depending on the wire-format shape.
type CheckoutSessionRequest struct {
	// PriceID is Stripe's `price_…` identifier — the per-plan
	// pricing config the customer is subscribing to. Required.
	PriceID string

	// Quantity is the seat count for per-seat plans. Defaults
	// to 1 if 0.
	Quantity int

	// CustomerID is `cus_…` if the user has an existing
	// Stripe customer. Optional; Stripe creates a fresh
	// customer when absent (and `CustomerEmail` populates the
	// new customer's email field).
	CustomerID string

	// CustomerEmail seeds a NEW Stripe customer's email when
	// CustomerID is absent. Stripe ignores this when
	// CustomerID is supplied (the existing customer wins).
	CustomerEmail string

	// SuccessURL / CancelURL are where Stripe redirects after
	// the user completes / abandons the checkout. Required by
	// Stripe; the handler builds them from request context.
	SuccessURL string
	CancelURL  string

	// Metadata carries our user_id + plan_code through the
	// Checkout Session and onto the eventual Subscription —
	// the webhook handler reads these to look up the right
	// row.
	Metadata map[string]string

	// IdempotencyKey is an optional caller-supplied key that
	// Stripe uses to dedupe retries. Generate a fresh UUID
	// per logical operation (NOT per network attempt) so a
	// user clicking "subscribe" twice doesn't spawn two
	// sessions.
	IdempotencyKey string
}

// CheckoutSessionResponse is the slice of Stripe's checkout-
// session response the caller actually reads. Stripe returns
// dozens of fields; the handler just needs the redirect URL +
// the session id (for analytics + debugging).
type CheckoutSessionResponse struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// CreateCheckoutSession POSTs to /v1/checkout/sessions and
// returns the resulting redirect URL. Caller forwards the URL
// to the frontend, which navigates to it (302 from the
// gateway or a JSON-wrapped URL the SPA opens).
//
// Errors:
//   - ErrEmptySecretKey when the client wasn't configured.
//   - StripeError on a non-2xx response — includes Stripe's
//     own error code so the caller can map to a user-visible
//     message ("card_declined", "invalid_request_error", …).
//   - context errors propagate verbatim.
func (c *Client) CreateCheckoutSession(ctx context.Context, req CheckoutSessionRequest) (*CheckoutSessionResponse, error) {
	if c.SecretKey == "" {
		return nil, ErrEmptySecretKey
	}
	if req.PriceID == "" {
		return nil, errors.New("stripeclient: PriceID is required")
	}
	if req.SuccessURL == "" || req.CancelURL == "" {
		return nil, errors.New("stripeclient: SuccessURL and CancelURL are required")
	}

	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("success_url", req.SuccessURL)
	form.Set("cancel_url", req.CancelURL)
	form.Set("line_items[0][price]", req.PriceID)
	qty := req.Quantity
	if qty < 1 {
		qty = 1
	}
	form.Set("line_items[0][quantity]", strconv.Itoa(qty))

	switch {
	case req.CustomerID != "":
		form.Set("customer", req.CustomerID)
	case req.CustomerEmail != "":
		// Stripe's "let Checkout build the customer" path —
		// the subscription event will land via webhook with
		// a fresh `cus_…` we then store on our Subscription
		// row.
		form.Set("customer_email", req.CustomerEmail)
	}

	// Metadata lands on the Checkout Session AND
	// `subscription_data.metadata` lands on the resulting
	// Subscription — both paths matter for the webhook
	// handler. Stripe deep-clones subscription_data.metadata
	// to every invoice that follows.
	for k, v := range req.Metadata {
		form.Set("metadata["+k+"]", v)
		form.Set("subscription_data[metadata]["+k+"]", v)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		c.baseURL()+"/v1/checkout/sessions",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.SecretKey)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Stripe-Version", "2024-06-20")
	if req.IdempotencyKey != "" {
		httpReq.Header.Set("Idempotency-Key", req.IdempotencyKey)
	}

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stripeclient: post checkout session: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("stripeclient: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseStripeError(resp.StatusCode, body)
	}

	var out CheckoutSessionResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("stripeclient: decode response: %w", err)
	}
	if out.URL == "" {
		// Stripe always returns a `url` on a successful
		// checkout-session create. An empty URL means the
		// response shape changed unexpectedly — fail loud.
		return nil, errors.New("stripeclient: checkout session response missing url")
	}
	return &out, nil
}

// Charge is the slice of Stripe's `charge` object the
// reconciliation path reads. Stripe exposes ~70 fields on a
// Charge; we keep two — the id (round-trip safety) and the
// linked balance_transaction (non-expanded string form). The
// fee reconciliation flow uses this id to chain into
// `GetBalanceTransaction` and recover the processor fee a row
// missed at webhook time.
//
// Note: when Stripe expands `balance_transaction`, the field
// arrives as a JSON object rather than a string. We
// deliberately do NOT request expansion here — the two-call
// pattern (GetCharge then GetBalanceTransaction) keeps each
// Stripe call independently observable and lets the
// reconciliation loop handle partial failures cleanly.
type Charge struct {
	ID                 string `json:"id"`
	BalanceTransaction string `json:"balance_transaction"`
}

// GetCharge fetches a single charge by id. Used by the
// payout-reconciliation pass to recover the linked
// balance_transaction id for rows whose webhook-time lookup
// failed (the charge object is the only authoritative carrier
// of the BT id; the row only knows the charge id).
//
// Errors mirror GetBalanceTransaction: ErrEmptySecretKey, a
// structured StripeError on non-2xx, context errors verbatim.
func (c *Client) GetCharge(ctx context.Context, chargeID string) (*Charge, error) {
	if c.SecretKey == "" {
		return nil, ErrEmptySecretKey
	}
	if chargeID == "" {
		return nil, errors.New("stripeclient: charge id is required")
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodGet,
		c.baseURL()+"/v1/charges/"+url.PathEscape(chargeID),
		nil,
	)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.SecretKey)
	httpReq.Header.Set("Stripe-Version", "2024-06-20")

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stripeclient: get charge: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("stripeclient: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseStripeError(resp.StatusCode, body)
	}

	var out Charge
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("stripeclient: decode response: %w", err)
	}
	return &out, nil
}

// BalanceTransaction is the slice of Stripe's `balance_transaction`
// object the marketplace revshare path reads. Stripe exposes ~25
// fields; we keep three (id, fee, currency) — the fee is what
// drives the revshare split, currency lets the caller spot a
// presentment-currency mismatch.
//
// Fees in Stripe are always in the smallest currency unit (cents
// for USD), and may include multiple `fee_details` entries
// (Stripe fee + Connect application fee + tax). We sum them all
// into the top-level `fee` total which Stripe also returns —
// callers care about total cost-to-record-the-charge, not the
// breakdown.
type BalanceTransaction struct {
	ID       string `json:"id"`
	Fee      int64  `json:"fee"`
	Currency string `json:"currency"`
}

// GetBalanceTransaction fetches a single balance-transaction by
// id. Used by the charge.succeeded handler to populate
// `revshare_entries.stripe_fee_cents` — every Stripe charge
// references a balance-transaction (`btx_…`) that records the
// processor fee Stripe charged us.
//
// Errors:
//   - ErrEmptySecretKey when the client wasn't configured.
//   - StripeError on a non-2xx response.
//   - context errors propagate verbatim.
//
// Caller is responsible for handling lookup failures gracefully —
// the revshare entry should still record (with fee=0) even when
// the BT lookup fails, so a payout-reconciliation job can
// backfill the fee later from Stripe's BT export.
func (c *Client) GetBalanceTransaction(ctx context.Context, btxID string) (*BalanceTransaction, error) {
	if c.SecretKey == "" {
		return nil, ErrEmptySecretKey
	}
	if btxID == "" {
		return nil, errors.New("stripeclient: balance-transaction id is required")
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodGet,
		c.baseURL()+"/v1/balance_transactions/"+url.PathEscape(btxID),
		nil,
	)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.SecretKey)
	httpReq.Header.Set("Stripe-Version", "2024-06-20")

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("stripeclient: get balance_transaction: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("stripeclient: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseStripeError(resp.StatusCode, body)
	}

	var out BalanceTransaction
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("stripeclient: decode response: %w", err)
	}
	return &out, nil
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: DefaultTimeout}
}

// StripeError carries Stripe's structured error payload back
// to the caller. Stripe sets HTTP status + a JSON body with
// `error.type` / `error.code` / `error.message` — preserving
// all three lets the handler map the response to a meaningful
// user-visible string without re-introducing tight coupling
// to the wire format upstream.
type StripeError struct {
	HTTPStatus int    `json:"-"`
	Type       string `json:"type"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

func (e *StripeError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("stripeclient: HTTP %d: %s (%s): %s", e.HTTPStatus, e.Type, e.Code, e.Message)
	}
	if e.Type != "" {
		return fmt.Sprintf("stripeclient: HTTP %d: %s: %s", e.HTTPStatus, e.Type, e.Message)
	}
	return fmt.Sprintf("stripeclient: HTTP %d: %s", e.HTTPStatus, e.Message)
}

// parseStripeError peels Stripe's `{"error": {...}}` envelope
// off a non-2xx response body. Falls back to a status-only
// error when the body isn't recognisable Stripe JSON (e.g.,
// the gateway between us and Stripe returns an HTML 502).
func parseStripeError(status int, body []byte) error {
	var wrapper struct {
		Error StripeError `json:"error"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && (wrapper.Error.Type != "" || wrapper.Error.Message != "") {
		wrapper.Error.HTTPStatus = status
		return &wrapper.Error
	}
	snippet := string(body)
	if len(snippet) > 200 {
		snippet = snippet[:200] + "…"
	}
	return &StripeError{
		HTTPStatus: status,
		Type:       "unknown",
		Message:    snippet,
	}
}
