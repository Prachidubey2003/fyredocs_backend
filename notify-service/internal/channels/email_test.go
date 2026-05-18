package channels

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEmail_SendLogOnlyModeReturnsNil(t *testing.T) {
	// SMTPHost empty → dev fallback. Send returns nil and the
	// dispatcher will persist Delivery as `delivered`. This is the
	// fast smoke-test path in CI; no SMTP creds needed.
	e := &Email{}
	payload, _ := json.Marshal(EmailPayload{Subject: "hi", Text: "hello"})
	err := e.Send(context.Background(), SendRequest{
		Target:  "user@example.com",
		Payload: payload,
	})
	if err != nil {
		t.Errorf("log-only mode should succeed; got %v", err)
	}
}

func TestEmail_SendRejectsEmptyTarget(t *testing.T) {
	e := &Email{}
	if err := e.Send(context.Background(), SendRequest{}); err == nil {
		t.Error("expected error for empty target")
	}
}

func TestEmail_SendRequiresSubject(t *testing.T) {
	e := &Email{}
	payload, _ := json.Marshal(EmailPayload{Text: "no subject"})
	err := e.Send(context.Background(), SendRequest{
		Target:  "user@example.com",
		Payload: payload,
	})
	if err == nil {
		t.Error("expected error when subject is empty")
	}
}

func TestEmail_SendRequiresTextOrHTML(t *testing.T) {
	e := &Email{}
	payload, _ := json.Marshal(EmailPayload{Subject: "hi"})
	err := e.Send(context.Background(), SendRequest{
		Target:  "user@example.com",
		Payload: payload,
	})
	if err == nil {
		t.Error("expected error when both text and html are empty")
	}
}

func TestEmail_SendRejectsMalformedPayload(t *testing.T) {
	e := &Email{}
	err := e.Send(context.Background(), SendRequest{
		Target:  "user@example.com",
		Payload: []byte(`{not json`),
	})
	if err == nil {
		t.Error("expected error for malformed payload")
	}
}
