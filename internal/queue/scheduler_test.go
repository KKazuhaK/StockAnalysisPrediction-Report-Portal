package queue

import (
	"reflect"
	"testing"
)

func TestDefaultRegistry(t *testing.T) {
	r := DefaultRegistry()
	if !r.Get("urgent").Reserved {
		t.Fatal("urgent should be the reserved (top) tier")
	}
	if r.Get("normal").Reserved || r.Get("other").Reserved {
		t.Fatal("only urgent is reserved")
	}
	// Unknown level falls back to a huge offset (dead last) and is never reserved.
	if r.Has("nope") {
		t.Fatal("nope should be unknown")
	}
	if r.Get("nope").Reserved || r.SchedKey("nope", 0) < r.SchedKey("other", 0) {
		t.Fatal("unknown level must sort last and not be reserved")
	}
}

// The aging key: a fresh urgent beats recently-queued normals, but a normal that
// has waited past its offset (30min) beats a fresh urgent.
func TestSchedKeyAging(t *testing.T) {
	r := DefaultRegistry()
	now := int64(1_000_000)

	// urgent now vs normal queued now → urgent runs first.
	if r.SchedKey("urgent", now) >= r.SchedKey("normal", now) {
		t.Fatal("fresh urgent should outrank a fresh normal")
	}
	// normal queued 20min ago vs urgent now → still within the 30min cap, urgent wins.
	if r.SchedKey("normal", now-20*60) <= r.SchedKey("urgent", now) {
		t.Fatal("a normal within its aging cap must still yield to a fresh urgent")
	}
	// normal queued 40min ago vs urgent now → past the 30min cap, normal wins.
	if r.SchedKey("normal", now-40*60) >= r.SchedKey("urgent", now) {
		t.Fatal("a normal past its aging cap must beat a fresh urgent")
	}
}

func ids(items []Item) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestAdmitOrderAndBudget(t *testing.T) {
	r := DefaultRegistry()
	waiting := []Item{
		{ID: 1, Level: "normal", SchedKey: 300},
		{ID: 2, Level: "normal", SchedKey: 100},
		{ID: 3, Level: "other", SchedKey: 200},
	}
	// Budget 2, nothing in flight, no urgent → take the two smallest keys in order.
	got := r.Admit(waiting, 0, Plan{Budget: 2, Reserved: 1})
	if !reflect.DeepEqual(ids(got), []int64{2, 3}) {
		t.Fatalf("admit order = %v, want [2 3]", ids(got))
	}
	// Full budget already in flight → admit nothing.
	if got := r.Admit(waiting, 2, Plan{Budget: 2, Reserved: 1}); len(got) != 0 {
		t.Fatalf("no free slots should admit nothing, got %v", ids(got))
	}
}

// The reserved slot holds a lane for a waiting urgent even when lower tiers have
// smaller keys — while those lower tiers still drain via the non-reserved slots.
func TestAdmitReservedSlotHoldsUrgentLane(t *testing.T) {
	r := DefaultRegistry()
	waiting := []Item{
		{ID: 1, Level: "normal", SchedKey: 100},
		{ID: 2, Level: "normal", SchedKey: 110},
		{ID: 3, Level: "urgent", SchedKey: 500}, // larger key, but reserved
	}
	got := r.Admit(waiting, 0, Plan{Budget: 2, Reserved: 1})
	// One normal (smallest key) fills the shared slot; the reserved slot goes to the
	// urgent — NOT the second normal.
	if !reflect.DeepEqual(ids(got), []int64{1, 3}) {
		t.Fatalf("reserved-slot admit = %v, want [1 3]", ids(got))
	}
}

// With no urgent waiting, lower tiers borrow the reserved slot (full utilisation).
func TestAdmitBorrowsReservedWhenNoTopWaits(t *testing.T) {
	r := DefaultRegistry()
	waiting := []Item{
		{ID: 1, Level: "normal", SchedKey: 100},
		{ID: 2, Level: "other", SchedKey: 200},
		{ID: 3, Level: "normal", SchedKey: 150},
	}
	got := r.Admit(waiting, 0, Plan{Budget: 3, Reserved: 1})
	if len(got) != 3 {
		t.Fatalf("with no urgent waiting all 3 slots should fill, got %v", ids(got))
	}
}

// An aged lower-tier item and a fresh urgent both run: the urgent takes its
// reserved lane, the aged item takes a shared slot — neither starves the other.
func TestAdmitAgedNormalAndUrgentCoexist(t *testing.T) {
	r := DefaultRegistry()
	waiting := []Item{
		{ID: 1, Level: "normal", SchedKey: 50}, // aged past urgent
		{ID: 2, Level: "urgent", SchedKey: 500},
		{ID: 3, Level: "normal", SchedKey: 600},
	}
	got := r.Admit(waiting, 0, Plan{Budget: 2, Reserved: 1})
	if !reflect.DeepEqual(ids(got), []int64{1, 2}) {
		t.Fatalf("aged normal + urgent = %v, want [1 2]", ids(got))
	}
}

// Reserved is clamped so it can never consume the entire budget.
func TestAdmitReservedClamped(t *testing.T) {
	r := DefaultRegistry()
	waiting := []Item{{ID: 1, Level: "normal", SchedKey: 100}}
	// Budget 1, Reserved 1 → clamp to 0, so the lone normal still runs.
	got := r.Admit(waiting, 0, Plan{Budget: 1, Reserved: 1})
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("reserved must clamp to budget-1; got %v", ids(got))
	}
}

func TestAhead(t *testing.T) {
	waiting := []Item{
		{ID: 1, Level: "normal", SchedKey: 300},
		{ID: 2, Level: "urgent", SchedKey: 100},
		{ID: 3, Level: "normal", SchedKey: 200},
		{ID: 4, Level: "normal", SchedKey: 300}, // tie with id 1, lower id first
	}
	// item id 1 (key 300): ahead = id2(100), id3(200), id4(300 & id<1? no, 4>1) → 2
	if n := Ahead(Item{ID: 1, SchedKey: 300}, waiting); n != 2 {
		t.Fatalf("Ahead(id1) = %d, want 2", n)
	}
	// item id 2 (key 100, smallest) → 0 ahead.
	if n := Ahead(Item{ID: 2, SchedKey: 100}, waiting); n != 0 {
		t.Fatalf("Ahead(id2) = %d, want 0", n)
	}
	// item id 4 (key 300): ahead = id2, id3, and id1 (same key, lower id) → 3
	if n := Ahead(Item{ID: 4, SchedKey: 300}, waiting); n != 3 {
		t.Fatalf("Ahead(id4) = %d, want 3", n)
	}
}
