package queue

import (
	"reflect"
	"testing"
)

func ids(items []Item) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// Score combines the weighted factors; a higher factor sum scores higher, and a
// bare 加急 run outranks the best-scoring non-urgent one.
func TestScoreCombinesFactorsAndUrgentDominates(t *testing.T) {
	w := Weights{Base: 1000, Age: 1000, Fair: 1000}
	low := w.Score(Factors{Base: 0.1, Age: 0, Fair: 0.1})
	high := w.Score(Factors{Base: 0.9, Age: 1, Fair: 0.9})
	if high <= low {
		t.Fatalf("higher factors should score higher: high=%v low=%v", high, low)
	}
	if urgentMin := w.Score(Factors{Urgent: true}); urgentMin <= high {
		t.Fatalf("a bare 加急 (%v) must outrank the best non-urgent (%v)", urgentMin, high)
	}
}

// An idle run sinks below every non-idle run: even a maxed-out idle score stays under
// a zero-factor normal run, while 加急 still dominates it. Among idle runs the base/
// age/fair terms still order them (an orderly FIFO-ish backlog). Symmetric to urgent.
func TestScoreIdleSinksBelowEverything(t *testing.T) {
	w := Weights{Base: 1000, Age: 1000, Fair: 1000}
	idleMax := w.Score(Factors{Base: 1, Age: 1, Fair: 1, Idle: true}) // best possible idle
	normalFloor := w.Score(Factors{})                                 // worst possible non-idle (all zero)
	if idleMax >= normalFloor {
		t.Fatalf("a maxed-out idle run (%v) must still sink below a zero non-idle run (%v)", idleMax, normalFloor)
	}
	if urgent := w.Score(Factors{Urgent: true}); urgent <= idleMax {
		t.Fatalf("加急 (%v) must outrank idle (%v)", urgent, idleMax)
	}
	if idleHigh, idleLow := w.Score(Factors{Age: 1, Idle: true}), w.Score(Factors{Age: 0, Idle: true}); idleHigh <= idleLow {
		t.Fatalf("idle runs still order among themselves by factors: high=%v low=%v", idleHigh, idleLow)
	}
}

// In Admit an idle run takes a slot only after every non-idle waiting run — when a
// normal run also waits it wins the free slot (the run-when-queue-idle contract).
func TestAdmitIdleRunsLast(t *testing.T) {
	w := Weights{Base: 1000, Age: 1000, Fair: 1000}
	waiting := []Item{
		{ID: 1, Score: w.Score(Factors{Idle: true})},
		{ID: 2, Score: w.Score(Factors{Base: 0.01})}, // a barely-there normal run
	}
	if got := Admit(waiting, 0, Plan{Budget: 1}); len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("a normal run must beat an idle run for the last slot, got %v", ids(got))
	}
	// Nothing non-idle waiting → the idle run finally takes the free slot.
	if got := Admit(waiting[:1], 0, Plan{Budget: 1}); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("with only idle waiting it should take the free slot, got %v", ids(got))
	}
}

// Admit takes the highest-scoring items first, up to the free budget.
func TestAdmitHighestScoreFirst(t *testing.T) {
	waiting := []Item{{ID: 1, Score: 5}, {ID: 2, Score: 20}, {ID: 3, Score: 12}}
	got := Admit(waiting, 0, Plan{Budget: 2, Reserved: 0})
	if !reflect.DeepEqual(ids(got), []int64{2, 3}) {
		t.Fatalf("admit order = %v, want [2 3] (highest score first)", ids(got))
	}
	// Full budget already in flight → admit nothing.
	if got := Admit(waiting, 2, Plan{Budget: 2}); len(got) != 0 {
		t.Fatalf("no free slots should admit nothing, got %v", ids(got))
	}
}

// Equal scores break the tie by lower id (FIFO within a score).
func TestAdmitTieBreakByID(t *testing.T) {
	waiting := []Item{{ID: 9, Score: 10}, {ID: 3, Score: 10}, {ID: 7, Score: 10}}
	got := Admit(waiting, 0, Plan{Budget: 1})
	if len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("tie should pick the lowest id, got %v", ids(got))
	}
}

// A reserved slot keeps a lane for 加急: with budget 2 / reserved 1, a waiting urgent
// plus lower-scoring non-urgents admits the urgent and only fills the remaining slot.
func TestAdmitReservedSlotHoldsUrgentLane(t *testing.T) {
	waiting := []Item{
		{ID: 1, Score: 6},
		{ID: 2, Score: 5},
		{ID: 3, Score: urgentBoost, Urgent: true},
	}
	got := Admit(waiting, 0, Plan{Budget: 2, Reserved: 1})
	if !reflect.DeepEqual(ids(got), []int64{3, 1}) {
		t.Fatalf("reserved-slot admit = %v, want [3 1]", ids(got))
	}
}

// With no urgent waiting, non-urgent runs borrow the reserved slot (full utilisation).
func TestAdmitBorrowsReservedWhenNoUrgentWaits(t *testing.T) {
	waiting := []Item{
		{ID: 1, Score: 30},
		{ID: 2, Score: 20},
		{ID: 3, Score: 25},
	}
	got := Admit(waiting, 0, Plan{Budget: 3, Reserved: 1})
	if len(got) != 3 {
		t.Fatalf("with no urgent waiting all 3 slots should fill, got %v", ids(got))
	}
}

// Reserved is clamped so it can never consume the entire budget.
func TestAdmitReservedClamped(t *testing.T) {
	waiting := []Item{{ID: 1, Score: 10}}
	// Budget 1, Reserved 1 → clamp to 0, so the lone non-urgent still runs.
	got := Admit(waiting, 0, Plan{Budget: 1, Reserved: 1})
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("reserved must clamp to budget-1; got %v", ids(got))
	}
}

// Ahead counts the items that rank higher (greater score, ties by lower id).
func TestAhead(t *testing.T) {
	waiting := []Item{
		{ID: 1, Score: 300},
		{ID: 2, Score: 900},
		{ID: 3, Score: 500},
		{ID: 4, Score: 300}, // tie with id 1, lower id ranks first
	}
	// id 1 (300): ahead = id2(900), id3(500) → 2 (id4 ties but higher id).
	if n := Ahead(Item{ID: 1, Score: 300}, waiting); n != 2 {
		t.Fatalf("Ahead(id1) = %d, want 2", n)
	}
	// id 2 (900, highest) → 0 ahead.
	if n := Ahead(Item{ID: 2, Score: 900}, waiting); n != 0 {
		t.Fatalf("Ahead(id2) = %d, want 0", n)
	}
	// id 4 (300): ahead = id2, id3, and id1 (same score, lower id) → 3.
	if n := Ahead(Item{ID: 4, Score: 300}, waiting); n != 3 {
		t.Fatalf("Ahead(id4) = %d, want 3", n)
	}
}
