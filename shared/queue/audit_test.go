package queue

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSubjectForAudit(t *testing.T) {
	cases := []struct{ action, want string }{
		{"auth.login", "audit.events.auth.login"},
		{"doc.edit", "audit.events.doc.edit"},
		{"plan.changed", "audit.events.plan.changed"},
		{"apikey.revoke", "audit.events.apikey.revoke"},
	}
	for _, tc := range cases {
		if got := SubjectForAudit(tc.action); got != tc.want {
			t.Errorf("SubjectForAudit(%q) = %q, want %q", tc.action, got, tc.want)
		}
	}
}

func TestAuditEvent_JSONRoundtrip(t *testing.T) {
	in := AuditEvent{
		Actor:      "u1",
		Action:     "doc.edit",
		Resource:   "doc-abc",
		Metadata:   json.RawMessage(`{"opCount":3}`),
		OccurredAt: time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out AuditEvent
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Actor != in.Actor || out.Action != in.Action || out.Resource != in.Resource {
		t.Errorf("round-trip mismatch:\ngot:  %+v\nwant: %+v", out, in)
	}
	if string(out.Metadata) != string(in.Metadata) {
		t.Errorf("metadata round-trip: got %q, want %q", out.Metadata, in.Metadata)
	}
	if !out.OccurredAt.Equal(in.OccurredAt) {
		t.Errorf("OccurredAt: got %v, want %v", out.OccurredAt, in.OccurredAt)
	}
}

func TestAuditEvent_OmitsEmptyOptionals(t *testing.T) {
	in := AuditEvent{
		Actor:  "u1",
		Action: "auth.login",
	}
	data, _ := json.Marshal(in)
	// Optional fields with omitempty must NOT appear in the wire
	// JSON when empty — auditors parse the simpler shape.
	for _, key := range []string{"resource", "metadata"} {
		needle := `"` + key + `"`
		if containsSubstring(string(data), needle) {
			t.Errorf("optional field %q leaked into wire JSON: %s", key, data)
		}
	}
}
