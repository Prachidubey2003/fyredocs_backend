package queue

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSubjectForBillable(t *testing.T) {
	cases := []struct {
		eventType, want string
	}{
		{"op.merge", "billable.events.op.merge"},
		{"op.ocr", "billable.events.op.ocr"},
		{"ai.tokens", "billable.events.ai.tokens"},
	}
	for _, tc := range cases {
		got := SubjectForBillable(tc.eventType)
		if got != tc.want {
			t.Errorf("SubjectForBillable(%q) = %q, want %q", tc.eventType, got, tc.want)
		}
	}
}

func TestBillableEvent_JSONRoundtrip(t *testing.T) {
	when := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	in := BillableEvent{
		UserID:     "11111111-1111-1111-1111-111111111111",
		APIKeyID:   "22222222-2222-2222-2222-222222222222",
		EventType:  "op.ocr",
		Quantity:   50,
		Unit:       "pages",
		OccurredAt: when,
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out BillableEvent
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\ngot:  %+v\nwant: %+v", out, in)
	}
}

func TestBillableEvent_OmitsEmptyAPIKey(t *testing.T) {
	// `apiKeyId` is JSON-omitempty so UI-driven ops (no key) don't
	// carry a useless empty string. Important because subscribers
	// uuid.Parse("") would otherwise log a warning on every event.
	in := BillableEvent{
		UserID:     "11111111-1111-1111-1111-111111111111",
		EventType:  "op.merge",
		Quantity:   1,
		Unit:       "ops",
		OccurredAt: time.Now().UTC(),
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(data); !jsonOmitsKey(got, "apiKeyId") {
		t.Errorf("apiKeyId should be omitted when empty; got %s", got)
	}
}

// jsonOmitsKey is a stringy helper: returns true if the given key
// name does NOT appear in the JSON blob. Crude but adequate — we
// just need to confirm the omitempty tag works.
func jsonOmitsKey(blob, key string) bool {
	needle := `"` + key + `"`
	for i := 0; i+len(needle) <= len(blob); i++ {
		if blob[i:i+len(needle)] == needle {
			return false
		}
	}
	return true
}
