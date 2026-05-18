package stripeclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newStubServer returns an httptest.Server whose handler the
// caller controls. Saves the boilerplate of constructing one
// inline in every test.
func newStubServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c := &Client{
		SecretKey: "sk_test_dummy",
		BaseURL:   s.URL,
	}
	return s, c
}

func TestCreateCheckoutSession_HappyPath(t *testing.T) {
	var capturedBody string
	var capturedAuth string
	var capturedIdempotency string

	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkout/sessions" {
			t.Errorf("path = %s, want /v1/checkout/sessions", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		capturedAuth = r.Header.Get("Authorization")
		capturedIdempotency = r.Header.Get("Idempotency-Key")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_abc","url":"https://checkout.stripe.com/c/pay/cs_test_abc"}`))
	})

	resp, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{
		PriceID:        "price_test_pro_monthly",
		Quantity:       3,
		CustomerEmail:  "user@example.com",
		SuccessURL:     "https://fyredocs.com/billing/success?session_id={CHECKOUT_SESSION_ID}",
		CancelURL:      "https://fyredocs.com/billing/cancel",
		Metadata:       map[string]string{"user_id": "u-123", "plan_code": "pro"},
		IdempotencyKey: "idem-xyz-789",
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if resp.ID != "cs_test_abc" || resp.URL == "" {
		t.Errorf("response = %+v", resp)
	}

	// Bearer auth sent correctly.
	if capturedAuth != "Bearer sk_test_dummy" {
		t.Errorf("Authorization = %q, want Bearer sk_test_dummy", capturedAuth)
	}
	// Idempotency-Key forwarded.
	if capturedIdempotency != "idem-xyz-789" {
		t.Errorf("Idempotency-Key = %q, want idem-xyz-789", capturedIdempotency)
	}

	// Body shape: mode=subscription, line_items[0][price] /
	// quantity, success_url, cancel_url, customer_email,
	// and both metadata + subscription_data.metadata for
	// each pair.
	expectedFragments := []string{
		"mode=subscription",
		"line_items%5B0%5D%5Bprice%5D=price_test_pro_monthly",
		"line_items%5B0%5D%5Bquantity%5D=3",
		"success_url=https%3A%2F%2Ffyredocs.com%2Fbilling%2Fsuccess",
		"cancel_url=https%3A%2F%2Ffyredocs.com%2Fbilling%2Fcancel",
		"customer_email=user%40example.com",
		"metadata%5Buser_id%5D=u-123",
		"metadata%5Bplan_code%5D=pro",
		"subscription_data%5Bmetadata%5D%5Buser_id%5D=u-123",
		"subscription_data%5Bmetadata%5D%5Bplan_code%5D=pro",
	}
	for _, want := range expectedFragments {
		if !strings.Contains(capturedBody, want) {
			t.Errorf("request body missing %q\nbody: %s", want, capturedBody)
		}
	}
}

func TestCreateCheckoutSession_UsesCustomerIDWhenSupplied(t *testing.T) {
	// When CustomerID is set, customer_email must NOT be sent
	// — Stripe ignores it but emitting it anyway pollutes
	// the wire trace + risks future Stripe behaviour change.
	var body string
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(`{"id":"cs_x","url":"https://checkout.stripe.com/cs_x"}`))
	})
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{
		PriceID:       "price_x",
		CustomerID:    "cus_existing",
		CustomerEmail: "ignored@example.com",
		SuccessURL:    "https://x/s",
		CancelURL:     "https://x/c",
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if !strings.Contains(body, "customer=cus_existing") {
		t.Errorf("body missing customer=cus_existing\n%s", body)
	}
	if strings.Contains(body, "customer_email=") {
		t.Errorf("body should NOT contain customer_email when CustomerID is set\n%s", body)
	}
}

func TestCreateCheckoutSession_DefaultsQuantityToOne(t *testing.T) {
	var body string
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(`{"id":"cs_q","url":"https://x"}`))
	})
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{
		PriceID:    "price_x",
		Quantity:   0, // unset → default 1
		SuccessURL: "https://x/s",
		CancelURL:  "https://x/c",
	})
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if !strings.Contains(body, "line_items%5B0%5D%5Bquantity%5D=1") {
		t.Errorf("quantity should default to 1; body = %s", body)
	}
}

func TestCreateCheckoutSession_ParsesStripeError(t *testing.T) {
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{
			"error": {
				"type": "invalid_request_error",
				"code": "resource_missing",
				"message": "No such price: price_does_not_exist"
			}
		}`))
	})
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{
		PriceID:    "price_does_not_exist",
		SuccessURL: "https://x/s",
		CancelURL:  "https://x/c",
	})
	if err == nil {
		t.Fatal("expected StripeError on 400; got nil")
	}
	var se *StripeError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StripeError; got %T: %v", err, err)
	}
	if se.HTTPStatus != http.StatusBadRequest {
		t.Errorf("HTTPStatus = %d, want 400", se.HTTPStatus)
	}
	if se.Type != "invalid_request_error" {
		t.Errorf("Type = %q", se.Type)
	}
	if se.Code != "resource_missing" {
		t.Errorf("Code = %q", se.Code)
	}
	if !strings.Contains(se.Message, "No such price") {
		t.Errorf("Message = %q", se.Message)
	}
}

func TestCreateCheckoutSession_FallsBackOnNonJSONErrorBody(t *testing.T) {
	// A gateway between us and Stripe might return a 502 with
	// an HTML body. The client must surface this as a
	// StripeError (with type="unknown") rather than panicking
	// or returning nil.
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>Bad Gateway</body></html>"))
	})
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{
		PriceID:    "price_x",
		SuccessURL: "https://x/s",
		CancelURL:  "https://x/c",
	})
	if err == nil {
		t.Fatal("expected error on 502; got nil")
	}
	var se *StripeError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StripeError; got %T: %v", err, err)
	}
	if se.HTTPStatus != http.StatusBadGateway {
		t.Errorf("HTTPStatus = %d, want 502", se.HTTPStatus)
	}
	if se.Type != "unknown" {
		t.Errorf("Type = %q, want unknown", se.Type)
	}
}

func TestCreateCheckoutSession_RejectsEmptySecretKey(t *testing.T) {
	c := &Client{SecretKey: ""} // BaseURL doesn't matter — never called
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{
		PriceID:    "p",
		SuccessURL: "s",
		CancelURL:  "c",
	})
	if !errors.Is(err, ErrEmptySecretKey) {
		t.Errorf("expected ErrEmptySecretKey; got %v", err)
	}
}

func TestCreateCheckoutSession_RejectsMissingPriceID(t *testing.T) {
	c := &Client{SecretKey: "sk_test_x"}
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{
		SuccessURL: "s",
		CancelURL:  "c",
	})
	if err == nil || !strings.Contains(err.Error(), "PriceID") {
		t.Errorf("expected PriceID error; got %v", err)
	}
}

func TestCreateCheckoutSession_RejectsMissingURLs(t *testing.T) {
	c := &Client{SecretKey: "sk_test_x"}
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{
		PriceID: "p",
	})
	if err == nil || !strings.Contains(err.Error(), "SuccessURL") {
		t.Errorf("expected SuccessURL/CancelURL error; got %v", err)
	}
}

func TestCreateCheckoutSession_RejectsMissingURLInResponse(t *testing.T) {
	// Stripe-side schema change would silently break the
	// checkout flow if we didn't validate the URL field came
	// back. Pin the regression now.
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"cs_no_url"}`))
	})
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionRequest{
		PriceID:    "p",
		SuccessURL: "https://x/s",
		CancelURL:  "https://x/c",
	})
	if err == nil || !strings.Contains(err.Error(), "missing url") {
		t.Errorf("expected missing-url error; got %v", err)
	}
}

func TestCreateCheckoutSession_PropagatesContextCancellation(t *testing.T) {
	// A canceled context must surface as ctx.Err, not as a
	// generic transport error. Important so the gin handler
	// can distinguish client-disconnect (don't retry) from
	// Stripe-side fail (retry).
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hold until the client cancels
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	_, err := c.CreateCheckoutSession(ctx, CheckoutSessionRequest{
		PriceID:    "p",
		SuccessURL: "https://x/s",
		CancelURL:  "https://x/c",
	})
	if err == nil {
		t.Fatal("expected context-cancel error; got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled wrapped; got %v", err)
	}
}

// ---- GetBalanceTransaction ----

func TestGetBalanceTransaction_HappyPath(t *testing.T) {
	var capturedPath, capturedAuth, capturedMethod string
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedMethod = r.Method
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"btx_abc","fee":175,"currency":"usd"}`))
	})

	bt, err := c.GetBalanceTransaction(context.Background(), "btx_abc")
	if err != nil {
		t.Fatalf("GetBalanceTransaction: %v", err)
	}
	if bt.ID != "btx_abc" || bt.Fee != 175 || bt.Currency != "usd" {
		t.Errorf("response = %+v, want {btx_abc 175 usd}", bt)
	}
	if capturedPath != "/v1/balance_transactions/btx_abc" {
		t.Errorf("path = %s, want /v1/balance_transactions/btx_abc", capturedPath)
	}
	if capturedMethod != http.MethodGet {
		t.Errorf("method = %s, want GET", capturedMethod)
	}
	if capturedAuth != "Bearer sk_test_dummy" {
		t.Errorf("Authorization = %q", capturedAuth)
	}
}

func TestGetBalanceTransaction_RejectsEmptyID(t *testing.T) {
	c := &Client{SecretKey: "sk_test_dummy"}
	_, err := c.GetBalanceTransaction(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty BT id")
	}
}

func TestGetBalanceTransaction_RejectsEmptySecretKey(t *testing.T) {
	c := &Client{} // no key
	_, err := c.GetBalanceTransaction(context.Background(), "btx_abc")
	if !errors.Is(err, ErrEmptySecretKey) {
		t.Errorf("expected ErrEmptySecretKey; got %v", err)
	}
}

func TestGetBalanceTransaction_ParsesStripeError(t *testing.T) {
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{
			"error": {
				"type": "invalid_request_error",
				"code": "resource_missing",
				"message": "No such balance_transaction: btx_missing"
			}
		}`))
	})
	_, err := c.GetBalanceTransaction(context.Background(), "btx_missing")
	if err == nil {
		t.Fatal("expected stripe error; got nil")
	}
	var se *StripeError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StripeError wrapping; got %T %v", err, err)
	}
	if se.HTTPStatus != http.StatusNotFound || se.Code != "resource_missing" {
		t.Errorf("StripeError = %+v", se)
	}
}

func TestGetBalanceTransaction_URLEncodesID(t *testing.T) {
	// Stripe ids are alphanumeric in practice, but the handler
	// hands us whatever the upstream charge.balance_transaction
	// field contains — if it ever included a path-sensitive
	// character we MUST encode it so the request stays on the
	// expected resource. Verifies the url.PathEscape call.
	var capturedPath string
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath() preserves the on-wire %XX form;
		// r.URL.Path is already decoded and would hide an
		// encoding regression.
		capturedPath = r.URL.EscapedPath()
		w.Write([]byte(`{"id":"x","fee":0,"currency":"usd"}`))
	})
	_, err := c.GetBalanceTransaction(context.Background(), "btx/with/slashes")
	if err != nil {
		t.Fatalf("GetBalanceTransaction: %v", err)
	}
	// Slashes in the id segment must be percent-encoded so they
	// don't extend the URL path.
	if !strings.Contains(capturedPath, "btx%2Fwith%2Fslashes") {
		t.Errorf("escaped path = %s; want id-segment percent-encoded", capturedPath)
	}
}

// ---- GetCharge ----

func TestGetCharge_HappyPath(t *testing.T) {
	var capturedPath, capturedAuth string
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ch_abc","balance_transaction":"btx_xyz"}`))
	})

	ch, err := c.GetCharge(context.Background(), "ch_abc")
	if err != nil {
		t.Fatalf("GetCharge: %v", err)
	}
	if ch.ID != "ch_abc" || ch.BalanceTransaction != "btx_xyz" {
		t.Errorf("response = %+v, want {ch_abc btx_xyz}", ch)
	}
	if capturedPath != "/v1/charges/ch_abc" {
		t.Errorf("path = %s, want /v1/charges/ch_abc", capturedPath)
	}
	if capturedAuth != "Bearer sk_test_dummy" {
		t.Errorf("Authorization = %q", capturedAuth)
	}
}

func TestGetCharge_NoBalanceTransaction(t *testing.T) {
	// Test-mode charges + refunds-prior-to-posting carry no
	// balance_transaction. The client returns the empty
	// string and lets the caller decide what to do.
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"ch_test"}`))
	})
	ch, err := c.GetCharge(context.Background(), "ch_test")
	if err != nil {
		t.Fatalf("GetCharge: %v", err)
	}
	if ch.BalanceTransaction != "" {
		t.Errorf("expected empty BalanceTransaction; got %q", ch.BalanceTransaction)
	}
}

func TestGetCharge_RejectsEmptyID(t *testing.T) {
	c := &Client{SecretKey: "sk_test_dummy"}
	_, err := c.GetCharge(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty charge id")
	}
}

func TestGetCharge_RejectsEmptySecretKey(t *testing.T) {
	c := &Client{}
	_, err := c.GetCharge(context.Background(), "ch_abc")
	if !errors.Is(err, ErrEmptySecretKey) {
		t.Errorf("expected ErrEmptySecretKey; got %v", err)
	}
}

func TestGetCharge_ParsesStripeError(t *testing.T) {
	_, c := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{
			"error": {
				"type": "invalid_request_error",
				"code": "resource_missing",
				"message": "No such charge: ch_missing"
			}
		}`))
	})
	_, err := c.GetCharge(context.Background(), "ch_missing")
	if err == nil {
		t.Fatal("expected stripe error; got nil")
	}
	var se *StripeError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StripeError; got %T %v", err, err)
	}
	if se.HTTPStatus != http.StatusNotFound || se.Code != "resource_missing" {
		t.Errorf("StripeError = %+v", se)
	}
}
