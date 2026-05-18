package authverify

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestNewVerifierNoAlgorithms(t *testing.T) {
	_, err := NewVerifier(VerifierConfig{AllowedAlgs: []string{}})
	if err == nil {
		t.Error("expected error for no algorithms")
	}
}

func TestNewVerifierNoneAlgorithmRejected(t *testing.T) {
	_, err := NewVerifier(VerifierConfig{AllowedAlgs: []string{"none"}})
	if err == nil {
		t.Error("expected error for 'none' algorithm")
	}
}

func TestNewVerifierHS256WithoutSecret(t *testing.T) {
	_, err := NewVerifier(VerifierConfig{AllowedAlgs: []string{"HS256"}, HMACSecret: nil})
	if err == nil {
		t.Error("expected error for HS256 without secret")
	}
}

func TestNewVerifierValidHS256(t *testing.T) {
	v, err := NewVerifier(VerifierConfig{AllowedAlgs: []string{"HS256"}, HMACSecret: []byte("test-secret-key-32-chars-long!!")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v == nil {
		t.Fatal("expected non-nil verifier")
	}
}

func TestNewVerifierEmptyAlgsFiltered(t *testing.T) {
	_, err := NewVerifier(VerifierConfig{AllowedAlgs: []string{"", "  ", ""}})
	if err == nil {
		t.Error("expected error when all algs are empty/whitespace")
	}
}

func TestVerifyEmptyToken(t *testing.T) {
	v, _ := NewVerifier(VerifierConfig{AllowedAlgs: []string{"HS256"}, HMACSecret: []byte("test-secret-key-32-chars-long!!")})
	_, err := v.Verify(context.Background(), "")
	if err != ErrTokenMissing {
		t.Errorf("expected ErrTokenMissing, got %v", err)
	}
}

func TestVerifyWhitespaceToken(t *testing.T) {
	v, _ := NewVerifier(VerifierConfig{AllowedAlgs: []string{"HS256"}, HMACSecret: []byte("test-secret-key-32-chars-long!!")})
	_, err := v.Verify(context.Background(), "   ")
	if err != ErrTokenMissing {
		t.Errorf("expected ErrTokenMissing, got %v", err)
	}
}

func TestVerifyValidToken(t *testing.T) {
	secret := []byte("test-secret-key-32-chars-long!!")
	v, _ := NewVerifier(VerifierConfig{AllowedAlgs: []string{"HS256"}, HMACSecret: secret})

	now := time.Now()
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		Role: "user",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}

	result, err := v.Verify(context.Background(), tokenStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Subject != "user-123" {
		t.Errorf("expected subject 'user-123', got %q", result.Subject)
	}
	if result.Role != "user" {
		t.Errorf("expected role 'user', got %q", result.Role)
	}
}

func TestVerifyExpiredToken(t *testing.T) {
	secret := []byte("test-secret-key-32-chars-long!!")
	v, _ := NewVerifier(VerifierConfig{AllowedAlgs: []string{"HS256"}, HMACSecret: secret, ClockSkew: 0})

	past := time.Now().Add(-2 * time.Hour)
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			IssuedAt:  jwt.NewNumericDate(past),
			ExpiresAt: jwt.NewNumericDate(past.Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString(secret)

	_, err := v.Verify(context.Background(), tokenStr)
	if err != ErrTokenExpired {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestVerifyMissingSubject(t *testing.T) {
	secret := []byte("test-secret-key-32-chars-long!!")
	v, _ := NewVerifier(VerifierConfig{AllowedAlgs: []string{"HS256"}, HMACSecret: secret})

	now := time.Now()
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString(secret)

	_, err := v.Verify(context.Background(), tokenStr)
	if err != ErrTokenInvalid {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestVerifyWithDenylist(t *testing.T) {
	secret := []byte("test-secret-key-32-chars-long!!")
	denylist := &mockDenylist{denied: true}
	v, _ := NewVerifier(VerifierConfig{AllowedAlgs: []string{"HS256"}, HMACSecret: secret, Denylist: denylist})

	now := time.Now()
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString(secret)

	_, err := v.Verify(context.Background(), tokenStr)
	if err != ErrTokenInvalid {
		t.Errorf("expected ErrTokenInvalid for denied token, got %v", err)
	}
}

func TestVerifyInvalidSignature(t *testing.T) {
	secret := []byte("test-secret-key-32-chars-long!!")
	v, _ := NewVerifier(VerifierConfig{AllowedAlgs: []string{"HS256"}, HMACSecret: secret})

	wrongSecret := []byte("wrong-secret-key-32-chars-long!!")
	now := time.Now()
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString(wrongSecret)

	_, err := v.Verify(context.Background(), tokenStr)
	if err != ErrTokenInvalid {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestVerifyWithIssuerAndAudience(t *testing.T) {
	secret := []byte("test-secret-key-32-chars-long!!")
	v, _ := NewVerifier(VerifierConfig{
		AllowedAlgs: []string{"HS256"},
		HMACSecret:  secret,
		Issuer:      "fyredocs",
		Audience:    "fyredocs-api",
	})

	now := time.Now()
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			Issuer:    "fyredocs",
			Audience:  []string{"fyredocs-api"},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString(secret)

	result, err := v.Verify(context.Background(), tokenStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Subject != "user-123" {
		t.Errorf("expected subject 'user-123', got %q", result.Subject)
	}
}

type mockDenylist struct {
	denied bool
}

func (m *mockDenylist) IsTokenDenied(_ context.Context, _ string) (bool, error) {
	return m.denied, nil
}

func (m *mockDenylist) DenyToken(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func TestParseCommaList(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"HS256", 1},
		{"HS256,RS256", 2},
		{"HS256, RS256, ES256", 3},
		{"", 0},
	}
	for _, tt := range tests {
		got := parseCommaList(tt.input)
		if len(got) != tt.want {
			t.Errorf("parseCommaList(%q) len = %d, want %d", tt.input, len(got), tt.want)
		}
	}
}

// Dual-key rotation tests. See SECRETS.md §3.

// signHS256 mints a valid HS256 token signed with the given secret.
func signHS256(t *testing.T, secret []byte, subject string) string {
	t.Helper()
	now := time.Now()
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		Role: "user",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// TestVerify_DualKey_AcceptsPreviousSignature: during rotation, a token
// signed with the previous secret must still verify.
func TestVerify_DualKey_AcceptsPreviousSignature(t *testing.T) {
	current := []byte("new-secret-32-chars-long!!!!!!!!")
	previous := []byte("old-secret-32-chars-long!!!!!!!!")
	v, err := NewVerifier(VerifierConfig{
		AllowedAlgs:        []string{"HS256"},
		HMACSecret:         current,
		PreviousHMACSecret: previous,
	})
	if err != nil {
		t.Fatal(err)
	}
	tok := signHS256(t, previous, "user-prev")
	claims, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify with previous secret should succeed: %v", err)
	}
	if claims.Subject != "user-prev" {
		t.Errorf("subject = %q, want %q", claims.Subject, "user-prev")
	}
}

// TestVerify_DualKey_AcceptsCurrentSignature: tokens signed with the current
// secret must continue to verify when previous is also configured.
func TestVerify_DualKey_AcceptsCurrentSignature(t *testing.T) {
	current := []byte("new-secret-32-chars-long!!!!!!!!")
	previous := []byte("old-secret-32-chars-long!!!!!!!!")
	v, _ := NewVerifier(VerifierConfig{
		AllowedAlgs:        []string{"HS256"},
		HMACSecret:         current,
		PreviousHMACSecret: previous,
	})
	tok := signHS256(t, current, "user-current")
	claims, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify with current secret should succeed: %v", err)
	}
	if claims.Subject != "user-current" {
		t.Errorf("subject = %q, want %q", claims.Subject, "user-current")
	}
}

// TestVerify_DualKey_RejectsThirdSignature: a token signed with a secret
// that is neither current nor previous must still be rejected.
func TestVerify_DualKey_RejectsThirdSignature(t *testing.T) {
	v, _ := NewVerifier(VerifierConfig{
		AllowedAlgs:        []string{"HS256"},
		HMACSecret:         []byte("current-secret-32-chars-long!!!!"),
		PreviousHMACSecret: []byte("previous-secret-32-chars-long!!!"),
	})
	tok := signHS256(t, []byte("attacker-secret-32-chars-long!!!"), "user-evil")
	if _, err := v.Verify(context.Background(), tok); err != ErrTokenInvalid {
		t.Errorf("Verify with unknown secret = %v, want ErrTokenInvalid", err)
	}
}

// TestVerify_NoDualKey_RejectsPreviousSignature: when no previous secret
// is configured (the steady-state case), a token signed with anything but
// the current secret must be rejected.
func TestVerify_NoDualKey_RejectsPreviousSignature(t *testing.T) {
	v, _ := NewVerifier(VerifierConfig{
		AllowedAlgs: []string{"HS256"},
		HMACSecret:  []byte("current-secret-32-chars-long!!!!"),
	})
	tok := signHS256(t, []byte("previous-secret-32-chars-long!!!"), "user-prev")
	if _, err := v.Verify(context.Background(), tok); err != ErrTokenInvalid {
		t.Errorf("Verify without previous-secret config = %v, want ErrTokenInvalid", err)
	}
}
