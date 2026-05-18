package client

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDo_UnwrapsEnvelopeOn2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fyr_test_key" {
			t.Errorf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"name":"hello"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "fyr_test_key")
	var out struct {
		Name string `json:"name"`
	}
	if err := c.Do("GET", "/some/path", nil, nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out.Name != "hello" {
		t.Errorf("Name = %q, want hello", out.Name)
	}
}

func TestDo_AppendsQueryString(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		_, _ = w.Write([]byte(`{"success":true,"data":null}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	q := url.Values{}
	q.Set("period", "2026-05")
	q.Set("revoked", "true")
	if err := c.Do("GET", "/v1/usage/me", q, nil, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !strings.Contains(gotURL, "period=2026-05") || !strings.Contains(gotURL, "revoked=true") {
		t.Errorf("query string missing expected params: %q", gotURL)
	}
}

func TestDo_PostsJSONBody(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readAll(r.Body)
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte(`{"success":true,"data":null}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	body := struct {
		Name string `json:"name"`
	}{"CI"}
	if err := c.Do("POST", "/auth/api-keys", nil, body, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !strings.Contains(string(gotBody), `"name":"CI"`) {
		t.Errorf("body = %q, want {\"name\":\"CI\"}", gotBody)
	}
}

func TestDo_Returns204AsNilError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	if err := c.Do("POST", "/auth/api-keys/k1/revoke", nil, nil, nil); err != nil {
		t.Errorf("Do on 204: %v", err)
	}
}

func TestDo_MapsServerErrorEnvelopeToAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"error":{"code":"INVALID_PLAN","details":"Unknown plan code"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	err := c.Do("POST", "/api/billing/v1/billing/me/subscribe", nil, struct{}{}, nil)
	if err == nil {
		t.Fatal("expected error on 400, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error not an *APIError: %T", err)
	}
	if apiErr.Status != 400 || apiErr.Code != "INVALID_PLAN" || apiErr.Message != "Unknown plan code" {
		t.Errorf("APIError = %+v, want Status=400 Code=INVALID_PLAN Message=Unknown plan code", apiErr)
	}
}

func TestDo_SynthesisesHTTPCodeWhenEnvelopeAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`upstream went away`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	err := c.Do("GET", "/x", nil, nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error not an *APIError: %v", err)
	}
	if apiErr.Code != "HTTP_500" {
		t.Errorf("Code = %q, want HTTP_500", apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "upstream went away") {
		t.Errorf("Message should include body excerpt; got %q", apiErr.Message)
	}
}

func TestDo_NoAuthHeaderWhenAPIKeyEmpty(t *testing.T) {
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"success":true,"data":null}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	_ = c.Do("GET", "/x", nil, nil, nil)
	if gotAuth != "" {
		t.Errorf("Authorization should be empty when APIKey is empty; got %q", gotAuth)
	}
}

func TestAPIError_ErrorMessage(t *testing.T) {
	e := &APIError{Status: 401, Code: "UNAUTHORIZED", Message: "Please log in"}
	if !strings.Contains(e.Error(), "Please log in") {
		t.Errorf("Error() = %q", e.Error())
	}
	if !strings.Contains(e.Error(), "401") {
		t.Errorf("Error() should include status; got %q", e.Error())
	}
}

// readAll buffers a small request body in tests.
func readAll(r interface{ Read(p []byte) (int, error) }) []byte {
	var out []byte
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	return out
}
