package batch

import "context"

// Live per-item progress (which node the backend is currently executing). It is
// ephemeral — surfaced only while a row runs, never persisted — so it needs no
// schema change (docs/adr/0001-batch-run-engine.md). The engine attaches a sink to
// the per-item context; a Provider reports node changes through it.

// Progress is a live status update for the currently running item.
type Progress struct {
	Node  string // current node title
	Index int    // node sequence index within the run
}

type progressKey struct{}

// WithProgress attaches a per-item progress sink to ctx (the engine sets it per row;
// a Provider reports through ReportProgress).
func WithProgress(ctx context.Context, fn func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// ReportProgress lets a Provider report the current item's progress. It is a no-op
// when the engine attached no sink (tests, or a run that isn't tracking progress).
func ReportProgress(ctx context.Context, p Progress) {
	if fn, ok := ctx.Value(progressKey{}).(func(Progress)); ok && fn != nil {
		fn(p)
	}
}
