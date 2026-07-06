package app

import (
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The submit-time policy: users spend 加急 tickets unless one of their groups grants
// unlimited urgent runs; admins do not get a role-based exemption.
func TestUrgentAllowedSpendsTickets(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "x"}}
	s.st.SetSetting("batch_urgent_enabled", "1") // ticket mechanics only apply when the 加急 lane is on
	s.st.UpsertUser(User{Username: "admin", PasswordHash: "x", Role: "admin"})
	s.st.UpsertUser(User{Username: "op", PasswordHash: "x", Role: "operator"})
	g, _ := s.st.CreateUserGroup("G", "", 2)
	s.st.SetUserGroups("op", []int64{g})

	// Admin with no tickets/unlimited group is limited like any other user.
	if p, d := s.urgentAllowed("admin", "urgent", 50); p != "50" || !d {
		t.Fatalf("admin urgent without group = %q downgraded=%v, want 50/true", p, d)
	}

	// Operator: two urgent runs succeed, the third falls back to its base priority.
	for i := 1; i <= 2; i++ {
		if p, d := s.urgentAllowed("op", "urgent", 50); p != "urgent" || d {
			t.Fatalf("op urgent #%d = %q downgraded=%v, want urgent/false", i, p, d)
		}
	}
	if p, d := s.urgentAllowed("op", "urgent", 50); p != "50" || !d {
		t.Fatalf("op urgent #3 = %q downgraded=%v, want 50/true", p, d)
	}
	// A non-urgent priority is never charged and passes through unchanged.
	if p, d := s.urgentAllowed("op", "50", 50); p != "50" || d {
		t.Fatalf("op base = %q downgraded=%v, want 50/false", p, d)
	}

	free, _ := s.st.CreateUserGroup("Unlimited", "", 0, true)
	s.st.SetUserGroups("admin", []int64{free})
	if p, d := s.urgentAllowed("admin", "urgent", 50); p != "urgent" || d {
		t.Fatalf("admin urgent from unlimited group = %q downgraded=%v, want urgent/false", p, d)
	}
}

// With the 加急 lane off (the default), any urgent submit downgrades to its base
// priority — even an admin's — while a non-urgent priority passes through unchanged.
// Turning the lane on still requires a ticket or an unlimited group.
func TestUrgentDisabledDowngrades(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "x"}}
	s.st.UpsertUser(User{Username: "admin", PasswordHash: "x", Role: "admin"})

	if s.urgentEnabled() {
		t.Fatal("加急 should be off by default")
	}
	if p, d := s.urgentAllowed("admin", "urgent", 40); p != "40" || !d {
		t.Fatalf("disabled admin urgent = %q downgraded=%v, want 40/true", p, d)
	}
	if p, d := s.urgentAllowed("op", "urgent", 20); p != "20" || !d {
		t.Fatalf("disabled op urgent = %q downgraded=%v, want 20/true", p, d)
	}
	if p, d := s.urgentAllowed("op", "70", 50); p != "70" || d {
		t.Fatalf("non-urgent = %q downgraded=%v, want 70/false", p, d)
	}

	s.st.SetSetting("batch_urgent_enabled", "1")
	if p, d := s.urgentAllowed("admin", "urgent", 40); p != "40" || !d {
		t.Fatalf("enabled admin urgent without group = %q downgraded=%v, want 40/true", p, d)
	}
	free, _ := s.st.CreateUserGroup("Unlimited", "", 0, true)
	s.st.SetUserGroups("admin", []int64{free})
	if p, d := s.urgentAllowed("admin", "urgent", 40); p != "urgent" || d {
		t.Fatalf("enabled admin urgent from unlimited group = %q downgraded=%v, want urgent/false", p, d)
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
	if st.UserUrgentUnlimited("op") {
		t.Fatal("user without an unlimited group should not be unlimited")
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
	if st.UserUrgentUnlimited("op") {
		t.Fatal("weighted groups should not imply unlimited urgent")
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

	g3, _ := st.CreateUserGroup("Unlimited", "", 0, true)
	st.SetUserGroups("op", []int64{g1, g3})
	if !st.UserUrgentUnlimited("op") {
		t.Fatal("unlimited group did not grant unlimited urgent")
	}
}
