package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newReq(method, path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	return c, rec
}

// The API-key handlers run behind the auth middleware in production,
// but the handler functions themselves also short-circuit on a
// missing auth context via loadUserFromAuth. We test that
// short-circuit here without standing up a DB or the full middleware
// stack — matches the pattern used by every other handler suite.

func TestIssueAPIKey_RejectsUnauthenticated(t *testing.T) {
	ae := &AuthEndpoints{}
	c, rec := newReq(http.MethodPost, "/auth/api-keys",
		[]byte(`{"name":"ci"}`))
	ae.IssueAPIKey(c)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestListAPIKeys_RejectsUnauthenticated(t *testing.T) {
	ae := &AuthEndpoints{}
	c, rec := newReq(http.MethodGet, "/auth/api-keys", nil)
	ae.ListAPIKeys(c)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestRevokeAPIKey_RejectsUnauthenticated(t *testing.T) {
	ae := &AuthEndpoints{}
	c, rec := newReq(http.MethodPost, "/auth/api-keys/X/revoke", nil)
	c.Params = gin.Params{{Key: "id", Value: uuid.NewString()}}
	ae.RevokeAPIKey(c)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// scopesToJSON is a pure function — exercised here so the JSON
// quoting logic isn't trusted blindly by the DB layer.
func TestScopesToJSON(t *testing.T) {
	cases := []struct {
		name   string
		in     []string
		want   string
		wantOK bool
	}{
		{"empty slice ⇒ nil", nil, "", true},
		{"single scope", []string{"documents:read"}, `["documents:read"]`, true},
		{"multiple scopes", []string{"a", "b", "c"}, `["a","b","c"]`, true},
		{"escapes quote", []string{`he"y`}, `["he\"y"]`, true},
		{"escapes backslash", []string{`a\b`}, `["a\\b"]`, true},
		{"escapes newline", []string{"a\nb"}, `["a\nb"]`, true},
		{"rejects control char", []string{"a\x01b"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scopesToJSON(tc.in)
			if tc.wantOK && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("err = nil, want non-nil")
			}
			if tc.wantOK {
				gotStr := string(got)
				if tc.want == "" && gotStr != "" {
					t.Errorf("got %q, want empty (nil JSON)", gotStr)
				}
				if tc.want != "" && gotStr != tc.want {
					t.Errorf("got %q, want %q", gotStr, tc.want)
				}
			}
		})
	}
}

// Reject-bad-input still requires the auth context, so we exercise
// only the rejection paths that fire BEFORE the auth check. (There
// aren't any — every handler short-circuits on auth first, by
// design.) This test documents that order-of-operations is part of
// the contract: a bogus body from an unauthenticated caller should
// surface as 401, not 400. Prevents accidental information leakage
// about request shape.
// decodeScopes is the pure path the verifier uses to read the JSONB
// column. We exercise it directly so the verifier's behavior on
// corrupt rows is locked in.
func TestDecodeScopes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"nil ⇒ nil", "", nil},
		{"empty array", "[]", nil},
		{"single scope", `["documents:read"]`, []string{"documents:read"}},
		{"multiple scopes", `["a","b","c"]`, []string{"a", "b", "c"}},
		{"corrupt JSON ⇒ nil (silently inherits user scopes)", "not json", nil},
		{"non-array JSON ⇒ nil", `{"a":1}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeScopes([]byte(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("len(got) = %d, want %d (got=%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// debouncedBumper's `bump` is a hot-path primitive — every API-key
// request hits it. The first hit on a key writes; subsequent hits
// inside the interval drop. We test the in-memory bookkeeping
// without observing the DB write (which spawns a goroutine).
func TestDebouncedBumper_DropsRepeatedHits(t *testing.T) {
	b := newDebouncedBumper(50 * time.Millisecond)
	id := uuid.New()

	// First hit: should record the timestamp.
	b.bump(id)
	b.mu.Lock()
	first, ok := b.lastSeen[id]
	b.mu.Unlock()
	if !ok || first.IsZero() {
		t.Fatal("first bump should record a timestamp")
	}

	// Immediate second hit: should NOT update the timestamp.
	time.Sleep(1 * time.Millisecond)
	b.bump(id)
	b.mu.Lock()
	second := b.lastSeen[id]
	b.mu.Unlock()
	if !second.Equal(first) {
		t.Errorf("second bump within interval should not advance timestamp; first=%v second=%v",
			first, second)
	}

	// After the interval, the next hit SHOULD update.
	time.Sleep(60 * time.Millisecond)
	b.bump(id)
	b.mu.Lock()
	third := b.lastSeen[id]
	b.mu.Unlock()
	if !third.After(first) {
		t.Errorf("bump past interval should advance timestamp; first=%v third=%v",
			first, third)
	}
}

func TestDebouncedBumper_IndependentPerKey(t *testing.T) {
	b := newDebouncedBumper(10 * time.Second)
	id1, id2 := uuid.New(), uuid.New()
	b.bump(id1)
	b.bump(id2)
	b.mu.Lock()
	_, ok1 := b.lastSeen[id1]
	_, ok2 := b.lastSeen[id2]
	b.mu.Unlock()
	if !ok1 || !ok2 {
		t.Errorf("each key should have its own debounce slot; got ok1=%v ok2=%v", ok1, ok2)
	}
}

func TestVerifyAPIKey_RejectsMalformedBody(t *testing.T) {
	// No `token` field → 400 INVALID_INPUT, NOT 401. Distinguishing
	// these matters: a gateway operator probing the endpoint with
	// `curl -X POST` should see "you sent garbage" not "auth failed."
	c, rec := newReq(http.MethodPost, "/internal/verify-api-key",
		[]byte(`{}`))
	VerifyAPIKey(c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing token field", rec.Code)
	}
}

func TestVerifyAPIKey_RejectsMalformedToken(t *testing.T) {
	// Well-formed JSON, malformed wire-format → 401 INVALID_API_KEY.
	// We don't leak which validation step failed.
	c, rec := newReq(http.MethodPost, "/internal/verify-api-key",
		[]byte(`{"token":"not-a-real-key"}`))
	VerifyAPIKey(c)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for malformed token", rec.Code)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error.Code != "INVALID_API_KEY" {
		t.Errorf("error.code = %q, want INVALID_API_KEY", env.Error.Code)
	}
}

func TestIssueAPIKey_AuthCheckRunsBeforeBodyValidation(t *testing.T) {
	ae := &AuthEndpoints{}
	c, rec := newReq(http.MethodPost, "/auth/api-keys",
		[]byte(`{}`)) // missing name — would be 400 if auth passed
	ae.IssueAPIKey(c)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (auth check must precede body check)",
			rec.Code)
	}
	// And the response envelope's code should be UNAUTHORIZED, not
	// INVALID_INPUT.
	var env struct {
		Success bool `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.EqualFold(env.Error.Code, "UNAUTHORIZED") {
		t.Errorf("error.code = %q, want UNAUTHORIZED", env.Error.Code)
	}
}
