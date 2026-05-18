// Package authclient is a thin HTTP client for the small subset
// of auth-service endpoints editor-service needs.
//
// Today: profile lookup (displayName) so comment lists can render
// "posted by X" without forcing the frontend to look up every
// author UUID separately.
//
// All calls are best-effort: a transport failure or 404 means
// "no display name available" — the caller falls back to the
// raw user-id string. This keeps the comments path working when
// auth-service is degraded.
package authclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client is a configured auth-service caller. Construct one at
// startup; share across handlers. Safe for concurrent use.
type Client struct {
	baseURL string
	http    *http.Client
	timeout time.Duration
}

// Options configures a Client. BaseURL is required (typically
// `http://auth-service:8082`). Timeout caps any single call;
// defaults to 2s.
type Options struct {
	BaseURL string
	Client  *http.Client
	Timeout time.Duration
}

// ErrNotFound is returned by Profile when auth-service responds
// with 404. Callers typically handle this the same way as a
// transport error (fall back to raw id), but distinguishing the
// two helps when logging.
var ErrNotFound = errors.New("authclient: user not found")

// Profile is the public-shaped subset of a user record. Fields
// match the keys auth-service returns under `data` in its
// standard response envelope.
type Profile struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
}

// New constructs a Client. Returns nil if BaseURL is empty so the
// caller can wire "auth lookups disabled" without a nil-check
// dance at the call site (the helper methods are nil-safe).
func New(opts Options) *Client {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return nil
	}
	c := opts.Client
	if c == nil {
		c = &http.Client{Timeout: 0} // per-request context handles timeout
	}
	to := opts.Timeout
	if to == 0 {
		to = 2 * time.Second
	}
	return &Client{baseURL: base, http: c, timeout: to}
}

// Profile fetches `/internal/users/{id}/profile`. Returns
// ErrNotFound on 404, a wrapped transport error otherwise.
//
// A nil receiver returns ErrNotFound — this is the "disabled
// lookups" path used when auth-service URL isn't configured.
// Callers should treat both that and the actual 404 case the
// same way (no display name available).
func (c *Client) Profile(ctx context.Context, userID string) (Profile, error) {
	if c == nil {
		return Profile{}, ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	url := c.baseURL + "/internal/users/" + userID + "/profile"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Profile{}, fmt.Errorf("authclient: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Profile{}, fmt.Errorf("authclient: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return Profile{}, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		return Profile{}, fmt.Errorf("authclient: non-2xx %d", resp.StatusCode)
	}

	// auth-service wraps responses in the platform envelope:
	// `{"success":true,"message":"...","data":{...}}`. We only
	// care about data; decoding everything keeps the call robust
	// to envelope-key additions.
	var envelope struct {
		Success bool    `json:"success"`
		Data    Profile `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return Profile{}, fmt.Errorf("authclient: decode: %w", err)
	}
	if !envelope.Success {
		return Profile{}, fmt.Errorf("authclient: envelope success=false")
	}
	return envelope.Data, nil
}

// LookupDisplayName is a small convenience: returns the display
// name for a userID, or the empty string on any failure. Use
// this when "fall back to raw id" is the only behaviour the
// caller wants.
func (c *Client) LookupDisplayName(ctx context.Context, userID string) string {
	p, err := c.Profile(ctx, userID)
	if err != nil {
		return ""
	}
	return p.DisplayName
}

// LookupDisplayNames batches profile lookups for a set of user
// ids, returning a map keyed by id. Unknown / errored ids are
// simply absent from the map — callers iterate the input slice
// and fall back to "" when an id is missing.
//
// Lookups run concurrently with a small cap so a 100-comment
// thread doesn't trigger 100 simultaneous auth-service hits.
// Cap of 8 matches the auth-service connection pool sizing used
// elsewhere in the platform.
func (c *Client) LookupDisplayNames(ctx context.Context, userIDs []string) map[string]string {
	if c == nil || len(userIDs) == 0 {
		return nil
	}
	// Dedupe — a thread with 50 comments by 3 authors should
	// hit auth-service 3 times, not 50.
	seen := make(map[string]struct{}, len(userIDs))
	unique := userIDs[:0:0]
	for _, id := range userIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}

	results := make(map[string]string, len(unique))
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, 8)
	)
	for _, id := range unique {
		id := id
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if name := c.LookupDisplayName(ctx, id); name != "" {
				mu.Lock()
				results[id] = name
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return results
}
