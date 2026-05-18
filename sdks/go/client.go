// Package fyredocs is the official Go SDK for the Fyredocs API.
//
//	import "github.com/fyredocs/fyredocs-go"
//
//	client := fyredocs.New(fyredocs.Options{
//	    APIKey: os.Getenv("FYREDOCS_KEY"),
//	})
//
//	keys, err := client.APIKeys.List(ctx, nil)
//	me, err := client.Billing.Me(ctx)
//	rev, err := client.Documents.Edit(ctx, "doc_01HV…", fyredocs.EditRequest{
//	    Ops: []fyredocs.EditorOp{
//	        {Type: fyredocs.OpPageRotate, Page: 1, Rotation: 90},
//	    },
//	})
//
// Surface mirrors @fyredocs/sdk (TypeScript). Same envelope
// unwrapping, same Authorization header, same error mapping.
//
// The SDK is intentionally hand-written rather than generated from
// the OpenAPI spec — the spec is the source of truth, but the
// hand-rolled types let us pick stable Go names without the noise
// a generator introduces. Drift is caught during dogfooding in the
// CLI (which already consumes the same wire format) and via
// `go test ./...` against httptest fixtures.
package fyredocs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.fyredocs.com"
	defaultTimeout = 30 * time.Second
)

// Options configures a Client. Zero-value defaults apply for every
// optional field, so a minimal call is `New(Options{APIKey: key})`.
type Options struct {
	// APIKey is an `fyr_live_…` / `fyr_test_…` token. For
	// server-to-server use. Leave empty when running against a
	// cookie-auth deployment (browser-side use is not the Go SDK's
	// primary target, but we don't lock it out either).
	APIKey string

	// BaseURL overrides the default production origin
	// (https://api.fyredocs.com). Trailing slashes are stripped.
	BaseURL string

	// HTTPClient overrides the default *http.Client. Useful for
	// custom transports, instrumentation, or test injection. When
	// nil the SDK builds an *http.Client with Timeout = Timeout.
	HTTPClient *http.Client

	// Timeout is the per-request timeout (when HTTPClient is nil).
	// Zero means use the SDK default (30s). Set to a negative value
	// to disable — useful for long-poll endpoints, but the v0 API
	// doesn't have any.
	Timeout time.Duration

	// UserAgent overrides the default `fyredocs-go/<version>`. We
	// don't ship a baked-in version yet; callers building tools on
	// top should set this so server-side analytics can distinguish
	// integrations.
	UserAgent string
}

// Client is the entrypoint. Hold one per credential — instances are
// cheap and stateless beyond the stored options.
type Client struct {
	baseURL   string
	apiKey    string
	http      *http.Client
	userAgent string

	// API namespaces. Each is a pointer so callers can swap them in
	// tests if they ever need a stub — but the standard path is to
	// inject Options.HTTPClient instead.
	APIKeys   *APIKeysAPI
	Billing   *BillingAPI
	Usage     *UsageAPI
	Documents *DocumentsAPI
}

// New constructs a Client with the supplied options.
func New(opts Options) *Client {
	base := opts.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	base = strings.TrimRight(base, "/")
	hc := opts.HTTPClient
	if hc == nil {
		t := opts.Timeout
		if t == 0 {
			t = defaultTimeout
		}
		if t < 0 {
			t = 0
		}
		hc = &http.Client{Timeout: t}
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "fyredocs-go"
	}
	c := &Client{
		baseURL:   base,
		apiKey:    opts.APIKey,
		http:      hc,
		userAgent: ua,
	}
	c.APIKeys = &APIKeysAPI{c: c}
	c.Billing = &BillingAPI{c: c}
	c.Usage = &UsageAPI{c: c}
	c.Documents = &DocumentsAPI{c: c}
	return c
}

// envelope is the {success, message, data, error} wrapper every
// Fyredocs endpoint returns. Kept unexported — callers see the
// inner data type via Request's `out` parameter.
type envelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *struct {
		Code    string `json:"code"`
		Details string `json:"details,omitempty"`
	} `json:"error,omitempty"`
}

// RequestOptions tunes a single HTTP call. All fields optional.
type RequestOptions struct {
	Method string            // GET, POST, etc.; defaults to GET
	Query  url.Values        // appended to the path's query string
	Body   any               // JSON-marshalled into the request body
	Out    any               // JSON-decoded into this pointer on 2xx
	Header map[string]string // extra request headers (e.g., Idempotency-Key)
}

// Request runs one HTTP call against the API. Public so the
// namespace types can call it without an indirection layer — but
// the documented surface is the per-namespace methods. Reach for
// Request directly only when you need an endpoint the SDK doesn't
// yet wrap.
//
// On 2xx with `Out` set, decodes the envelope's `data` into `out`.
// On non-2xx, returns *Error.
func (c *Client) Request(ctx context.Context, path string, opts RequestOptions) error {
	method := opts.Method
	if method == "" {
		method = http.MethodGet
	}
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}
	if len(opts.Query) > 0 {
		u.RawQuery = opts.Query.Encode()
	}

	var bodyReader io.Reader
	if opts.Body != nil {
		raw, err := json.Marshal(opts.Body)
		if err != nil {
			return fmt.Errorf("fyredocs: marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if opts.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range opts.Header {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Status: 0, Code: "NETWORK", Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Error{Status: resp.StatusCode, Code: "READ_FAILED", Message: err.Error()}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp.StatusCode, raw)
	}

	if opts.Out == nil {
		return nil
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// Non-enveloped 2xx — fall through to surface the body
		// directly. Rare; usually means an upstream edge cache
		// rewrote the response.
		return json.Unmarshal(raw, opts.Out)
	}
	if !env.Success {
		return errors.New("fyredocs: server returned success=false on a 2xx response")
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, opts.Out)
}

// RequestStream runs one HTTP call and copies the response body to
// `dst` on 2xx without unwrapping the envelope. Use this for
// binary endpoints (e.g., `/download` returns application/pdf).
// Non-2xx responses are read into memory and parsed as an envelope
// so the *Error carries the server's message.
func (c *Client) RequestStream(ctx context.Context, path string, opts RequestOptions, dst io.Writer) error {
	method := opts.Method
	if method == "" {
		method = http.MethodGet
	}
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}
	if len(opts.Query) > 0 {
		u.RawQuery = opts.Query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.userAgent)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range opts.Header {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Status: 0, Code: "NETWORK", Message: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return decodeError(resp.StatusCode, raw)
	}
	if dst == nil {
		return nil
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

func decodeError(status int, raw []byte) error {
	out := &Error{Status: status, Code: fmt.Sprintf("HTTP_%d", status)}
	if len(raw) == 0 {
		out.Message = http.StatusText(status)
		return out
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		out.Message = strings.TrimSpace(string(raw))
		return out
	}
	switch {
	case env.Error != nil:
		out.Code = env.Error.Code
		out.Message = env.Error.Details
	case env.Message != "":
		out.Message = env.Message
	default:
		out.Message = strings.TrimSpace(string(raw))
	}
	return out
}
