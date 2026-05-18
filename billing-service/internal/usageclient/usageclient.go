// Package usageclient is the HTTP client billing-service uses to
// fetch a user's usage rollup from analytics-service.
//
// We talk to the `/internal/v1/usage/:userID?period=` endpoint
// (analytics-service handlers/usage.go). The endpoint lives
// outside auth middleware on the assumption that the service
// mesh is private — billing-service is expected to be a
// peer-trusted caller. If the mesh boundary needs hardening
// later, add an HMAC header here and a matching verifier on the
// analytics side; both sides share the secret via Vault.
package usageclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// RollupRow mirrors analytics-service's handlers.UsageRollupRow.
// We re-declare it here (rather than importing across the
// service boundary) per the CLAUDE.md §3 rule "no cross-service
// struct sharing"; the JSON tags are the contract.
type RollupRow struct {
	EventType     string `json:"eventType"`
	Unit          string `json:"unit"`
	TotalQuantity int64  `json:"totalQuantity"`
	EventCount    int64  `json:"eventCount"`
}

// RollupResponse mirrors analytics-service's
// handlers.UsageRollupResponse, unwrapped from the standard
// {success, message, data} envelope by [Client.GetRollup].
type RollupResponse struct {
	UserID string      `json:"userId"`
	Period string      `json:"period"`
	Items  []RollupRow `json:"items"`
}

// Client is the analytics-service usage-rollup reader.
//
// Construct via New, not as a literal — New encodes the URL
// normalisation we want (trailing-slash trim, scheme defaulting)
// and writes a stable timeout.
type Client struct {
	baseURL string
	http    *http.Client
}

// Options tune the Client. Zero values mean "use the default".
type Options struct {
	// BaseURL is the analytics-service root, e.g.
	// "http://analytics-service:8087". Required.
	BaseURL string
	// Timeout is the per-request cap; default 3s. Billing UI is
	// not the hot path but a hung request shouldn't pin a goroutine.
	Timeout time.Duration
	// HTTPClient is injected for tests; default http.DefaultClient
	// with the timeout applied.
	HTTPClient *http.Client
}

// New builds a Client. Returns error on missing/invalid BaseURL.
func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("usageclient: BaseURL is required")
	}
	u, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("usageclient: bad BaseURL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("usageclient: BaseURL must include scheme + host")
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 3 * time.Second
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL: trimRight(opts.BaseURL, "/"),
		http:    hc,
	}, nil
}

// GetRollup fetches the rollup for `userID` in `period` (YYYY-MM).
// `period` may be empty — analytics-service defaults to the
// current UTC month in that case.
//
// Returns:
//   - empty Items + nil error: user has no metered events yet
//     (a fresh signup looks like this).
//   - non-nil error: transport failure, non-2xx response, or
//     malformed envelope. Callers should fall back to "no usage
//     visible" UI rather than failing the /v1/billing/me handler
//     since usage data is non-critical for billing display.
func (c *Client) GetRollup(ctx context.Context, userID, period string) (*RollupResponse, error) {
	u := c.baseURL + "/internal/v1/usage/" + url.PathEscape(userID)
	if period != "" {
		u += "?period=" + url.QueryEscape(period)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("usageclient: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("usageclient: do request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("usageclient: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("usageclient: analytics returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	// Standard {success, message, data} envelope from
	// fyredocs/shared/response. Unwrap to the data field.
	var env struct {
		Success bool            `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("usageclient: decode envelope: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("usageclient: analytics envelope not OK: %s", env.Message)
	}
	var out RollupResponse
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return nil, fmt.Errorf("usageclient: decode data: %w", err)
	}
	return &out, nil
}

// trimRight is a tiny stdlib-free helper that mirrors
// strings.TrimRight(s, cutset). Kept local because pulling in
// strings for one call adds an import for cosmetic reasons.
func trimRight(s, cutset string) string {
	for len(s) > 0 {
		c := s[len(s)-1]
		hit := false
		for i := 0; i < len(cutset); i++ {
			if cutset[i] == c {
				hit = true
				break
			}
		}
		if !hit {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
