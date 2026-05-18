// Package workflow is the recording + replay library for
// Fyredocs workflows. A workflow is an ordered list of steps,
// each describing one action (an editor op, an outbound notify
// call, an upload, etc.) plus a key-value bag of parameters.
//
// The library is the canonical format the editor's record-button
// will produce + the future workflow-service will store and run.
// Lives in `shared/` so editor-service, workflow-service, the
// CLI, and analytics-service all consume one definition without
// cross-imports.
//
// Design constraints:
//   - The library is op-agnostic. We do not enumerate every step
//     kind here; that's the workflow-service / executor's job.
//     The recorder accepts whatever (kind, params) the caller
//     emits, and Replay calls a caller-supplied function per
//     step. Adding a new editor op doesn't require an SDK
//     release.
//   - Stable JSON. The wire format is a small typed shape that
//     round-trips cleanly. Field names are camelCase to match
//     every other public API in the system.
//   - Pure. No DB, no HTTP, no time-of-day, no randomness. The
//     library is the recording format + replay engine; storage
//     and scheduling live in workflow-service.
package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// FormatVersion is the wire-format version this package emits.
// Bumping is a breaking change — store the version on every
// serialised workflow so future readers can refuse-or-upgrade
// definitions they don't understand.
//
// Promote to 2 only when an incompatible shape change lands;
// additive changes that older readers can ignore via
// `json.Unmarshal`'s default leave the version at 1.
const FormatVersion = 1

// Workflow is one recorded process: name, identity, and an
// ordered list of steps. Empty steps are valid (a draft
// recording), but Replay on an empty workflow is a no-op.
type Workflow struct {
	// ID is the stable identifier — UUIDv7 in production,
	// caller-supplied for tests. Must be set before
	// serialisation; the recorder validates this at Finalise
	// time.
	ID string `json:"id"`

	// Name is the human-friendly label. Free-form; not unique.
	Name string `json:"name"`

	// Description is optional explanatory text — e.g., "What
	// this workflow does, when it should run, who owns it."
	Description string `json:"description,omitempty"`

	// Version is the FormatVersion this workflow was recorded
	// against. Set automatically by the Recorder.
	Version int `json:"version"`

	// Steps is the ordered list of recorded actions. Order is
	// preserved through serialisation + Replay.
	Steps []Step `json:"steps"`

	// CreatedAt + UpdatedAt are ISO-8601 strings supplied by
	// the caller (no time.Now in this library — clocks live in
	// the calling service so tests stay deterministic).
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// Step is one recorded action. Kind is a free-form string the
// caller picks (e.g., `editor.page.rotate`, `notify.email`,
// `wait`); Params is an arbitrary JSON object the Replay
// executor consumes. We intentionally keep Params as
// `map[string]any` rather than a closed union so the recorder
// stays op-agnostic — adding a new op never requires a code
// change here.
type Step struct {
	// ID is a per-step identifier — stable across replays so
	// observability traces can correlate. Caller-supplied
	// (typically a short uuid).
	ID string `json:"id"`

	// Kind is the action discriminator. Examples:
	// `editor.page.rotate`, `editor.annotation.add`,
	// `notify.email`, `wait`. Non-empty is required.
	Kind string `json:"kind"`

	// Params carries the per-step inputs. Marshalled as JSON;
	// arbitrary nested shape supported. Empty map is allowed
	// (some steps need no parameters — `wait` for instance
	// may use only its Kind).
	Params map[string]any `json:"params,omitempty"`

	// ContinueOnError lets a step fail without halting the
	// workflow. Default (false) is fail-fast: Replay returns
	// the first error and stops. With true, Replay collects
	// errors and continues — useful for fan-out
	// notifications where each recipient is independent.
	ContinueOnError bool `json:"continueOnError,omitempty"`
}

// ErrEmptyID is returned by Finalise / MarshalJSON guards when
// a workflow or step is missing its required ID. We don't
// auto-generate IDs in this library so the caller stays
// deterministic in tests.
var ErrEmptyID = errors.New("workflow: ID is required")

// ErrEmptyStepKind is returned when a step's Kind is the empty
// string. Kind is the only field Replay must dispatch on; an
// empty Kind would land a step in the runFn with no
// disambiguator.
var ErrEmptyStepKind = errors.New("workflow: step Kind is required")

// ErrFutureVersion is returned by Decode when the serialised
// Version is greater than FormatVersion. Callers should treat
// this as a corruption / forward-compat error and refuse to
// run the workflow.
var ErrFutureVersion = errors.New("workflow: definition uses a future format version")

// Validate checks the workflow's structural invariants —
// required fields populated, every step has a Kind. Returns
// nil on success. Called automatically by Encode / Finalise;
// callers that build a Workflow by hand can run it explicitly.
func (w Workflow) Validate() error {
	if strings.TrimSpace(w.ID) == "" {
		return fmt.Errorf("%w: workflow.ID", ErrEmptyID)
	}
	if w.Version != FormatVersion {
		if w.Version > FormatVersion {
			return ErrFutureVersion
		}
		return fmt.Errorf("workflow: Version=%d, expected %d", w.Version, FormatVersion)
	}
	for i, s := range w.Steps {
		if strings.TrimSpace(s.ID) == "" {
			return fmt.Errorf("%w: steps[%d].ID", ErrEmptyID, i)
		}
		if strings.TrimSpace(s.Kind) == "" {
			return fmt.Errorf("%w: steps[%d]", ErrEmptyStepKind, i)
		}
	}
	return nil
}

// Encode serialises the workflow to JSON bytes, validating
// first. Use this rather than json.Marshal directly so the
// FormatVersion / Validate gate runs.
func (w Workflow) Encode() ([]byte, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

// Decode parses a JSON workflow definition + validates. Mirror
// of Encode — refuses ErrFutureVersion definitions so callers
// don't accidentally run a workflow they can't interpret.
func Decode(raw []byte) (Workflow, error) {
	var w Workflow
	if err := json.Unmarshal(raw, &w); err != nil {
		return Workflow{}, fmt.Errorf("workflow: decode: %w", err)
	}
	if err := w.Validate(); err != nil {
		return Workflow{}, err
	}
	return w, nil
}
