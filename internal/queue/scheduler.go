package queue

import "sort"

// Item is the minimal unit the scheduler orders: an id, its priority level, and
// the precomputed scheduling key (see Registry.SchedKey).
type Item struct {
	ID       int64
	Level    string
	SchedKey int64
}

// Plan is the scheduler's capacity configuration.
type Plan struct {
	Budget   int // total concurrent runs allowed across the whole queue
	Reserved int // slots held for the top (Reserved) tier; clamped to [0, Budget-1]
}

// byKey sorts items by scheduling key, breaking ties by id (older/lower id first)
// so the order is deterministic and FIFO within a tier.
func byKey(items []Item) []Item {
	out := make([]Item, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SchedKey != out[j].SchedKey {
			return out[i].SchedKey < out[j].SchedKey
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Admit selects, from the waiting items, which to start now given how many runs
// are already in flight and the plan. It is non-preemptive: it only ever returns
// waiting items and never touches running ones.
//
// Items are considered in scheduling-key order. A reserved slot is held for the top
// (Reserved) tier while any top item is still waiting, so a fresh 加急 always has a
// lane even behind a backlog of aged lower-tier items — and those lower-tier items
// still drain through the non-reserved slots, so they don't starve. When no top
// item waits, lower tiers borrow the reserved slots.
func (r Registry) Admit(waiting []Item, inFlight int, plan Plan) []Item {
	free := plan.Budget - inFlight
	if free <= 0 || len(waiting) == 0 {
		return nil
	}
	// Never reserve the whole budget, or lower tiers could never run.
	reserved := plan.Reserved
	if max := plan.Budget - 1; reserved > max {
		reserved = max
	}
	if reserved < 0 {
		reserved = 0
	}

	sorted := byKey(waiting)
	topWaiting := 0
	for _, it := range sorted {
		if r.Get(it.Level).Reserved {
			topWaiting++
		}
	}

	var out []Item
	for _, it := range sorted {
		if free <= 0 {
			break
		}
		if r.Get(it.Level).Reserved {
			out = append(out, it)
			free--
			topWaiting--
			continue
		}
		// Non-top: keep up to `reserved` slots for top items still waiting.
		keep := reserved
		if topWaiting < keep {
			keep = topWaiting
		}
		if free > keep {
			out = append(out, it)
			free--
		}
	}
	return out
}

// Ahead returns how many waiting items would be dequeued before the given item —
// the "N ahead of you in the queue" number. It counts waiting items with a smaller
// scheduling key (ties broken by lower id), which is the queue order Admit follows.
func Ahead(item Item, waiting []Item) int {
	n := 0
	for _, w := range waiting {
		if w.ID == item.ID {
			continue
		}
		if w.SchedKey < item.SchedKey || (w.SchedKey == item.SchedKey && w.ID < item.ID) {
			n++
		}
	}
	return n
}
