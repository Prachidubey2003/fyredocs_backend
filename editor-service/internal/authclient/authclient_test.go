package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

func TestNew_NilOnEmptyBaseURL(t *testing.T) {
	if got := New(Options{BaseURL: ""}); got != nil {
		t.Errorf("New with empty BaseURL = %v, want nil", got)
	}
	if got := New(Options{BaseURL: "   "}); got != nil {
		t.Errorf("New with whitespace BaseURL = %v, want nil", got)
	}
}

func TestProfile_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/users/u-1/profile" {
			http.Error(w, "wrong path", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"userId":"u-1","displayName":"Alice"}}`))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL})
	p, err := c.Profile(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if p.UserID != "u-1" || p.DisplayName != "Alice" {
		t.Errorf("Profile = %+v, want UserID=u-1 DisplayName=Alice", p)
	}
}

func TestProfile_404ReturnsErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL})
	_, err := c.Profile(context.Background(), "u-x")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestProfile_NilClientReturnsNotFound(t *testing.T) {
	// Nil-receiver shape: callers wire `authclient.New(...)` whose
	// result may be nil. The methods on a nil pointer must not
	// panic; they must return ErrNotFound so the fallback path
	// kicks in.
	var c *Client
	_, err := c.Profile(context.Background(), "u-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("nil-client Profile = %v, want ErrNotFound", err)
	}
}

func TestProfile_NonSuccessEnvelopeIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"data":{}}`))
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL})
	if _, err := c.Profile(context.Background(), "u-1"); err == nil {
		t.Error("expected error when envelope.success=false")
	}
}

func TestLookupDisplayName_ReturnsEmptyOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL})
	if got := c.LookupDisplayName(context.Background(), "u-x"); got != "" {
		t.Errorf("LookupDisplayName on error = %q, want empty", got)
	}
}

func TestLookupDisplayNames_DedupesRequests(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// Echo the id back so the test can prove the map is
		// keyed correctly.
		id := r.URL.Path[len("/internal/users/") : len(r.URL.Path)-len("/profile")]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"userId":"` + id + `","displayName":"name-` + id + `"}}`))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL})
	ids := []string{"a", "b", "a", "c", "b", "a"}
	got := c.LookupDisplayNames(context.Background(), ids)
	if got["a"] != "name-a" || got["b"] != "name-b" || got["c"] != "name-c" {
		keys := []string{}
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Errorf("LookupDisplayNames = %v (keys %v), want a/b/c mapped", got, keys)
	}
	if hits != 3 {
		t.Errorf("auth-service was hit %d times, want 3 (dedupe failed)", hits)
	}
}

func TestProfile_TimeoutReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, Timeout: 50 * time.Millisecond})
	if _, err := c.Profile(context.Background(), "u-1"); err == nil {
		t.Error("expected timeout to surface as an error")
	}
}
