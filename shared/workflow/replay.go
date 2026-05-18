package workflow

import (
	"context"
	"errors"
	"fmt"
)

// ReplayResult is the per-step outcome from Replay. Returned
// in the same order as Workflow.Steps; index aligns 1:1 with
// the source list so callers can correlate failures to the
// recording UI.
type ReplayResult struct {
	// StepID echoes Step.ID from the source workflow — saves
	// every caller from having to track the index themselves.
	StepID string
	// Err is non-nil iff the step's run function returned an
	// error. When the workflow halted on this step (i.e.,
	// fail-fast was set on the step), every subsequent
	// Steps entry is absent from Results — Replay does NOT
	// pad with zero values, so len(Results) is the count of
	// steps actually attempted.
	Err error
	// Skipped is true when the step was skipped — reserved
	// for future conditional / when-clause support; today
	// always false but exposed in the result so adding
	// conditions later doesn't change the call site.
	Skipped bool
}

// StepRunFunc is the per-step executor Replay calls. The
// library is op-agnostic; the caller's runFn dispatches on
// step.Kind to the right backend (editor-service edit op,
// notify-service send, etc.). Return non-nil error to signal
// failure — fail-fast vs continue-on-error is then driven by
// step.ContinueOnError.
//
// The context is forwarded as-is from Replay's caller — used
// for cancellation + deadlines.
type StepRunFunc func(ctx context.Context, step Step) error

// ErrWorkflowHalted is the sentinel returned by Replay when at
// least one fail-fast step errored. Callers can errors.Is
// against it to distinguish "the underlying step's error"
// from "the replay engine deliberately halted." The wrapped
// chain still carries the step's error for diagnostics.
var ErrWorkflowHalted = errors.New("workflow: halted on step error")

// Replay walks the workflow's steps in order, calling runFn
// for each one. The dispatch is fully synchronous — concurrent
// step execution / dependency graphs are out of scope for v0
// (and would change the on-wire shape of Workflow when they
// land).
//
// Semantics:
//   - For each step, runFn is called with the supplied ctx
//     and the step verbatim.
//   - If runFn returns nil, Replay continues to the next step.
//   - If runFn returns non-nil AND step.ContinueOnError is
//     true, the error is recorded and Replay continues.
//   - If runFn returns non-nil AND step.ContinueOnError is
//     false, the error is recorded, Replay returns
//     ErrWorkflowHalted (with the step's error wrapped), and
//     subsequent steps are NOT attempted.
//   - If ctx is cancelled mid-replay, the next iteration
//     returns ctx.Err() wrapped in the same halt envelope.
//
// The returned []ReplayResult has one entry per attempted step
// (may be shorter than len(workflow.Steps) when halted).
//
// Returns an error only when at least one halt-on-error step
// failed. continue-on-error failures are reflected in the
// per-step Result.Err but do not produce a top-level error.
// This keeps the "everything completed, but two notifications
// failed" path easy to distinguish from "the workflow died
// halfway through".
func Replay(ctx context.Context, w Workflow, runFn StepRunFunc) ([]ReplayResult, error) {
	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("workflow: invalid definition: %w", err)
	}
	if runFn == nil {
		return nil, errors.New("workflow: Replay requires a non-nil StepRunFunc")
	}

	results := make([]ReplayResult, 0, len(w.Steps))
	for _, step := range w.Steps {
		// Honour ctx cancellation between steps. Even a
		// fast-running step deserves a chance to be skipped
		// when the caller has signalled "stop". The runFn
		// itself should also honour ctx — we don't double-check
		// here, but defensive callers do both.
		if err := ctx.Err(); err != nil {
			results = append(results, ReplayResult{
				StepID: step.ID,
				Err:    fmt.Errorf("%w: %v", ErrWorkflowHalted, err),
			})
			return results, fmt.Errorf("%w: %v", ErrWorkflowHalted, err)
		}

		err := runFn(ctx, step)
		results = append(results, ReplayResult{StepID: step.ID, Err: err})
		if err != nil && !step.ContinueOnError {
			return results, fmt.Errorf("%w: step %q (%s): %v",
				ErrWorkflowHalted, step.ID, step.Kind, err)
		}
	}
	return results, nil
}
