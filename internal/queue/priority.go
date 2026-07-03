// Package queue is the portal's priority run-queue scheduler: it orders waiting
// runs by priority with bounded aging, keeps a reserved slot for the top tier, and
// admits work against a global concurrency budget — non-preemptively (a running
// job is never interrupted). See docs/adr/0004-run-queue.md.
//
// This package is pure (no I/O, the clock is injected via enqueue timestamps) so
// the scheduling policy is unit-testable in isolation; internal/app wires it to the
// store and the batch engine.
package queue

// Level is one priority tier. Value orders tiers for display/tie-break (lower =
// higher priority). Offset is the aging concession in seconds: a larger Offset
// means the tier yields to higher tiers for that many seconds of waiting before its
// own wait time takes over — so the Offset *delta* between two tiers is the maximum
// time the lower one is overtaken, i.e. its starvation cap. Reserved marks the top
// tier that gets a dedicated worker slot.
type Level struct {
	Name     string
	Value    int
	Offset   int64
	Reserved bool
}

// Registry is a set of priority levels keyed by name, plus a fallback used for
// unknown level strings (always the lowest priority, so bad data can never jump
// the queue).
type Registry struct {
	byName   map[string]Level
	fallback Level
}

// NewRegistry builds a registry from the given levels. The fallback for unknown
// names is a synthetic lowest-priority tier (huge offset, not reserved).
func NewRegistry(levels []Level) Registry {
	r := Registry{byName: make(map[string]Level, len(levels))}
	for _, l := range levels {
		r.byName[l.Name] = l
	}
	// Unknown levels sort dead last and never get the reserved slot.
	r.fallback = Level{Name: "", Value: 1 << 30, Offset: 1 << 40, Reserved: false}
	return r
}

// DefaultRegistry is the seed taxonomy from ADR 0004: 加急 / 普通 / 其他.
func DefaultRegistry() Registry {
	return NewRegistry([]Level{
		{Name: "urgent", Value: 10, Offset: 0, Reserved: true},            // 加急: jumps the queue, reserved slot
		{Name: "normal", Value: 100, Offset: 30 * 60, Reserved: false},    // 普通: yields to urgent for ≤30min
		{Name: "other", Value: 200, Offset: 2 * 60 * 60, Reserved: false}, // 其他: yields for ≤2h
	})
}

// Get returns the named level, or the fallback for an unknown name.
func (r Registry) Get(name string) Level {
	if l, ok := r.byName[name]; ok {
		return l
	}
	return r.fallback
}

// Has reports whether a level name is registered.
func (r Registry) Has(name string) bool {
	_, ok := r.byName[name]
	return ok
}

// SchedKey is the item's fixed, sortable scheduling key: enqueue time shifted by
// the level's aging offset. Dequeue picks the smallest key. Because the key is a
// pure function of enqueue time, aging needs no background timer — a lower tier's
// older enqueue time eventually beats a higher tier's newer one, bounded by the
// offset delta.
func (r Registry) SchedKey(level string, enqueuedAtUnix int64) int64 {
	return enqueuedAtUnix + r.Get(level).Offset
}
