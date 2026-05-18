// Package client is the CLI's HTTP wrapper around the Fyredocs
// API. It mirrors the TypeScript SDK's shape so the two stay
// behaviourally consistent — same envelope-unwrapping, same
// `Authorization: Bearer` header, same error mapping.
//
// Stateless: every Client method does one request. No caching,
// no retries (the CLI is interactive; a transient failure is
// best surfaced to the user immediately).
package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is the Fyredocs API client. Construct via New.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New returns a Client with a 30-second-timeout HTTP client.
// Trailing slashes on baseURL are normalised.
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// APIError is returned for non-2xx responses. The CLI prints the
// `Message` to stderr; `Status` + `Code` are checked
// programmatically (e.g., 401 → re-run login).
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s (%d)", e.Message, e.Status)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// envelope is the {success, message, data, error} wrapper every
// Fyredocs endpoint returns.
type envelope struct {
	Success bool            `json:"success"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *struct {
		Code    string `json:"code"`
		Details string `json:"details,omitempty"`
	} `json:"error,omitempty"`
}

// DoRaw runs one request and streams the response body to `dst`
// on 2xx. The Fyredocs JSON envelope is NOT unwrapped — use this
// for endpoints that return binary (`/download` returns
// `application/pdf`) or anything else where the response body is
// not the standard `{success, data, ...}` shape.
//
// On non-2xx the response body is read into memory and parsed as
// an envelope so the *APIError carries the server's message.
func (c *Client) DoRaw(method, path string, query url.Values, dst io.Writer) error {
	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return err
	}
	if query != nil {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequest(method, u.String(), nil)
	if err != nil {
		return err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		var env envelope
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &env)
		}
		apiErr := &APIError{Status: resp.StatusCode}
		switch {
		case env.Error != nil:
			apiErr.Code = env.Error.Code
			apiErr.Message = env.Error.Details
		case env.Message != "":
			apiErr.Code = fmt.Sprintf("HTTP_%d", resp.StatusCode)
			apiErr.Message = env.Message
		default:
			apiErr.Code = fmt.Sprintf("HTTP_%d", resp.StatusCode)
			apiErr.Message = strings.TrimSpace(string(raw))
		}
		return apiErr
	}
	if dst == nil {
		return nil
	}
	_, err = io.Copy(dst, resp.Body)
	return err
}

// Do runs one request. `query` may be nil. `body` may be nil or
// any JSON-marshalable value. On 2xx, decodes the envelope's
// `data` into `out` (pass nil for endpoints that return no body).
// On non-2xx, returns *APIError.
func (c *Client) Do(method, path string, query url.Values, body, out any) error {
	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return err
	}
	if query != nil {
		u.RawQuery = query.Encode()
	}
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, u.String(), bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var env envelope
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &env) // best-effort; non-JSON errors surfaced via APIError.Message below
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode}
		switch {
		case env.Error != nil:
			apiErr.Code = env.Error.Code
			apiErr.Message = env.Error.Details
		case env.Message != "":
			apiErr.Code = fmt.Sprintf("HTTP_%d", resp.StatusCode)
			apiErr.Message = env.Message
		default:
			apiErr.Code = fmt.Sprintf("HTTP_%d", resp.StatusCode)
			apiErr.Message = strings.TrimSpace(string(raw))
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if !env.Success {
		return errors.New("server returned success=false on a 2xx response")
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}
