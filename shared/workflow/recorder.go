package workflow

import (
	"errors"
	"strings"
)

// Recorder collects Steps into a Workflow. Construct via
// NewRecorder, call Append for each action observed, and
// Finalise to produce a validated Workflow ready for storage.
//
// Goroutine-unsafe by design. The editor's record-button runs
// on one goroutine; the future workflow-service ingest path
// receives steps from a NATS subscriber that already
// serialises. Adding a mutex would be premature.
type Recorder struct {
	// workflow is the in-progress definition. Created at
	// NewRecorder time; mutated by Append.
	workflow Workflow

	// finalised is set true by Finalise. Subsequent Append /
	// Finalise calls return ErrRecorderFinalised. Prevents
	// the easy-to-make mistake of "Finalise to inspect, then
	// Append more".
	finalised bool
}

// ErrRecorderFinalised is returned by Append / Finalise after
// a Recorder has been Finalised once. Construct a new Recorder
// to start over.
var ErrRecorderFinalised = errors.New("workflow: recorder already finalised")

// ErrEmptyWorkflowID is the same idea as ErrEmptyID but
// surfaces at NewRecorder time — fail fast rather than at
// Finalise.
var ErrEmptyWorkflowID = errors.New("workflow: NewRecorder requires a non-empty ID")

// NewRecorder seeds a fresh Recorder with the workflow's
// identity + name. ID must be non-empty. CreatedAt is supplied
// by the caller (this package keeps no clock — see
// workflow.go). Steps slice is allocated lazily on the first
// Append so an unused Recorder doesn't carry overhead.
func NewRecorder(id, name, createdAt string) (*Recorder, error) {
	if strings.TrimSpace(id) == "" {
		return nil, ErrEmptyWorkflowID
	}
	return &Recorder{
		workflow: Workflow{
			ID:        id,
			Name:      name,
			Version:   FormatVersion,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		},
	}, nil
}

// SetDescription updates the workflow's free-form description.
// Idempotent; OK to call multiple times before Finalise.
func (r *Recorder) SetDescription(desc string) {
	if r.finalised {
		return
	}
	r.workflow.Description = desc
}

// Append validates a Step and adds it to the in-progress
// workflow. The validation here is the same Validate runs at
// Finalise — done eagerly so the caller learns about a bad
// step at the point of recording rather than at the end of a
// long session.
func (r *Recorder) Append(s Step) error {
	if r.finalised {
		return ErrRecorderFinalised
	}
	if strings.TrimSpace(s.ID) == "" {
		return ErrEmptyID
	}
	if strings.TrimSpace(s.Kind) == "" {
		return ErrEmptyStepKind
	}
	// Defensive copy of Params so the caller can reuse / mutate
	// their map without retroactively rewriting recorded steps.
	if len(s.Params) > 0 {
		copied := make(map[string]any, len(s.Params))
		for k, v := range s.Params {
			copied[k] = v
		}
		s.Params = copied
	}
	r.workflow.Steps = append(r.workflow.Steps, s)
	return nil
}

// Len reports the number of steps appended so far. Useful for
// the recording UI to display "5 steps recorded" without
// reaching into the workflow itself.
func (r *Recorder) Len() int {
	return len(r.workflow.Steps)
}

// Finalise validates the in-progress workflow + returns it.
// After Finalise the Recorder is locked — subsequent
// Append / Finalise return ErrRecorderFinalised so callers
// don't accidentally keep mutating a workflow that's already
// been handed off for storage.
//
// updatedAt overrides Workflow.UpdatedAt — typically the
// timestamp the recording was stopped, set by the calling
// service. Empty string keeps the value from NewRecorder.
func (r *Recorder) Finalise(updatedAt string) (Workflow, error) {
	if r.finalised {
		return Workflow{}, ErrRecorderFinalised
	}
	if updatedAt != "" {
		r.workflow.UpdatedAt = updatedAt
	}
	if err := r.workflow.Validate(); err != nil {
		return Workflow{}, err
	}
	r.finalised = true
	// Return a copy so post-Finalise callers can't reach back
	// in and mutate the recorder's internal state.
	out := r.workflow
	out.Steps = append([]Step{}, r.workflow.Steps...)
	return out, nil
}
