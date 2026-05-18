package authverify

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-do-not-use-in-prod"

func newTestVerifier(t *testing.T) *Verifier {
	t.Helper()
	v, err := NewVerifier(VerifierConfig{
		AllowedAlgs: []string{"HS256"},
		HMACSecret:  []byte(testSecret),
		ClockSkew:   60 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func mintToken(t *testing.T, claims Claims) string {
	t.Helper()
	if claims.ExpiresAt == nil {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	}
	if claims.IssuedAt == nil {
		claims.IssuedAt = jwt.NewNumericDate(time.Now())
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func okHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCtx, ok := FromContext(r.Context())
		if !ok {
			t.Error("inner handler called without AuthContext in ctx")
		}
		if authCtx.UserID == "" {
			t.Error("inner handler called with empty UserID")
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_RejectsMissingToken(t *testing.T) {
	v := newTestVerifier(t)
	h := Middleware(MiddlewareOptions{Verifier: v}, okHandler(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMiddleware_AcceptsBearerHeader(t *testing.T) {
	v := newTestVerifier(t)
	token := mintToken(t, Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
	})
	h := Middleware(MiddlewareOptions{Verifier: v}, okHandler(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestMiddleware_AcceptsCookie(t *testing.T) {
	v := newTestVerifier(t)
	token := mintToken(t, Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
	})
	h := Middleware(MiddlewareOptions{Verifier: v}, okHandler(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect", nil)
	req.AddCookie(&http.Cookie{Name: "access_token", Value: token})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestMiddleware_AcceptsQueryParam(t *testing.T) {
	// Browser WS clients can't set headers and may not have
	// cookies (CORS-restricted iframes). Query-param is the
	// documented fallback for that case.
	v := newTestVerifier(t)
	token := mintToken(t, Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
	})
	h := Middleware(MiddlewareOptions{Verifier: v}, okHandler(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect?access_token="+token, nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestMiddleware_RejectsInvalidToken(t *testing.T) {
	v := newTestVerifier(t)
	h := Middleware(MiddlewareOptions{Verifier: v}, okHandler(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect", nil)
	req.Header.Set("Authorization", "Bearer nonsense.not.a.jwt")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMiddleware_RejectsExpiredToken(t *testing.T) {
	v := newTestVerifier(t)
	token := mintToken(t, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-3 * time.Hour)),
		},
	})
	h := Middleware(MiddlewareOptions{Verifier: v}, okHandler(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestMiddleware_RejectsGuestToken(t *testing.T) {
	// Even if the signature is valid, an is_guest=true claim
	// must be refused — collab requires an owned identity.
	v := newTestVerifier(t)
	token := mintToken(t, Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "guest-1"},
		IsGuest:          true,
	})
	h := Middleware(MiddlewareOptions{Verifier: v}, okHandler(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for guest", rec.Code)
	}
}

func TestMiddleware_TrustGatewayHeaders(t *testing.T) {
	// When AUTH_TRUST_GATEWAY_HEADERS=true and X-User-ID is set,
	// JWT verification is skipped.
	v := newTestVerifier(t)
	h := Middleware(MiddlewareOptions{
		Verifier:            v,
		TrustGatewayHeaders: true,
	}, okHandler(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect", nil)
	req.Header.Set("X-User-ID", "user-from-gateway")
	req.Header.Set("X-User-Role", "editor")
	req.Header.Set("X-User-Scope", "docs:read docs:write")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when gateway headers trusted", rec.Code)
	}
}

func TestMiddleware_TrustGatewayHeaders_FallsBackToJWTWhenAbsent(t *testing.T) {
	// TrustGatewayHeaders=true is "use header IF set"; if the
	// header is absent we still need to fall back to JWT
	// verification rather than blindly admitting the request.
	v := newTestVerifier(t)
	h := Middleware(MiddlewareOptions{
		Verifier:            v,
		TrustGatewayHeaders: true,
	}, okHandler(t))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect", nil)
	// No X-User-ID, no token.
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no header and no token", rec.Code)
	}
}

func TestMiddleware_OptionsPassesThrough(t *testing.T) {
	v := newTestVerifier(t)
	// We can't use okHandler here because OPTIONS requests
	// won't have an AuthContext attached. Use a plain handler.
	called := false
	h := Middleware(MiddlewareOptions{Verifier: v},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/v1/docs/x/connect", nil)
	h.ServeHTTP(rec, req)
	if !called {
		t.Error("OPTIONS request was blocked by middleware")
	}
}

func TestMiddleware_BearerBeatsCookieBeatsQuery(t *testing.T) {
	// Verifies the documented precedence: bearer > cookie > query.
	// We mint three tokens with distinct subjects, attach all
	// three, and assert the inner handler observes the bearer one.
	v := newTestVerifier(t)
	bearerTok := mintToken(t, Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "from-bearer"}})
	cookieTok := mintToken(t, Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "from-cookie"}})
	queryTok := mintToken(t, Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "from-query"}})

	got := make(chan string, 1)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCtx, _ := FromContext(r.Context())
		got <- authCtx.UserID
		w.WriteHeader(http.StatusOK)
	})
	h := Middleware(MiddlewareOptions{Verifier: v}, inner)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/docs/x/connect?access_token="+queryTok, nil)
	req.Header.Set("Authorization", "Bearer "+bearerTok)
	req.AddCookie(&http.Cookie{Name: "access_token", Value: cookieTok})
	h.ServeHTTP(rec, req)
	if uid := <-got; uid != "from-bearer" {
		t.Errorf("UserID = %q, want %q (bearer must win)", uid, "from-bearer")
	}
}
