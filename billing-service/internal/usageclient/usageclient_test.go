package usageclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNew_RejectsEmptyOrMalformedBaseURL(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("New({}) = nil err, want non-nil for empty BaseURL")
	}
	if _, err := New(Options{BaseURL: "not a url"}); err == nil {
		t.Error("New(\"not a url\") = nil err, want non-nil")
	}
	if _, err := New(Options{BaseURL: "missing-scheme.example"}); err == nil {
		t.Error("New(missing scheme) = nil err, want non-nil")
	}
}

func TestGetRollup_UnwrapsStandardEnvelope(t *testing.T) {
	const userID = "11111111-1111-1111-1111-111111111111"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Confirm the client is calling the right path with the
		// right query parameter.
		if !strings.HasSuffix(r.URL.Path, "/internal/v1/usage/"+userID) {
			t.Errorf("URL path = %q, want suffix /internal/v1/usage/<userID>", r.URL.Path)
		}
		if r.URL.Query().Get("period") != "2026-05" {
			t.Errorf("period query = %q, want 2026-05", r.URL.Query().Get("period"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"message": "ok",
			"data": {
				"userId": "` + userID + `",
				"period": "2026-05",
				"items": [
					{"eventType": "op.merge", "unit": "ops", "totalQuantity": 7, "eventCount": 7},
					{"eventType": "op.ocr",   "unit": "pages", "totalQuantity": 50, "eventCount": 1}
				]
			}
		}`))
	}))
	defer srv.Close()

	c, err := New(Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.GetRollup(context.Background(), userID, "2026-05")
	if err != nil {
		t.Fatalf("GetRollup: %v", err)
	}
	if got.UserID != userID || got.Period != "2026-05" {
		t.Errorf("UserID/Period = %q/%q", got.UserID, got.Period)
	}
	if len(got.Items) != 2 {
		t.Fatalf("Items count = %d, want 2", len(got.Items))
	}
	if got.Items[0].EventType != "op.merge" || got.Items[0].TotalQuantity != 7 {
		t.Errorf("Items[0] = %+v", got.Items[0])
	}
	if got.Items[1].Unit != "pages" || got.Items[1].EventCount != 1 {
		t.Errorf("Items[1] = %+v", got.Items[1])
	}
}

func TestGetRollup_PropagatesNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`server unavailable`))
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL})
	if _, err := c.GetRollup(context.Background(), "abc", ""); err == nil {
		t.Error("GetRollup: nil err on 500, want non-nil")
	}
}

func TestGetRollup_FailsOnEnvelopeWithoutSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success": false, "message": "bad period", "data": null}`))
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL})
	if _, err := c.GetRollup(context.Background(), "abc", ""); err == nil {
		t.Error("GetRollup: nil err on success=false envelope, want non-nil")
	}
}

func TestGetRollup_OmitsPeriodQueryWhenEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, present := r.URL.Query()["period"]; present {
			t.Errorf("period query was present when caller passed \"\"; URL = %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"success": true, "message": "ok", "data": {"userId": "x", "period": "", "items": []}}`))
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL})
	if _, err := c.GetRollup(context.Background(), "abc", ""); err != nil {
		t.Errorf("GetRollup: %v", err)
	}
}

func TestTrimRight(t *testing.T) {
	cases := []struct {
		in, cut, want string
	}{
		{"http://x/", "/", "http://x"},
		{"http://x///", "/", "http://x"},
		{"abc", "", "abc"},
		{"", "/", ""},
		{"abc/", "x", "abc/"},
	}
	for _, tc := range cases {
		if got := trimRight(tc.in, tc.cut); got != tc.want {
			t.Errorf("trimRight(%q, %q) = %q, want %q", tc.in, tc.cut, got, tc.want)
		}
	}
}
