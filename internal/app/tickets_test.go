package app

import (
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The submit-time policy: admins get 加急 free; others spend a ticket and are
// downgraded to 普通 once their allowance runs out; 普通 never spends.
func TestUrgentAllowedSpendsTickets(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "x"}}
	s.st.UpsertUser(User{Username: "admin", PasswordHash: "x", Role: "admin"})
	s.st.UpsertUser(User{Username: "op", PasswordHash: "x", Role: "operator"})
	g, _ := s.st.CreateUserGroup("G", "", 2)
	s.st.SetUserGroups("op", []int64{g})

	// Admin: unlimited urgent, no ticket spent.
	if p, d := s.urgentAllowed("admin", "urgent"); p != "urgent" || d {
		t.Fatalf("admin urgent = %q downgraded=%v, want urgent/false", p, d)
	}

	// Operator: two urgent runs succeed, the third downgrades to normal.
	for i := 1; i <= 2; i++ {
		if p, d := s.urgentAllowed("op", "urgent"); p != "urgent" || d {
			t.Fatalf("op urgent #%d = %q downgraded=%v, want urgent/false", i, p, d)
		}
	}
	if p, d := s.urgentAllowed("op", "urgent"); p != "normal" || !d {
		t.Fatalf("op urgent #3 = %q downgraded=%v, want normal/true", p, d)
	}
	// 普通 is never charged.
	if p, d := s.urgentAllowed("op", "normal"); p != "normal" || d {
		t.Fatalf("op normal = %q downgraded=%v, want normal/false", p, d)
	}
}

// The pure refill policy: a full allocation on first use, no change within a
// period, and a reset (not accumulation) after one or more whole periods elapse.
func TestTicketRefill(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 9, 0, 0, 0, time.Local)
	week := 7

	// First use → full allocation, period anchored at now.
	rem, ps := ticketRefill(0, time.Time{}, 5, week, t0)
	if rem != 5 || !ps.Equal(t0) {
		t.Fatalf("first use = %d @ %v, want 5 @ t0", rem, ps)
	}
	// Mid-period (3 days later) → unchanged.
	rem, ps = ticketRefill(2, t0, 5, week, t0.AddDate(0, 0, 3))
	if rem != 2 || !ps.Equal(t0) {
		t.Fatalf("mid-period = %d @ %v, want 2 @ t0 (no refill)", rem, ps)
	}
	// After exactly one period → reset to allocation, cadence preserved.
	rem, ps = ticketRefill(0, t0, 5, week, t0.AddDate(0, 0, 7))
	if rem != 5 || !ps.Equal(t0.AddDate(0, 0, 7)) {
		t.Fatalf("one period later = %d @ %v, want 5 @ t0+7d", rem, ps)
	}
	// After two-plus periods → still just a reset (no accumulation) and the anchor
	// advances by whole periods.
	rem, ps = ticketRefill(1, t0, 5, week, t0.AddDate(0, 0, 16))
	if rem != 5 || !ps.Equal(t0.AddDate(0, 0, 14)) {
		t.Fatalf("two periods later = %d @ %v, want 5 @ t0+14d", rem, ps)
	}
	// Zero-length period (misconfig) never refills.
	if r, _ := ticketRefill(1, t0, 5, 0, t0.AddDate(0, 0, 30)); r != 1 {
		t.Fatalf("zero period refilled to %d, want 1", r)
	}
}

func TestTicketSpendAndAllocation(t *testing.T) {
	st := newTestStore(t)
	mkUser(t, st, "op", "operator")
	now := time.Date(2026, 7, 1, 9, 0, 0, 0, time.Local)

	// No weighted group → allocation 0, cannot spend.
	if a := st.UserTicketAllocation("op"); a != 0 {
		t.Fatalf("allocation with no group = %d, want 0", a)
	}
	if ok, _ := st.SpendTicket("op", 0, 7, now); ok {
		t.Fatal("spent a ticket with zero allocation")
	}

	// Join a weight-3 group; allocation = 3 (max across groups).
	g1, _ := st.CreateUserGroup("A", "", 3)
	g2, _ := st.CreateUserGroup("B", "", 1)
	st.SetUserGroups("op", []int64{g1, g2})
	if a := st.UserTicketAllocation("op"); a != 3 {
		t.Fatalf("allocation = %d, want 3 (max weight)", a)
	}

	alloc := st.UserTicketAllocation("op")
	// Spend all three, then the fourth fails.
	for i := 3; i >= 1; i-- {
		ok, left := st.SpendTicket("op", alloc, 7, now)
		if !ok || left != i-1 {
			t.Fatalf("spend #%d = ok:%v left:%d", 4-i, ok, left)
		}
	}
	if ok, _ := st.SpendTicket("op", alloc, 7, now); ok {
		t.Fatal("spent a 4th ticket with allocation 3")
	}
	if st.TicketStatus("op", alloc, 7, now) != 0 {
		t.Fatal("status should be 0 after spending all")
	}

	// Next week refills back to the full allocation.
	next := now.AddDate(0, 0, 8)
	if st.TicketStatus("op", alloc, 7, next) != 3 {
		t.Fatal("tickets did not refill next period")
	}
	if ok, left := st.SpendTicket("op", alloc, 7, next); !ok || left != 2 {
		t.Fatalf("spend after refill = ok:%v left:%d, want ok/2", ok, left)
	}
}
