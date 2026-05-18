package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPush_PostsExpoMessageWithToken(t *testing.T) {
	const token = "ExponentPushToken[xxxx-yyyy]"
	received := struct {
		method      string
		path        string
		auth        string
		contentType string
		body        map[string]any
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.method = r.Method
		received.path = r.URL.Path
		received.auth = r.Header.Get("Authorization")
		received.contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &received.body)
		_, _ = w.Write([]byte(`{"data":{"status":"ok","id":"ticket-1"}}`))
	}))
	defer srv.Close()

	p := NewPush("expo_access_test")
	p.Endpoint = srv.URL
	err := p.Send(context.Background(), SendRequest{
		Target: token,
		Payload: json.RawMessage(
			`{"title":"Build done","body":"v2.1 shipped","data":{"docId":"doc_01HV"}}`,
		),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if received.method != http.MethodPost {
		t.Errorf("method = %q, want POST", received.method)
	}
	if received.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", received.contentType)
	}
	if received.auth != "Bearer expo_access_test" {
		t.Errorf("Authorization = %q, want Bearer expo_access_test", received.auth)
	}
	if received.body["to"] != token {
		t.Errorf("body.to = %v, want %q (channel must inject Target as `to`)", received.body["to"], token)
	}
	if received.body["title"] != "Build done" {
		t.Errorf("body.title = %v, want Build done", received.body["title"])
	}
	if data, ok := received.body["data"].(map[string]any); !ok || data["docId"] != "doc_01HV" {
		t.Errorf("body.data = %v, want forwarded verbatim", received.body["data"])
	}
}

func TestPush_OverridesCallerProvidedTo(t *testing.T) {
	// Defensive: even if the caller's payload sneaks in a
	// `to` field, the channel must overwrite it with
	// req.Target. Otherwise a misbehaving publisher could
	// silently address the wrong device.
	const realTarget = "ExponentPushToken[real-device]"
	received := struct {
		to string
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if v, ok := body["to"].(string); ok {
			received.to = v
		}
		_, _ = w.Write([]byte(`{"data":{"status":"ok","id":"t"}}`))
	}))
	defer srv.Close()

	p := NewPush("")
	p.Endpoint = srv.URL
	err := p.Send(context.Background(), SendRequest{
		Target:  realTarget,
		Payload: json.RawMessage(`{"to":"ExponentPushToken[ATTACKER]","title":"x","body":"y"}`),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if received.to != realTarget {
		t.Errorf("body.to = %q, want %q (channel must override caller-supplied to)", received.to, realTarget)
	}
}

func TestPush_OmitsAuthorizationWhenNoAccessToken(t *testing.T) {
	seenAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{"status":"ok","id":"t"}}`))
	}))
	defer srv.Close()

	p := NewPush("")
	p.Endpoint = srv.URL
	err := p.Send(context.Background(), SendRequest{
		Target:  "ExponentPushToken[x]",
		Payload: json.RawMessage(`{"title":"x"}`),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if seenAuth != "" {
		t.Errorf("Authorization = %q, want empty (no access token configured)", seenAuth)
	}
}

func TestPush_HandlesArrayResponse(t *testing.T) {
	// Expo's docs say single-message requests get a single
	// ticket object, but batch endpoints return an array. The
	// channel must handle both — Send doesn't batch in v0 but
	// some proxies wrap singletons in arrays.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"status":"ok","id":"t1"}]}`))
	}))
	defer srv.Close()

	p := NewPush("")
	p.Endpoint = srv.URL
	if err := p.Send(context.Background(), SendRequest{
		Target:  "ExponentPushToken[x]",
		Payload: json.RawMessage(`{"title":"x","body":"y"}`),
	}); err != nil {
		t.Errorf("Send (array response): %v", err)
	}
}

func TestPush_TicketStatusErrorReturnsError(t *testing.T) {
	// Expo returns 200 with per-ticket status="error" on
	// DeviceNotRegistered etc. The channel must treat this as
	// a delivery failure (not success) so the audit row lands
	// `failed` with the specific error code in LastError.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`{"data":{"status":"error","message":"\"ExponentPushToken[x]\" is not a registered push notification recipient","details":{"error":"DeviceNotRegistered"}}}`,
		))
	}))
	defer srv.Close()

	p := NewPush("")
	p.Endpoint = srv.URL
	err := p.Send(context.Background(), SendRequest{
		Target:  "ExponentPushToken[x]",
		Payload: json.RawMessage(`{"title":"x"}`),
	})
	if err == nil {
		t.Fatal("expected error for per-ticket status=error")
	}
	if !strings.Contains(err.Error(), "DeviceNotRegistered") &&
		!strings.Contains(err.Error(), "not a registered") {
		t.Errorf("err = %v, want hint about the registration failure", err)
	}
}

func TestPush_TopLevelExpoErrorReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			`{"errors":[{"code":"VALIDATION_ERROR","message":"\"to\" is required"}]}`,
		))
	}))
	defer srv.Close()

	p := NewPush("")
	p.Endpoint = srv.URL
	err := p.Send(context.Background(), SendRequest{
		Target:  "ExponentPushToken[x]",
		Payload: json.RawMessage(`{"title":"x"}`),
	})
	if err == nil {
		t.Fatal("expected error for top-level Expo error")
	}
	if !strings.Contains(err.Error(), "VALIDATION_ERROR") {
		t.Errorf("err = %v, want hint about VALIDATION_ERROR", err)
	}
}

func TestPush_Non2xxReturnsErrorWithBodyExcerpt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid access token"))
	}))
	defer srv.Close()

	p := NewPush("bad-token")
	p.Endpoint = srv.URL
	err := p.Send(context.Background(), SendRequest{
		Target:  "ExponentPushToken[x]",
		Payload: json.RawMessage(`{"title":"x"}`),
	})
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid access token") {
		t.Errorf("err = %v, want 401 + body excerpt", err)
	}
}

func TestPush_EmptyTargetRejected(t *testing.T) {
	p := NewPush("")
	err := p.Send(context.Background(), SendRequest{Payload: json.RawMessage(`{"title":"x"}`)})
	if err == nil || !strings.Contains(err.Error(), "empty target") {
		t.Errorf("err = %v, want empty-target error", err)
	}
}

func TestPush_EmptyPayloadRejected(t *testing.T) {
	p := NewPush("")
	err := p.Send(context.Background(), SendRequest{Target: "ExponentPushToken[x]"})
	if err == nil || !strings.Contains(err.Error(), "empty payload") {
		t.Errorf("err = %v, want empty-payload error", err)
	}
}

func TestPush_InvalidJSONPayloadRejectedLocally(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer srv.Close()

	p := NewPush("")
	p.Endpoint = srv.URL
	err := p.Send(context.Background(), SendRequest{
		Target:  "ExponentPushToken[x]",
		Payload: json.RawMessage(`not-json`),
	})
	if err == nil || !strings.Contains(err.Error(), "not valid Expo message JSON") {
		t.Errorf("err = %v, want local JSON rejection", err)
	}
	if called {
		t.Error("server should never have been called for invalid JSON")
	}
}

func TestPush_DropsUnknownFieldsToPreventToInjection(t *testing.T) {
	// The struct unmarshal drops fields that aren't on
	// expoMessage. This is what stops a caller from sneaking
	// `to` (covered above) AND from forwarding undocumented
	// fields Expo might reject. Verify a known-unknown field
	// is not forwarded.
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"data":{"status":"ok","id":"t"}}`))
	}))
	defer srv.Close()

	p := NewPush("")
	p.Endpoint = srv.URL
	err := p.Send(context.Background(), SendRequest{
		Target:  "ExponentPushToken[x]",
		Payload: json.RawMessage(`{"title":"x","unknownProductFlag":true}`),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, present := body["unknownProductFlag"]; present {
		t.Errorf("unknown field leaked to Expo: %v", body)
	}
}

func TestPush_ContextCancellationPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := NewPush("")
	p.Endpoint = srv.URL
	err := p.Send(ctx, SendRequest{
		Target:  "ExponentPushToken[x]",
		Payload: json.RawMessage(`{"title":"x"}`),
	})
	if err == nil {
		t.Error("expected error when ctx is cancelled")
	}
}

func TestPush_NewHasReasonableTimeout(t *testing.T) {
	p := NewPush("")
	if p.HTTP == nil {
		t.Fatal("NewPush must populate HTTP")
	}
	if p.HTTP.Timeout < 1*time.Second {
		t.Errorf("HTTP timeout = %v, want a sane non-zero default", p.HTTP.Timeout)
	}
}
