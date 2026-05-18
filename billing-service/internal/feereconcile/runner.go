package feereconcile

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"billing-service/internal/stripeclient"
)

// LookupFactory returns a Stripe lookup target for one
// reconciliation pass. The factory shape matches
// `handlers.stripeFactory` so main.go can pass the same closure
// it wires into the rest of the service — no second config
// path for the secret key.
type LookupFactory func() (*stripeclient.Client, error)

// RunnerOptions tunes the periodic driver around the
// single-pass `BackfillStripeFees` function. Mirrors the
// in-band Options struct but adds scheduler-only knobs.
//
// Defaults (zero values) are deliberately conservative:
//
//   - Interval: 10 minutes. Small enough that a failed-webhook
//     fee back-fills before the daily payout pass needs it,
//     large enough that a flapping Stripe doesn't get hammered.
//   - InitialDelay: 30 seconds. Lets the service finish binding
//     + the DB connection pool warm before the first call.
//     A 0 here would fire mid-startup before the readyz probe
//     flips green.
//   - PassOptions: zeroed → BackfillStripeFees uses its own
//     defaults (MinAge 5 min, MaxRows 100).
type RunnerOptions struct {
	Interval     time.Duration
	InitialDelay time.Duration
	PassOptions  Options
}

func (o RunnerOptions) interval() time.Duration {
	if o.Interval <= 0 {
		return 10 * time.Minute
	}
	return o.Interval
}

func (o RunnerOptions) initialDelay() time.Duration {
	if o.InitialDelay < 0 {
		return 0
	}
	if o.InitialDelay == 0 {
		return 30 * time.Second
	}
	return o.InitialDelay
}

// Runner is the periodic driver around `BackfillStripeFees`.
// Single goroutine, ticker-paced, stops cleanly on Stop().
//
// Design choices:
//
//   - The runner holds nothing about Stripe state beyond the
//     LookupFactory closure. Each pass calls the factory fresh
//     so a config rotation (e.g. STRIPE_API_KEY swap mid-run)
//     picks up the new key on the next tick.
//   - Per-tick errors NEVER stop the loop. A Stripe outage
//     mid-night must not require operator intervention to
//     restart the back-fill at sunrise. Stats from each pass
//     log at Info; pass-level errors log at Warn.
//   - The Start() method takes a `done chan<- struct{}`
//     hook for tests — production code passes nil. Tests pass
//     a buffered channel that signals after each pass so
//     assertions can synchronise on tick boundaries without
//     wall-clock sleeps.
type Runner struct {
	mu       sync.Mutex
	stopFn   context.CancelFunc
	stopped  chan struct{}
	stopping bool
}

// Start kicks off the periodic loop. Returns immediately;
// the loop runs in its own goroutine.
//
// `factory` is called once per tick to obtain the Stripe client.
// A factory returning (nil, err) skips THAT tick — logged at
// Warn so an operator can spot a stuck config. A nil factory
// is rejected at Start time; that's a programming bug, not a
// runtime config issue.
//
// `done` is an OPTIONAL hook used by tests: when non-nil,
// the runner sends an empty struct on `done` after each
// completed pass (success OR failure). Production callers
// pass nil.
//
// Calling Start more than once on the same Runner is a
// programming bug — the second call returns without doing
// anything. Construct a fresh Runner for each lifecycle.
func (r *Runner) Start(parent context.Context, db *gorm.DB, factory LookupFactory, opts RunnerOptions, done chan<- struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopFn != nil {
		slog.Warn("feereconcile: Runner.Start called twice; ignoring")
		return
	}
	if factory == nil {
		slog.Error("feereconcile: Runner.Start called with nil factory; refusing")
		return
	}

	ctx, cancel := context.WithCancel(parent)
	r.stopFn = cancel
	r.stopped = make(chan struct{})

	go r.loop(ctx, db, factory, opts, done)
}

// Stop blocks until the in-flight pass (if any) finishes and
// the goroutine exits. Idempotent — calling Stop twice is
// safe (the second call returns immediately). Safe to call
// before Start (no-op); useful for test cleanup paths.
func (r *Runner) Stop() {
	r.mu.Lock()
	if r.stopping || r.stopFn == nil {
		r.mu.Unlock()
		return
	}
	r.stopping = true
	stopped := r.stopped
	cancel := r.stopFn
	r.mu.Unlock()

	cancel()
	if stopped != nil {
		<-stopped
	}
}

func (r *Runner) loop(ctx context.Context, db *gorm.DB, factory LookupFactory, opts RunnerOptions, done chan<- struct{}) {
	defer close(r.stopped)

	// Initial delay before the first pass. Lets the service
	// finish binding + the DB pool warm. A select on ctx so a
	// shutdown during the wait doesn't strand the goroutine.
	initial := opts.initialDelay()
	if initial > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(initial):
		}
	}

	r.runOnce(ctx, db, factory, opts.PassOptions)
	if done != nil {
		select {
		case done <- struct{}{}:
		case <-ctx.Done():
			return
		}
	}

	tick := time.NewTicker(opts.interval())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.runOnce(ctx, db, factory, opts.PassOptions)
			if done != nil {
				select {
				case done <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// runOnce wraps one pass with the per-tick error logging.
// Never panics out (recover via deferred log) so a single
// bad row can't crash the goroutine.
func (r *Runner) runOnce(ctx context.Context, db *gorm.DB, factory LookupFactory, passOpts Options) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("feereconcile: pass panicked; loop continues", "panic", rec)
		}
	}()

	client, err := factory()
	if err != nil {
		slog.Warn("feereconcile: Stripe factory failed; skipping this pass", "error", err)
		return
	}
	if client == nil {
		slog.Warn("feereconcile: Stripe factory returned nil client; skipping this pass")
		return
	}
	stats, err := BackfillStripeFees(ctx, db, client, passOpts)
	if err != nil {
		slog.Warn("feereconcile: pass returned error; loop continues",
			"error", err,
			"scanned", stats.Scanned,
			"updated", stats.Updated,
			"lookup_errors", stats.LookupErrors)
		return
	}
	slog.Info("feereconcile: pass complete",
		"scanned", stats.Scanned,
		"updated", stats.Updated,
		"skipped_no_bt", stats.SkippedNoBT,
		"skipped_zero_fee", stats.SkippedZeroFee,
		"lookup_errors", stats.LookupErrors)
}
