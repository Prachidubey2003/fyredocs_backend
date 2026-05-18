package authverify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLooksLikeAPIKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"fyr_live_aaaaaa_bbb", true},
		{"fyr_test_aaaaaa_bbb", true},
		// Just the prefix counts — the oracle does the real validation.
		// A static prefix check is intentionally permissive: false
		// positives cost one extra RPC, false negatives mean a valid
		// key would be routed through the JWT verifier and rejected.
		{"fyr_", true},
		{"sk_live_aaaaaa_bbb", false},
		{"eyJhbGciOi...", false}, // looks like a JWT
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := LooksLikeAPIKey(tc.in); got != tc.want {
				t.Errorf("LooksLikeAPIKey(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestVerify_HappyPath stands up a fake oracle that returns valid
// claims; the verifier should round-trip the request body and parse
// the response into an AuthContext.
func TestAPIKeyVerifier_HappyPath(t *testing.T) {
	var receivedBody string
	var receivedContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"success": true,
			"message": "ok",
			"data": {
				"userId": "01234567-89ab-cdef-0123-456789abcdef",
				"environment": "live",
				"scopes": ["documents:read"]
			}
		}`))
	}))
	defer server.Close()

	v := &APIKeyVerifier{BaseURL: server.URL}
	claims, err := v.Verify(context.Background(), "fyr_live_aaaaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Errorf("UserID = %q, want the oracle's value", claims.UserID)
	}
	if claims.Environment != "live" {
		t.Errorf("Environment = %q, want live", claims.Environment)
	}
	if len(claims.Scopes) != 1 || claims.Scopes[0] != "documents:read" {
		t.Errorf("Scopes = %v, want [documents:read]", claims.Scopes)
	}
	if !strings.Contains(receivedBody, "fyr_live_") {
		t.Errorf("oracle didn't receive the token; body=%q", receivedBody)
	}
	if receivedContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", receivedContentType)
	}

	authCtx := claims.ToAuthContext()
	if authCtx.UserID != claims.UserID {
		t.Errorf("ToAuthContext drops UserID")
	}
	if len(authCtx.Scope) != 1 {
		t.Errorf("ToAuthContext drops Scope")
	}
}

func TestAPIKeyVerifier_Invalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"error":{"code":"INVALID_API_KEY"}}`))
	}))
	defer server.Close()

	v := &APIKeyVerifier{BaseURL: server.URL}
	_, err := v.Verify(context.Background(), "fyr_live_xxx")
	if !errors.Is(err, ErrAPIKeyInvalid) {
		t.Errorf("err = %v, want ErrAPIKeyInvalid", err)
	}
}

func TestAPIKeyVerifier_OracleDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	v := &APIKeyVerifier{BaseURL: server.URL}
	_, err := v.Verify(context.Background(), "fyr_live_xxx")
	if !errors.Is(err, ErrAPIKeyUnreachable) {
		t.Errorf("err = %v, want ErrAPIKeyUnreachable for 500", err)
	}
}

func TestAPIKeyVerifier_OracleReturns400(t *testing.T) {
	// A 400 from the oracle (e.g., empty token in our internal RPC)
	// is bucketed as Unreachable, not Invalid — operator-visible
	// failure rather than confusingly-quiet 401 at the gateway.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	v := &APIKeyVerifier{BaseURL: server.URL}
	_, err := v.Verify(context.Background(), "fyr_live_xxx")
	if !errors.Is(err, ErrAPIKeyUnreachable) {
		t.Errorf("err = %v, want ErrAPIKeyUnreachable for 400", err)
	}
}

func TestAPIKeyVerifier_MissingBaseURL(t *testing.T) {
	v := &APIKeyVerifier{} // BaseURL unset
	_, err := v.Verify(context.Background(), "fyr_live_xxx")
	if !errors.Is(err, ErrAPIKeyUnreachable) {
		t.Errorf("err = %v, want ErrAPIKeyUnreachable for unconfigured verifier", err)
	}
}

func TestAPIKeyVerifier_GarbageJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	v := &APIKeyVerifier{BaseURL: server.URL}
	_, err := v.Verify(context.Background(), "fyr_live_xxx")
	if !errors.Is(err, ErrAPIKeyUnreachable) {
		t.Errorf("err = %v, want ErrAPIKeyUnreachable for malformed oracle response", err)
	}
}

func TestAPIKeyVerifier_SuccessFalseTreatedAsUnreachable(t *testing.T) {
	// A 200 OK with `success:false` shouldn't happen if the oracle
	// behaves, but if it does we treat it as a server-side fault
	// rather than authenticate the request with a missing userId.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	defer server.Close()

	v := &APIKeyVerifier{BaseURL: server.URL}
	_, err := v.Verify(context.Background(), "fyr_live_xxx")
	if !errors.Is(err, ErrAPIKeyUnreachable) {
		t.Errorf("err = %v, want ErrAPIKeyUnreachable for success:false", err)
	}
}
