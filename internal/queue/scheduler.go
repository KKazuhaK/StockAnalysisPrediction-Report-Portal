package queue

import "sort"

// Item is the minimal unit the scheduler orders: an id, its multifactor priority
// score (higher = admitted sooner), and whether it is an 加急 run (which gets a
// reserved slot). internal/app computes Score from the weighted factors; the queue
// package only orders and admits. See docs/adr/0008-multifactor-priority.md.
type Item struct {
	ID     int64
	Score  float64
	Urgent bool
}

// Plan is the scheduler's capacity configuration.
type Plan struct {
	Budget   int // total concurrent runs allowed across the whole queue
	Reserved int // slots held for 加急 runs; clamped to [0, Budget-1]
}

// byScore orders items by score descending, breaking ties by id (older/lower id
// first) so the order is deterministic and FIFO within an equal score.
func byScore(items []Item) []Item {
	out := make([]Item, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Admit selects, from the waiting items, which to start now given how many runs are
// already in flight and the plan. It is non-preemptive: it only ever returns waiting
// items and never touches running ones.
//
// Items are considered highest-score first. A reserved slot is held for 加急 (Urgent)
// items while any is still waiting, so a fresh 加急 always has a lane even behind a
// backlog — and the lower-priority items still drain through the non-reserved slots,
// so they don't starve. When no 加急 waits, they borrow the reserved slots.
func Admit(waiting []Item, inFlight int, plan Plan) []Item {
	free := plan.Budget - inFlight
	if free <= 0 || len(waiting) == 0 {
		return nil
	}
	// Never reserve the whole budget, or non-urgent runs could never start.
	reserved := plan.Reserved
	if max := plan.Budget - 1; reserved > max {
		reserved = max
	}
	if reserved < 0 {
		reserved = 0
	}

	sorted := byScore(waiting)
	urgentWaiting := 0
	for _, it := range sorted {
		if it.Urgent {
			urgentWaiting++
		}
	}

	var out []Item
	for _, it := range sorted {
		if free <= 0 {
			break
		}
		if it.Urgent {
			out = append(out, it)
			free--
			urgentWaiting--
			continue
		}
		// Non-urgent: keep up to `reserved` slots for 加急 runs still waiting.
		keep := reserved
		if urgentWaiting < keep {
			keep = urgentWaiting
		}
		if free > keep {
			out = append(out, it)
			free--
		}
	}
	return out
}

// Ahead returns how many waiting items would be dequeued before the given item — the
// "N ahead of you in the queue" number. It counts waiting items that rank higher
// (greater score, ties broken by lower id), which is the order Admit follows.
func Ahead(item Item, waiting []Item) int {
	n := 0
	for _, w := range waiting {
		if w.ID == item.ID {
			continue
		}
		if w.Score > item.Score || (w.Score == item.Score && w.ID < item.ID) {
			n++
		}
	}
	return n
}
