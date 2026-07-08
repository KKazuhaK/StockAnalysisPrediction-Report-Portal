// Package queue is the portal's run-queue scheduler: it scores waiting runs with a
// Slurm-style multifactor priority, keeps a reserved slot for 加急, and admits work
// against a global concurrency budget — non-preemptively (a running job is never
// interrupted). See docs/adr/0008-multifactor-priority.md.
//
// This package is pure (no I/O, no clock): internal/app computes each run's
// normalized factors (it owns the clock, usage history, and settings) and this
// package combines them into a score and does the admission.
package queue

// Weights are the multifactor priority weights (Slurm's PriorityWeight*). Each is
// applied to a factor normalized to [0,1].
type Weights struct {
	Base float64 // configured base priority (per group / system default)
	Age  float64 // waiting time (anti-starvation)
	Fair float64 // fair-share: recent usage vs the pack
}

// Factors are one run's normalized priority factors, each in [0,1], plus the 加急
// flag (a dominating boost + a reserved slot) and the mutually-exclusive idle flag
// (a symmetric bottom-anchor: "run only when the queue is otherwise idle").
type Factors struct {
	Base   float64
	Age    float64
	Fair   float64
	Urgent bool
	Idle   bool
}

// urgentBoost dominates any non-urgent score, so a 加急 run outranks every normal
// run regardless of weights, while age/base/fair still order 加急 runs among
// themselves. idleBoost is its mirror: subtracted from an idle run so it sinks below
// every non-idle run and only takes a slot when nothing else competes, while age/
// base/fair still order idle runs among themselves. Both are far larger than any
// achievable weighted factor sum (weights default 1000; see ADR 0008/0014).
const (
	urgentBoost = 1e9
	idleBoost   = 1e9
)

// Score combines the weighted factors into one priority; higher is admitted sooner.
// Urgent and idle are mutually exclusive (a run's priority is one of "urgent"/"idle"/
// a base number); urgent is checked first so it wins if both were ever set.
func (w Weights) Score(f Factors) float64 {
	s := w.Base*f.Base + w.Age*f.Age + w.Fair*f.Fair
	if f.Urgent {
		s += urgentBoost
	} else if f.Idle {
		s -= idleBoost
	}
	return s
}
