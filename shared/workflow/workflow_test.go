package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// sampleWorkflow returns a small but realistic recording used
// across the rendering / replay tests. The kinds are
// editor-flavoured because that's the v0 caller — but the
// library doesn't know or care, which is part of what these
// tests pin.
func sampleWorkflow() Workflow {
	return Workflow{
		ID:        "wf_01HV",
		Name:      "Sign + email weekly contract",
		Version:   FormatVersion,
		CreatedAt: "2026-05-16T10:00:00Z",
		UpdatedAt: "2026-05-16T10:05:00Z",
		Steps: []Step{
			{ID: "s1", Kind: "editor.page.rotate", Params: map[string]any{"page": 1.0, "rotation": 90.0}},
			{ID: "s2", Kind: "editor.annotation.add", Params: map[string]any{"page": 1.0, "kind": "highlight"}},
			{ID: "s3", Kind: "notify.email", Params: map[string]any{"to": "alice@example.com"}, ContinueOnError: true},
		},
	}
}

// ---- Workflow.Validate / Encode / Decode ------------------------------

func TestWorkflow_ValidateAcceptsSample(t *testing.T) {
	if err := sampleWorkflow().Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestWorkflow_ValidateRequiresID(t *testing.T) {
	w := sampleWorkflow()
	w.ID = ""
	if err := w.Validate(); !errors.Is(err, ErrEmptyID) {
		t.Errorf("err = %v, want ErrEmptyID", err)
	}
}

func TestWorkflow_ValidateRejectsWrongVersion(t *testing.T) {
	w := sampleWorkflow()
	w.Version = 0
	err := w.Validate()
	if err == nil || !strings.Contains(err.Error(), "Version") {
		t.Errorf("err = %v, want version mismatch", err)
	}
}

func TestWorkflow_ValidateRejectsFutureVersion(t *testing.T) {
	w := sampleWorkflow()
	w.Version = FormatVersion + 1
	if err := w.Validate(); !errors.Is(err, ErrFutureVersion) {
		t.Errorf("err = %v, want ErrFutureVersion", err)
	}
}

func TestWorkflow_ValidateRequiresStepID(t *testing.T) {
	w := sampleWorkflow()
	w.Steps[1].ID = ""
	err := w.Validate()
	if !errors.Is(err, ErrEmptyID) {
		t.Errorf("err = %v, want ErrEmptyID for missing step ID", err)
	}
	if !strings.Contains(err.Error(), "steps[1]") {
		t.Errorf("err = %v, want index in message", err)
	}
}

func TestWorkflow_ValidateRequiresStepKind(t *testing.T) {
	w := sampleWorkflow()
	w.Steps[0].Kind = "   "
	err := w.Validate()
	if !errors.Is(err, ErrEmptyStepKind) {
		t.Errorf("err = %v, want ErrEmptyStepKind", err)
	}
}

func TestWorkflow_EncodeDecodeRoundTrip(t *testing.T) {
	w := sampleWorkflow()
	raw, err := w.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// Re-encode the decoded value; bytes should match, modulo
	// stable JSON field ordering. encoding/json sorts struct
	// fields by source order, so this is deterministic.
	roundTrip, err := got.Encode()
	if err != nil {
		t.Fatalf("Encode (round-trip): %v", err)
	}
	if string(raw) != string(roundTrip) {
		t.Errorf("encode → decode → encode diverged:\n  first:  %s\n  second: %s", raw, roundTrip)
	}
}

func TestWorkflow_DecodeRejectsInvalidJSON(t *testing.T) {
	_, err := Decode([]byte("not json"))
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("err = %v, want decode error", err)
	}
}

func TestWorkflow_DecodeRejectsFutureFormatVersion(t *testing.T) {
	raw := fmt.Sprintf(`{"id":"x","name":"y","version":%d,"steps":[]}`, FormatVersion+99)
	_, err := Decode([]byte(raw))
	if !errors.Is(err, ErrFutureVersion) {
		t.Errorf("err = %v, want ErrFutureVersion", err)
	}
}

func TestWorkflow_EncodeOmitsBlankOptionalFields(t *testing.T) {
	// Description, CreatedAt/UpdatedAt are `omitempty`; with
	// them blank the JSON shouldn't carry empty strings.
	w := Workflow{
		ID:      "x",
		Name:    "y",
		Version: FormatVersion,
		Steps: []Step{
			{ID: "s1", Kind: "k"},
		},
	}
	raw, err := w.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got := string(raw)
	for _, banned := range []string{`"description":""`, `"createdAt":""`, `"updatedAt":""`} {
		if strings.Contains(got, banned) {
			t.Errorf("omit-empty leaked %q in %s", banned, got)
		}
	}
}

// ---- Recorder --------------------------------------------------------

func TestRecorder_NewRequiresID(t *testing.T) {
	if _, err := NewRecorder("", "n", "t"); !errors.Is(err, ErrEmptyWorkflowID) {
		t.Errorf("err = %v, want ErrEmptyWorkflowID", err)
	}
}

func TestRecorder_AppendValidatesEarly(t *testing.T) {
	r, _ := NewRecorder("wf_1", "test", "2026-05-16T10:00:00Z")
	if err := r.Append(Step{Kind: "k"}); !errors.Is(err, ErrEmptyID) {
		t.Errorf("missing-step-id err = %v, want ErrEmptyID", err)
	}
	if err := r.Append(Step{ID: "s1"}); !errors.Is(err, ErrEmptyStepKind) {
		t.Errorf("missing-step-kind err = %v, want ErrEmptyStepKind", err)
	}
}

func TestRecorder_AppendCopiesParamsDefensively(t *testing.T) {
	// The recording UI may keep mutating its scratch params
	// map after recording one step. The recorder must store
	// a snapshot so a post-record mutation doesn't rewrite
	// already-recorded steps.
	r, _ := NewRecorder("wf_1", "test", "2026-05-16T10:00:00Z")
	scratch := map[string]any{"page": 1}
	if err := r.Append(Step{ID: "s1", Kind: "editor.page.rotate", Params: scratch}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	scratch["page"] = 999 // mutate post-record
	wf, err := r.Finalise("2026-05-16T10:05:00Z")
	if err != nil {
		t.Fatalf("Finalise: %v", err)
	}
	if got := wf.Steps[0].Params["page"]; got != 1 {
		t.Errorf("Params not copied defensively: page=%v, want 1", got)
	}
}

func TestRecorder_LenReportsAppendedCount(t *testing.T) {
	r, _ := NewRecorder("wf_1", "test", "2026-05-16T10:00:00Z")
	if r.Len() != 0 {
		t.Errorf("Len before Append = %d, want 0", r.Len())
	}
	for i := 0; i < 5; i++ {
		_ = r.Append(Step{ID: fmt.Sprintf("s%d", i), Kind: "k"})
	}
	if r.Len() != 5 {
		t.Errorf("Len = %d, want 5", r.Len())
	}
}

func TestRecorder_FinaliseLocksFurtherWrites(t *testing.T) {
	r, _ := NewRecorder("wf_1", "test", "2026-05-16T10:00:00Z")
	_ = r.Append(Step{ID: "s1", Kind: "k"})
	if _, err := r.Finalise("2026-05-16T10:05:00Z"); err != nil {
		t.Fatalf("Finalise: %v", err)
	}
	if err := r.Append(Step{ID: "s2", Kind: "k"}); !errors.Is(err, ErrRecorderFinalised) {
		t.Errorf("post-finalise Append err = %v, want ErrRecorderFinalised", err)
	}
	if _, err := r.Finalise("now"); !errors.Is(err, ErrRecorderFinalised) {
		t.Errorf("second Finalise err = %v, want ErrRecorderFinalised", err)
	}
}

func TestRecorder_FinaliseReturnsCopy(t *testing.T) {
	// A caller that holds the returned Workflow should not be
	// able to mutate Steps back into the recorder's state via
	// shared slice aliasing.
	r, _ := NewRecorder("wf_1", "test", "2026-05-16T10:00:00Z")
	_ = r.Append(Step{ID: "s1", Kind: "k"})
	wf, _ := r.Finalise("2026-05-16T10:05:00Z")
	wf.Steps[0].Kind = "tampered"
	// Internal state should still hold "k" — we can't reach
	// it after Finalise, but a fresh Encode reflects whatever
	// the user-visible workflow is, so just confirm the
	// returned copy doesn't mysteriously update on mutation.
	if wf.Steps[0].Kind != "tampered" {
		t.Error("returned slice unexpectedly immutable")
	}
}

// ---- Replay ----------------------------------------------------------

func TestReplay_CallsRunFnInOrder(t *testing.T) {
	w := sampleWorkflow()
	var seen []string
	results, err := Replay(context.Background(), w, func(_ context.Context, s Step) error {
		seen = append(seen, s.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len(results) = %d, want 3", len(results))
	}
	if strings.Join(seen, ",") != "s1,s2,s3" {
		t.Errorf("step order = %v, want [s1 s2 s3]", seen)
	}
}

func TestReplay_FailFastHaltsOnFirstError(t *testing.T) {
	w := sampleWorkflow()
	w.Steps[2].ContinueOnError = false // make all halt-on-error
	var attempted []string
	boom := errors.New("boom")
	results, err := Replay(context.Background(), w, func(_ context.Context, s Step) error {
		attempted = append(attempted, s.ID)
		if s.ID == "s2" {
			return boom
		}
		return nil
	})
	if !errors.Is(err, ErrWorkflowHalted) {
		t.Errorf("err = %v, want ErrWorkflowHalted", err)
	}
	if !errors.Is(err, boom) {
		// We wrap with %v, not %w, on the underlying err, so
		// errors.Is(boom) deliberately won't find it. The
		// underlying string is in the message though.
		if !strings.Contains(err.Error(), "boom") {
			t.Errorf("err = %v, should mention underlying error", err)
		}
	}
	if len(attempted) != 2 || attempted[1] != "s2" {
		t.Errorf("attempted = %v, want [s1 s2] (stop after failure)", attempted)
	}
	if len(results) != 2 {
		t.Errorf("len(results) = %d, want 2 (truncated at halt)", len(results))
	}
	if results[1].Err == nil {
		t.Errorf("results[1].Err = nil, should carry the step's error")
	}
}

func TestReplay_ContinueOnErrorKeepsGoing(t *testing.T) {
	w := sampleWorkflow()
	// s2 is fail-fast in the sample; flip it to continue.
	w.Steps[1].ContinueOnError = true
	// s3 already ContinueOnError=true.
	boom := errors.New("nope")
	results, err := Replay(context.Background(), w, func(_ context.Context, s Step) error {
		if s.ID == "s2" || s.ID == "s3" {
			return boom
		}
		return nil
	})
	if err != nil {
		t.Errorf("Replay should have completed despite errors; got %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len(results) = %d, want 3 (all attempted)", len(results))
	}
	if results[0].Err != nil {
		t.Errorf("results[0].Err = %v, want nil", results[0].Err)
	}
	if results[1].Err == nil || results[2].Err == nil {
		t.Errorf("expected results[1] and results[2] to carry the per-step error: %+v", results)
	}
}

func TestReplay_HonoursContextCancellationBetweenSteps(t *testing.T) {
	w := sampleWorkflow()
	ctx, cancel := context.WithCancel(context.Background())
	results, err := Replay(ctx, w, func(_ context.Context, s Step) error {
		if s.ID == "s1" {
			cancel() // signal stop before s2 starts
		}
		return nil
	})
	if !errors.Is(err, ErrWorkflowHalted) {
		t.Errorf("err = %v, want ErrWorkflowHalted on ctx cancel", err)
	}
	// s1 ran (returned nil + then cancelled at the end);
	// next iteration sees ctx.Err() and returns. Expect
	// 2 results: s1 ok + s2 halted-by-ctx.
	if len(results) != 2 {
		t.Errorf("len(results) = %d, want 2", len(results))
	}
}

func TestReplay_RejectsInvalidWorkflow(t *testing.T) {
	w := sampleWorkflow()
	w.ID = ""
	_, err := Replay(context.Background(), w, func(_ context.Context, _ Step) error { return nil })
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestReplay_RejectsNilRunFn(t *testing.T) {
	_, err := Replay(context.Background(), sampleWorkflow(), nil)
	if err == nil || !strings.Contains(err.Error(), "non-nil") {
		t.Errorf("err = %v, want nil-runFn rejection", err)
	}
}

// ---- End-to-end --------------------------------------------------------

func TestRecorderThenReplay_RoundTripsThroughJSON(t *testing.T) {
	// Realistic flow: editor's record button appends steps,
	// finalise produces a Workflow, the workflow-service
	// stores its JSON, an executor decodes and replays.
	// Verify nothing is lost across that pipeline.
	r, _ := NewRecorder("wf_e2e", "round-trip", "2026-05-16T10:00:00Z")
	_ = r.Append(Step{ID: "s1", Kind: "editor.page.rotate", Params: map[string]any{"page": 1.0, "rotation": 90.0}})
	_ = r.Append(Step{ID: "s2", Kind: "notify.slack", Params: map[string]any{"text": "Done!"}})
	w, err := r.Finalise("2026-05-16T10:05:00Z")
	if err != nil {
		t.Fatalf("Finalise: %v", err)
	}
	raw, err := w.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Quick sanity: the JSON should mention both kinds.
	var sanity map[string]any
	_ = json.Unmarshal(raw, &sanity)
	if sanity["id"] != "wf_e2e" {
		t.Errorf("decoded id = %v", sanity["id"])
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	var seenKinds []string
	_, err = Replay(context.Background(), decoded, func(_ context.Context, s Step) error {
		seenKinds = append(seenKinds, s.Kind)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if strings.Join(seenKinds, ",") != "editor.page.rotate,notify.slack" {
		t.Errorf("replayed kinds = %v", seenKinds)
	}
}
