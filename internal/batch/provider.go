// Package batch is the backend-agnostic batch-run engine. It triggers a Provider
// over many input rows with configurable concurrency, retries, cancellation, and
// crash recovery. The sole Provider implementation today is the declarative
// manifest interpreter (see manifest.go); the interface is the seam where a future
// sandboxed provider could slot in without touching the engine. See
// docs/adr/0001-batch-run-engine.md.
package batch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Outcome is the normalised result of a single run, read only from the backend's
// own response.
type Outcome int

const (
	Failed Outcome = iota
	Partial
	Ok
	// Untracked: the run demonstrably started on the backend (it was dispatched / the stream
	// opened) but its true outcome could not be determined — no reconcilable handle, or a
	// reconcile that never reached a terminal state. It is terminal-but-neutral: NOT a failure
	// (the run may well have succeeded and cost tokens) and never retried, so a started run is
	// never duplicated. The operator can reconcile it manually. See docs/adr/0006-dify-native.md.
	Untracked
)

func (o Outcome) String() string {
	switch o {
	case Ok:
		return "ok"
	case Partial:
		return "partial"
	case Untracked:
		return "untracked"
	default:
		return "failed"
	}
}

// RunResult is what a Provider reports for one triggered run.
type RunResult struct {
	RunID  string          // backend's run identifier, for tracing
	Status Outcome         // ok | partial | failed
	Detail string          // human-readable note (e.g. backend error message)
	Raw    json.RawMessage // backend's raw response, for debugging
}

// Provider triggers one unit of work and reports a normalised outcome. Run returns
// an error only for transport-level problems (network, timeout, 5xx, 4xx, or a
// malformed request); a backend that ran but reported failure comes back as
// RunResult{Status: Failed}, not an error. Transport errors are wrapped so
// IsTransient can classify them for the retry policy.
type Provider interface {
	Run(ctx context.Context, inputs map[string]string) (RunResult, error)
}

// Reconciler is an optional Provider capability: recover a run's terminal outcome from a
// persisted handle (run id / conversation id) WITHOUT re-running it. The run-queue uses it on
// restart and for the admin's manual reconcile so a run that started before a crash is settled
// by its true outcome instead of being fired again. A Provider that can't reconcile (e.g. the
// manifest HTTP provider, whose runs are atomic) simply doesn't implement it.
type Reconciler interface {
	Reconcile(ctx context.Context, runID, convID string) (RunResult, error)
}

// runError carries a transient/permanent classification for the engine's retry
// policy.
type runError struct {
	msg       string
	transient bool
}

func (e *runError) Error() string   { return e.msg }
func (e *runError) Transient() bool { return e.transient }

func transientErr(format string, a ...any) error {
	return &runError{msg: fmt.Sprintf(format, a...), transient: true}
}

func permanentErr(format string, a ...any) error {
	return &runError{msg: fmt.Sprintf(format, a...), transient: false}
}

// IsTransient reports whether err warrants a retry. Errors that do not carry a
// classification are treated as permanent.
func IsTransient(err error) bool {
	var te interface{ Transient() bool }
	if errors.As(err, &te) {
		return te.Transient()
	}
	return false
}
