package app

import (
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// EffectiveGroupSettings layers permissive baseline < Default group < primary group.
func TestGroupGovernanceResolution(t *testing.T) {
	st := newTestStore(t)
	mkUser(t, st, "u", "user")
	def := st.DefaultGroupID()

	// Fresh Default has NULL governance → permissive baseline.
	if gs := st.EffectiveGroupSettings("u"); !gs.AllowUrgent || gs.MaxQueued != 0 || gs.RunWindow != "" {
		t.Fatalf("baseline = %+v, want permissive", gs)
	}

	// Default overrides: disallow urgent, cap 2, window 9-18.
	no, cap2, win := false, 2, "9-18"
	st.SetGroupGovernance(def, &no, &cap2, &win)
	if gs := st.EffectiveGroupSettings("u"); gs.AllowUrgent || gs.MaxQueued != 2 || gs.RunWindow != "9-18" {
		t.Fatalf("default override = %+v", gs)
	}

	// A purely-inheriting primary group keeps the Default's governance.
	g, _ := st.CreateUserGroup("G", "", 0)
	st.SetGroupGovernance(g, nil, nil, nil)
	st.SetPrimaryGroup("u", g)
	if gs := st.EffectiveGroupSettings("u"); gs.AllowUrgent || gs.MaxQueued != 2 || gs.RunWindow != "9-18" {
		t.Fatalf("inheriting primary = %+v, want Default's governance", gs)
	}

	// Primary overrides win per field.
	yes, cap5, none := true, 5, ""
	st.SetGroupGovernance(g, &yes, &cap5, &none)
	if gs := st.EffectiveGroupSettings("u"); !gs.AllowUrgent || gs.MaxQueued != 5 || gs.RunWindow != "" {
		t.Fatalf("primary override = %+v", gs)
	}
}

func TestParseRunWindow(t *testing.T) {
	cases := []struct {
		in   string
		a, b int
		ok   bool
	}{
		{"", 0, 0, false},
		{"9-18", 9, 18, true},
		{"22-6", 22, 6, true}, // wraps midnight
		{"9-9", 0, 0, false},  // degenerate
		{"x-9", 0, 0, false},
		{"9", 0, 0, false},
		{"25-9", 0, 0, false},
	}
	for _, c := range cases {
		a, b, ok := parseRunWindow(c.in)
		if ok != c.ok || (ok && (a != c.a || b != c.b)) {
			t.Errorf("parseRunWindow(%q) = (%d,%d,%v), want (%d,%d,%v)", c.in, a, b, ok, c.a, c.b, c.ok)
		}
	}
}

func TestActiveJobCount(t *testing.T) {
	st := newTestStore(t)
	if st.ActiveJobCount("u") != 0 {
		t.Fatal("no jobs → 0")
	}
	st.CreateBatchJob(1, 1, 0, "u", []map[string]string{{"symbol": "x"}}, "50")
	st.CreateBatchJob(1, 1, 0, "u", []map[string]string{{"symbol": "y"}}, "50")
	st.CreateBatchJob(1, 1, 0, "other", []map[string]string{{"symbol": "z"}}, "50")
	if n := st.ActiveJobCount("u"); n != 2 {
		t.Fatalf("ActiveJobCount(u) = %d, want 2 (queued jobs, excludes other user)", n)
	}
}

// A group that disallows urgent downgrades an urgent submit even with the lane enabled.
func TestUrgentAllowedGroupDisallow(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "x"}}
	s.st.SetSetting("batch_urgent_enabled", "1")
	s.st.UpsertUser(User{Username: "u", PasswordHash: "x", Role: "user"})

	no := false
	s.st.SetGroupGovernance(s.st.DefaultGroupID(), &no, nil, nil) // Default disallows urgent
	if p, d := s.urgentAllowed("u", "urgent", 50); p != "50" || !d {
		t.Fatalf("group-disallow urgent = %q downgraded=%v, want 50/true", p, d)
	}
	// Re-allow on the Default → urgent works again (unlimited group grants it freely).
	yes := true
	s.st.SetGroupGovernance(s.st.DefaultGroupID(), &yes, nil, nil)
	free, _ := s.st.CreateUserGroup("Free", "", 0, true)
	s.st.SetPrimaryGroup("u", free)
	if p, d := s.urgentAllowed("u", "urgent", 50); p != "urgent" || d {
		t.Fatalf("re-allowed urgent = %q downgraded=%v, want urgent/false", p, d)
	}
}
