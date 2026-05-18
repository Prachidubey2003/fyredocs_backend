package authverify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// APIKeyPrefix is the wire-format identifier that tells the gateway
// "this Authorization bearer is an API key, not a JWT." Both
// production (`fyr_live_…`) and sandbox (`fyr_test_…`) keys share it.
const APIKeyPrefix = "fyr_"

// LooksLikeAPIKey reports whether a bearer-token value should be
// routed through the API-key oracle rather than the JWT verifier.
//
// We use a static prefix check (not a full parse) so the request
// path stays cheap when the heuristic mis-categorises a JWT — JWTs
// don't start with `fyr_`, so a false positive would only happen if
// someone tried to authenticate with the literal string "fyr_…",
// in which case the oracle returns 401 anyway.
func LooksLikeAPIKey(token string) bool {
	return strings.HasPrefix(token, APIKeyPrefix)
}

// APIKeyClaims is the shape we accept back from auth-service's
// `/internal/verify-api-key` oracle. Mirrors the
// `VerifyAPIKeyResponse` server-side struct, but lives in this
// package (not imported) so the gateway doesn't take a build-time
// dependency on auth-service internals.
type APIKeyClaims struct {
	UserID      string   `json:"userId"`
	Environment string   `json:"environment"`
	Scopes      []string `json:"scopes,omitempty"`
}

// APIKeyVerifier validates a `fyr_…` token by RPC to auth-service.
//
// Why not embed the verification in the gateway: auth-service is
// the source of truth for the api_keys table. Letting the gateway
// read it directly would either require a shared DB connection
// (violating the per-service-DB rule in [CLAUDE.md] §3) or a shared
// model package (violating §1). The internal endpoint is the
// designed seam.
//
// The verifier is stateless and safe for concurrent use; the
// underlying http.Client handles connection pooling.
type APIKeyVerifier struct {
	// BaseURL is the auth-service base (e.g. http://auth-service:8080).
	// Must NOT end with a slash.
	BaseURL string
	// Client is the HTTP client used for the verify call. If nil we
	// fall back to a default 2-second-timeout client so a slow
	// auth-service can't stall every API-key request indefinitely.
	Client *http.Client
}

// ErrAPIKeyInvalid is returned for any 401 from the oracle. We
// collapse to one sentinel so the gateway's logging can't
// distinguish wrong-secret from revoked-key from env-mismatch —
// preserving the same opacity the oracle itself enforces.
var ErrAPIKeyInvalid = errors.New("authverify: API key invalid")

// ErrAPIKeyUnreachable is returned when the oracle can't be
// contacted (network failure, 5xx, malformed response). The gateway
// distinguishes this from `ErrAPIKeyInvalid` so it can surface a
// 503 to the client instead of a misleading 401 during an outage.
var ErrAPIKeyUnreachable = errors.New("authverify: API key oracle unreachable")

// Verify exchanges the wire-format token for the user's claims via
// the auth-service oracle. The caller has already established that
// the token looks like an API key via [LooksLikeAPIKey].
func (v *APIKeyVerifier) Verify(ctx context.Context, token string) (*APIKeyClaims, error) {
	if v == nil || v.BaseURL == "" {
		return nil, fmt.Errorf("%w: verifier not configured", ErrAPIKeyUnreachable)
	}

	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		// json.Marshal on a static map[string]string can't realistically
		// fail; wrap defensively so the gateway never panics on an
		// internal serialisation oddity.
		return nil, fmt.Errorf("%w: encode request: %v", ErrAPIKeyUnreachable, err)
	}

	url := v.BaseURL + "/internal/verify-api-key"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrAPIKeyUnreachable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := v.Client
	if client == nil {
		client = defaultAPIKeyHTTPClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAPIKeyUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrAPIKeyInvalid
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: oracle returned %d", ErrAPIKeyUnreachable, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		// 400 / unexpected — bucket as unreachable rather than invalid
		// so the gateway operator gets a 503 they can actually debug.
		return nil, fmt.Errorf("%w: oracle returned %d", ErrAPIKeyUnreachable, resp.StatusCode)
	}

	var env struct {
		Success bool         `json:"success"`
		Data    APIKeyClaims `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("%w: decode response: %v", ErrAPIKeyUnreachable, err)
	}
	if !env.Success || env.Data.UserID == "" {
		return nil, fmt.Errorf("%w: oracle returned success=false or empty userId", ErrAPIKeyUnreachable)
	}
	return &env.Data, nil
}

// defaultAPIKeyHTTPClient is the fallback when the caller doesn't
// supply one. 2-second timeout caps the worst-case stall — under
// load the gateway should fail fast rather than queue.
var defaultAPIKeyHTTPClient = &http.Client{Timeout: 2 * time.Second}

// ToAuthContext converts oracle claims into the context shape every
// downstream service expects. The Plan field is left blank — plan
// enrichment happens via the existing [PlanResolver] hook, same as
// for JWT-authenticated requests.
func (c *APIKeyClaims) ToAuthContext() AuthContext {
	return AuthContext{
		UserID: c.UserID,
		Scope:  c.Scopes,
	}
}
