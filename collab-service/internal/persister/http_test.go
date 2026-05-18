package persister

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewHTTP_RejectsMissingBaseURL(t *testing.T) {
	if _, err := NewHTTP(HTTPOptions{}); err == nil {
		t.Error("expected error for empty BaseURL")
	}
	if _, err := NewHTTP(HTTPOptions{BaseURL: "   "}); err == nil {
		t.Error("expected error for whitespace BaseURL")
	}
}

func TestHTTP_Save_RoundTripsToServer(t *testing.T) {
	var (
		mu          sync.Mutex
		gotMethod   string
		gotPath     string
		gotBody     []byte
		gotContent  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContent = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p, err := NewHTTP(HTTPOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	frames := [][]byte{[]byte("frame-1"), []byte("frame-2")}
	p.Save("doc-abc", frames)

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/internal/v1/snapshots/doc-abc" {
		t.Errorf("path = %q, want /internal/v1/snapshots/doc-abc", gotPath)
	}
	if gotContent != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", gotContent)
	}
	want := Encode(frames)
	if !bytes.Equal(gotBody, want) {
		t.Errorf("body bytes mismatch: got %d, want %d", len(gotBody), len(want))
	}
}

func TestHTTP_Save_DropsEmptyInput(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	p, _ := NewHTTP(HTTPOptions{BaseURL: srv.URL})
	p.Save("doc-x", nil)
	p.Save("doc-x", [][]byte{})
	if called {
		t.Error("Save with empty frames should not call the server")
	}
}

func TestHTTP_Save_SwallowsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p, _ := NewHTTP(HTTPOptions{BaseURL: srv.URL})
	// Survives without panic; logged warning is the only observable effect.
	p.Save("doc-x", [][]byte{[]byte("x")})
}

func TestHTTP_Load_RoundTripsFromServer(t *testing.T) {
	want := [][]byte{[]byte("a"), []byte("bb"), []byte("ccc")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/doc-x") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(Encode(want))
	}))
	defer srv.Close()

	p, _ := NewHTTP(HTTPOptions{BaseURL: srv.URL})
	got := p.Load("doc-x")
	if len(got) != len(want) {
		t.Fatalf("Load returned %d frames, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("frame %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestHTTP_Load_NotFoundReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	p, _ := NewHTTP(HTTPOptions{BaseURL: srv.URL})
	if got := p.Load("doc-x"); got != nil {
		t.Errorf("Load on 404 = %v, want nil", got)
	}
}

func TestHTTP_Load_ServerErrorReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p, _ := NewHTTP(HTTPOptions{BaseURL: srv.URL})
	if got := p.Load("doc-x"); got != nil {
		t.Errorf("Load on 500 = %v, want nil (best-effort semantics)", got)
	}
}

func TestHTTP_Load_TimeoutReturnsNil(t *testing.T) {
	// Server takes longer than the configured timeout — Load should
	// give up and return nil rather than block the room run-loop.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	p, _ := NewHTTP(HTTPOptions{
		BaseURL:     srv.URL,
		LoadTimeout: 50 * time.Millisecond,
	})
	got := p.Load("doc-x")
	if got != nil {
		t.Errorf("Load on timeout = %v, want nil", got)
	}
}

func TestHTTP_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p, _ := NewHTTP(HTTPOptions{BaseURL: srv.URL + "/"})
	p.Save("doc-x", [][]byte{[]byte("y")})
	if gotPath != "/internal/v1/snapshots/doc-x" {
		t.Errorf("path = %q (trailing slash not trimmed?)", gotPath)
	}
}
