package eventbridge

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"fyredocs/shared/queue"

	"job-service/internal/models"
)

func TestToolSpecificEventType_Variants(t *testing.T) {
	cases := []struct {
		tool         string
		jobEventType string
		want         string
		wantMapped   bool
	}{
		// Happy path: sign-pdf + JobCompleted → document.signed.
		{"sign-pdf", "JobCompleted", "document.signed", true},
		// JobFailed is success-only — no document.signed for
		// a failed signing job. The generic `job.failed` covers
		// the failure case.
		{"sign-pdf", "JobFailed", "", false},
		{"sign-pdf", "JobProgress", "", false},
		// Unmapped tools — only the generic job.* fires.
		{"merge-pdf", "JobCompleted", "", false},
		{"compress-pdf", "JobCompleted", "", false},
		{"ocr-pdf", "JobCompleted", "", false},
		// Empty / unknown.
		{"", "JobCompleted", "", false},
		{"", "", "", false},
	}
	for _, tc := range cases {
		got, ok := toolSpecificEventType(tc.tool, tc.jobEventType)
		if ok != tc.wantMapped {
			t.Errorf("toolSpecificEventType(%q, %q) ok = %v, want %v",
				tc.tool, tc.jobEventType, ok, tc.wantMapped)
		}
		if got != tc.want {
			t.Errorf("toolSpecificEventType(%q, %q) = %q, want %q",
				tc.tool, tc.jobEventType, got, tc.want)
		}
	}
}

func TestMapEventType_Variants(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		isMapped bool
	}{
		{"JobCompleted", "job.completed", true},
		{"JobFailed", "job.failed", true},
		{"JobProgress", "", false},
		{"JobQueued", "", false},
		{"", "", false},
		{"random.value", "", false},
	}
	for _, tc := range cases {
		got, ok := mapEventType(tc.in)
		if ok != tc.isMapped {
			t.Errorf("mapEventType(%q) ok = %v, want %v", tc.in, ok, tc.isMapped)
		}
		if got != tc.want {
			t.Errorf("mapEventType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// setupDB returns an in-memory sqlite with the ProcessingJob
// table migrated. Lets resolveUserID exercise the DB fallback
// without a real Postgres.
func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ProcessingJob{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestResolveUserID_PrefersEventField(t *testing.T) {
	// When the worker populated UserID, the bridge MUST NOT
	// hit the DB. Pinning this saves a query per event in the
	// common path.
	db := setupDB(t)
	uid := uuid.Must(uuid.NewV7()).String()
	got, err := resolveUserID(context.Background(), db, queue.JobEvent{
		UserID: uid,
		JobID:  uuid.Must(uuid.NewV7()).String(), // not in DB — proves no lookup happened
	})
	if err != nil {
		t.Fatalf("resolveUserID: %v", err)
	}
	if got != uid {
		t.Errorf("got %q, want %q", got, uid)
	}
}

func TestResolveUserID_FallsBackToProcessingJobsLookup(t *testing.T) {
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())
	jobID := uuid.Must(uuid.NewV7())

	// Seed a processing_jobs row for the lookup.
	job := models.ProcessingJob{
		ID:       jobID,
		UserID:   &userID,
		Status:   "completed",
		ToolType: "merge",
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := resolveUserID(context.Background(), db, queue.JobEvent{
		UserID: "", // worker emitted without populating
		JobID:  jobID.String(),
	})
	if err != nil {
		t.Fatalf("resolveUserID: %v", err)
	}
	if got != userID.String() {
		t.Errorf("got %q, want %q", got, userID.String())
	}
}

func TestResolveUserID_ReturnsEmptyForUnknownJob(t *testing.T) {
	// A JobEvent for a JobID that no longer has a row (cleanup-
	// worker reaped it) returns "" + nil — caller Acks + skips.
	// Returning an error here would Nak the message and retry
	// forever.
	db := setupDB(t)
	got, err := resolveUserID(context.Background(), db, queue.JobEvent{
		JobID: uuid.Must(uuid.NewV7()).String(),
	})
	if err != nil {
		t.Errorf("missing row must not error; got %v", err)
	}
	if got != "" {
		t.Errorf("missing row must return empty; got %q", got)
	}
}

func TestResolveUserID_ReturnsEmptyForRowWithNilUserID(t *testing.T) {
	// Anonymous (guest) jobs have UserID = nil on the row.
	// The bridge can't anchor a fanout to those — skip.
	db := setupDB(t)
	jobID := uuid.Must(uuid.NewV7())
	if err := db.Create(&models.ProcessingJob{
		ID:       jobID,
		UserID:   nil,
		Status:   "completed",
		ToolType: "merge",
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := resolveUserID(context.Background(), db, queue.JobEvent{
		JobID: jobID.String(),
	})
	if err != nil {
		t.Errorf("nil-user row must not error; got %v", err)
	}
	if got != "" {
		t.Errorf("nil-user row must return empty; got %q", got)
	}
}

func TestResolveUserID_ReturnsEmptyForMalformedJobID(t *testing.T) {
	db := setupDB(t)
	got, err := resolveUserID(context.Background(), db, queue.JobEvent{
		JobID: "not-a-uuid",
	})
	if err != nil {
		t.Errorf("malformed jobId must not error; got %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestResolveUserID_NilDBReturnsEmpty(t *testing.T) {
	got, err := resolveUserID(context.Background(), nil, queue.JobEvent{
		JobID: uuid.Must(uuid.NewV7()).String(),
	})
	if err != nil {
		t.Errorf("nil db must not error (no UserID to recover from); got %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// ---- jobEventData JSON shape ----

func TestJobEventData_OmitsEmptyFields(t *testing.T) {
	// job.completed events shouldn't carry FailureReason;
	// job.failed events shouldn't carry OutputPath / FileSize.
	// omitempty does the work — pin the contract.
	completed := jobEventData{
		JobID:      "j_1",
		Tool:       "merge",
		OutputPath: "/files/users/x/docs/y/output.pdf",
		FileSize:   12345,
	}
	bytes, _ := json.Marshal(completed)
	if string(bytes) == "" {
		t.Fatal("marshal returned empty")
	}
	s := string(bytes)
	for _, want := range []string{`"jobId":"j_1"`, `"tool":"merge"`, `"outputPath":"/files`, `"fileSize":12345`} {
		if !contains(s, want) {
			t.Errorf("missing %q in completed event: %s", want, s)
		}
	}
	if contains(s, "failureReason") {
		t.Errorf("completed event must not include failureReason: %s", s)
	}

	failed := jobEventData{
		JobID:         "j_2",
		Tool:          "ocr",
		FailureReason: "stage 3 timed out",
	}
	bytes2, _ := json.Marshal(failed)
	s2 := string(bytes2)
	for _, want := range []string{`"jobId":"j_2"`, `"tool":"ocr"`, `"failureReason":"stage 3 timed out"`} {
		if !contains(s2, want) {
			t.Errorf("missing %q in failed event: %s", want, s2)
		}
	}
	if contains(s2, "outputPath") || contains(s2, "fileSize") {
		t.Errorf("failed event must not include success-only fields: %s", s2)
	}
}

func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Unused-import guard: the test only uses `time` if a future
// failure expects a specific Timestamp shape. Reference it so
// the import survives a refactor.
var _ = time.Now

// ---- buildEventPayload / signedEventData ----

func TestBuildEventPayload_GenericCompletedPreservesJobEventData(t *testing.T) {
	// The generic `job.completed` payload must NOT leak signer
	// fields — they're only meaningful for `document.signed`.
	// A subscriber listening to `job.completed` for a sign-pdf
	// run gets the same shape as for any other tool.
	got, err := buildEventPayload("job.completed", queue.JobEvent{
		JobID:      "j_sign",
		ToolType:   "sign-pdf",
		OutputPath: "/files/users/u/docs/d/signed.pdf",
		FileSize:   54321,
	}, "user-1")
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		`"jobId":"j_sign"`,
		`"tool":"sign-pdf"`,
		`"outputPath":"/files/users/u/docs/d/signed.pdf"`,
		`"fileSize":54321`,
	} {
		if !contains(s, want) {
			t.Errorf("missing %q in completed payload: %s", want, s)
		}
	}
	for _, leak := range []string{`signerId`, `signMode`} {
		if contains(s, leak) {
			t.Errorf("job.completed must NOT leak %q: %s", leak, s)
		}
	}
}

func TestBuildEventPayload_DocumentSignedCarriesSignerAndMode(t *testing.T) {
	got, err := buildEventPayload("document.signed", queue.JobEvent{
		JobID:      "j_sign_2",
		ToolType:   "sign-pdf",
		OutputPath: "/files/users/u/docs/d/signed.pdf",
		FileSize:   12345,
	}, "user-7")
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	s := string(got)
	for _, want := range []string{
		`"jobId":"j_sign_2"`,
		`"tool":"sign-pdf"`,
		`"outputPath":"/files/users/u/docs/d/signed.pdf"`,
		`"fileSize":12345`,
		`"signerId":"user-7"`,
		// v0: image-stamp signature, NOT cryptographic. The
		// field is required so receivers always see a value;
		// "image" pins today's reality and reserves
		// pades-b-{b,t,lt,lta} for future cryptographic
		// modes.
		`"signMode":"image"`,
	} {
		if !contains(s, want) {
			t.Errorf("missing %q in signed payload: %s", want, s)
		}
	}
	// failureReason MUST NOT leak even though it's omitted —
	// document.signed is success-only by toolSpecificEventType's
	// gate.
	if contains(s, "failureReason") {
		t.Errorf("document.signed must NOT include failureReason: %s", s)
	}
}

func TestSignedEventData_RoundTripsSignerIDExactly(t *testing.T) {
	// signerId currently mirrors the JobEvent's resolved user.
	// Future delegated-signing flows might decouple the two;
	// pin the contract so a future refactor doesn't silently
	// drop the field.
	payload, err := buildEventPayload("document.signed", queue.JobEvent{
		JobID:    "j_round_trip",
		ToolType: "sign-pdf",
	}, "user-uuid-abc")
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	var decoded signedEventData
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode signed payload: %v", err)
	}
	if decoded.SignerID != "user-uuid-abc" {
		t.Errorf("signerId = %q, want user-uuid-abc", decoded.SignerID)
	}
	if decoded.SignMode != "image" {
		t.Errorf("signMode = %q, want image", decoded.SignMode)
	}
}

func TestSignedEventData_SignModeFieldAlwaysPresent(t *testing.T) {
	// signMode has no `omitempty` — it MUST always serialise
	// so subscribers can rely on the key existing.
	payload, _ := json.Marshal(signedEventData{
		JobID:    "j",
		Tool:     "sign-pdf",
		SignerID: "u",
		SignMode: "image",
	})
	if !contains(string(payload), `"signMode":"image"`) {
		t.Errorf("signMode missing from payload: %s", string(payload))
	}
	// signerId is also required for the same reason.
	if !contains(string(payload), `"signerId":"u"`) {
		t.Errorf("signerId missing from payload: %s", string(payload))
	}
}

func TestSignModeForTool_DefaultsToImageForUnknownTool(t *testing.T) {
	// Defensive default — if someone wires a new tool through
	// toolSpecificEventType without updating signModeForTool,
	// we still emit a documented value instead of "".
	if got := signModeForTool("sign-pdf"); got != "image" {
		t.Errorf("sign-pdf signMode = %q, want image", got)
	}
	if got := signModeForTool("future-pades-tool"); got != "image" {
		t.Errorf("unknown tool signMode = %q, want image (default)", got)
	}
}
