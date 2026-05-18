// Package authverify is the collab-service copy of the JWT
// verification + auth-context plumbing used across the platform.
// Per [CLAUDE.md] §1 we don't import this from editor-service or
// job-service — each microservice owns its own auth code and the
// shape is duplicated. The duplication is intentional: each
// service can evolve the algorithm allowlist, denylist behaviour,
// and middleware shape independently.
//
// What's different from editor-service's copy:
//   - No GuestStore / guest-token path. Multiplayer editing
//     requires an authenticated account; guests can't join rooms.
//   - Stdlib net/http middleware (collab-service uses ServeMux,
//     not gin).
//   - Adds query-parameter token extraction so the browser WS
//     client can authenticate without a custom Authorization
//     header (browsers can't set headers on `new WebSocket()`).
package authverify

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"fyredocs/shared/config"
	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrTokenMissing = errors.New("token missing")
	ErrTokenInvalid = errors.New("token invalid")
	ErrTokenExpired = errors.New("token expired")
)

type VerifierConfig struct {
	AllowedAlgs []string
	HMACSecret  []byte
	// PreviousHMACSecret accepts tokens signed with the previous
	// HS256 key during zero-downtime rotation. New tokens are
	// always signed with HMACSecret; PreviousHMACSecret is
	// verify-only. See SECRETS.md §3.
	PreviousHMACSecret []byte
	RSAPublicKey       *rsa.PublicKey
	Issuer             string
	Audience           string
	ClockSkew          time.Duration
	Denylist           TokenDenylist
}

type Verifier struct {
	allowedAlgs        map[string]struct{}
	hmacSecret         []byte
	previousHMACSecret []byte
	rsaPublicKey       *rsa.PublicKey
	issuer             string
	audience           string
	clockSkew          time.Duration
	denylist           TokenDenylist
}

func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	allowed := make(map[string]struct{}, len(cfg.AllowedAlgs))
	for _, alg := range cfg.AllowedAlgs {
		alg = strings.TrimSpace(alg)
		if alg == "" {
			continue
		}
		if strings.EqualFold(alg, "none") {
			return nil, fmt.Errorf("disallowed jwt algorithm: none")
		}
		allowed[alg] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("no allowed jwt algorithms configured")
	}
	if _, ok := allowed[jwt.SigningMethodHS256.Alg()]; ok && len(cfg.HMACSecret) == 0 {
		return nil, fmt.Errorf("hs256 secret not configured")
	}
	if _, ok := allowed[jwt.SigningMethodRS256.Alg()]; ok && cfg.RSAPublicKey == nil {
		return nil, fmt.Errorf("rs256 public key not configured")
	}
	return &Verifier{
		allowedAlgs:        allowed,
		hmacSecret:         cfg.HMACSecret,
		previousHMACSecret: cfg.PreviousHMACSecret,
		rsaPublicKey:       cfg.RSAPublicKey,
		issuer:             strings.TrimSpace(cfg.Issuer),
		audience:           strings.TrimSpace(cfg.Audience),
		clockSkew:          cfg.ClockSkew,
		denylist:           cfg.Denylist,
	}, nil
}

func NewVerifierFromEnv(denylist TokenDenylist) (*Verifier, error) {
	allowed := parseCommaList(config.GetEnv("JWT_ALLOWED_ALGS", "HS256"))
	clockSkew := config.GetEnvDuration("JWT_CLOCK_SKEW", 60*time.Second)
	secret := os.Getenv("JWT_HS256_SECRET")
	if secret == "" {
		secret = os.Getenv("JWT_SECRET")
	}
	previousSecret := os.Getenv("JWT_HS256_SECRET_PREVIOUS")
	publicKey, err := parseRSAPublicKey(os.Getenv("JWT_RS256_PUBLIC_KEY"))
	if err != nil {
		return nil, err
	}
	return NewVerifier(VerifierConfig{
		AllowedAlgs:        allowed,
		HMACSecret:         []byte(secret),
		PreviousHMACSecret: []byte(previousSecret),
		RSAPublicKey:       publicKey,
		Issuer:             os.Getenv("JWT_ISSUER"),
		Audience:           os.Getenv("JWT_AUDIENCE"),
		ClockSkew:          clockSkew,
		Denylist:           denylist,
	})
}

func (v *Verifier) Verify(ctx context.Context, tokenString string) (*Claims, error) {
	if strings.TrimSpace(tokenString) == "" {
		return nil, ErrTokenMissing
	}
	if v.denylist != nil {
		denied, err := v.denylist.IsTokenDenied(ctx, tokenString)
		if err != nil {
			return nil, ErrTokenInvalid
		}
		if denied {
			return nil, ErrTokenInvalid
		}
	}

	options := []jwt.ParserOption{
		jwt.WithValidMethods(v.allowedMethods()),
		jwt.WithLeeway(v.clockSkew),
	}
	if v.issuer != "" {
		options = append(options, jwt.WithIssuer(v.issuer))
	}
	if v.audience != "" {
		options = append(options, jwt.WithAudience(v.audience))
	}
	parser := jwt.NewParser(options...)

	claims := &Claims{}
	token, err := parser.ParseWithClaims(tokenString, claims, v.keyFunc)

	// Dual-key fallback for HS256 zero-downtime rotation.
	if errors.Is(err, jwt.ErrTokenSignatureInvalid) && len(v.previousHMACSecret) > 0 {
		claims = &Claims{}
		token, err = parser.ParseWithClaims(tokenString, claims, v.keyFuncPrevious)
	}

	if err != nil || token == nil || !token.Valid {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrTokenInvalid
	}
	if strings.TrimSpace(claims.Subject) == "" {
		return nil, ErrTokenInvalid
	}
	if claims.ExpiresAt == nil || claims.IssuedAt == nil {
		return nil, ErrTokenInvalid
	}
	if claims.IssuedAt.Time.After(time.Now().Add(v.clockSkew)) {
		return nil, ErrTokenInvalid
	}
	return claims, nil
}

func (v *Verifier) keyFunc(token *jwt.Token) (interface{}, error) {
	if token == nil || token.Method == nil {
		return nil, ErrTokenInvalid
	}
	alg := token.Method.Alg()
	if _, ok := v.allowedAlgs[alg]; !ok {
		return nil, ErrTokenInvalid
	}
	if strings.EqualFold(alg, "none") {
		return nil, ErrTokenInvalid
	}
	switch alg {
	case jwt.SigningMethodHS256.Alg():
		return v.hmacSecret, nil
	case jwt.SigningMethodRS256.Alg():
		return v.rsaPublicKey, nil
	default:
		return nil, ErrTokenInvalid
	}
}

func (v *Verifier) keyFuncPrevious(token *jwt.Token) (interface{}, error) {
	if token == nil || token.Method == nil {
		return nil, ErrTokenInvalid
	}
	alg := token.Method.Alg()
	if alg != jwt.SigningMethodHS256.Alg() {
		return nil, ErrTokenInvalid
	}
	if _, ok := v.allowedAlgs[alg]; !ok {
		return nil, ErrTokenInvalid
	}
	return v.previousHMACSecret, nil
}

func (v *Verifier) allowedMethods() []string {
	methods := make([]string, 0, len(v.allowedAlgs))
	for alg := range v.allowedAlgs {
		methods = append(methods, alg)
	}
	return methods
}

func parseRSAPublicKey(raw string) (*rsa.PublicKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, fmt.Errorf("invalid rsa public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err == nil {
		if key, ok := pub.(*rsa.PublicKey); ok {
			return key, nil
		}
	}
	key, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid rsa public key")
	}
	return key, nil
}

func parseCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// TokenDenylist mirrors editor-service's interface so a single
// Redis-backed denylist can be shared via a common contract,
// without forcing collab-service to depend on Redis directly.
type TokenDenylist interface {
	IsTokenDenied(ctx context.Context, token string) (bool, error)
}
