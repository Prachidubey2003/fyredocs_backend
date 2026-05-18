package queue

import (
	"encoding/json"
	"testing"
)

func TestSubjectForNotify(t *testing.T) {
	cases := []struct{ ch, want string }{
		{"email", "notify.send.email"},
		{"webhook", "notify.send.webhook"},
		{"push", "notify.send.push"},
		{"slack", "notify.send.slack"},
	}
	for _, tc := range cases {
		if got := SubjectForNotify(tc.ch); got != tc.want {
			t.Errorf("SubjectForNotify(%q) = %q, want %q", tc.ch, got, tc.want)
		}
	}
}

func TestNotifyEvent_JSONRoundtrip(t *testing.T) {
	in := NotifyEvent{
		Channel:        "webhook",
		Target:         "https://example.com/hook",
		UserID:         "11111111-1111-1111-1111-111111111111",
		Payload:        json.RawMessage(`{"event":"doc.signed"}`),
		IdempotencyKey: "doc-signed-2026-05-16-abc",
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out NotifyEvent
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Channel != in.Channel || out.Target != in.Target ||
		out.UserID != in.UserID || out.IdempotencyKey != in.IdempotencyKey {
		t.Errorf("round-trip mismatch:\ngot:  %+v\nwant: %+v", out, in)
	}
	if string(out.Payload) != string(in.Payload) {
		t.Errorf("payload round-trip: got %q, want %q", out.Payload, in.Payload)
	}
}

func TestNotifyEvent_OmitsEmptyOptionalFields(t *testing.T) {
	in := NotifyEvent{
		Channel: "email",
		Target:  "user@example.com",
	}
	data, _ := json.Marshal(in)
	// Optional fields with omitempty must NOT appear in the wire
	// JSON when empty — subscribers parse the simpler shape.
	for _, key := range []string{"userId", "payload", "idempotencyKey"} {
		needle := `"` + key + `"`
		if containsSubstring(string(data), needle) {
			t.Errorf("optional field %q leaked into wire JSON: %s", key, data)
		}
	}
}

func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
