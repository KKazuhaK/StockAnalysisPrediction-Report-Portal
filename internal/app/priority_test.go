package app

import "testing"

// resolvePriority precedence: explicit > highest group default > system default.
// (docs/adr/0007-run-analysis-and-scheduling.md)
func TestResolvePriority(t *testing.T) {
	st := newTestStore(t)
	srv := &Server{st: st}

	// No group, no setting → system default "normal".
	if p := srv.resolvePriority("u", ""); p != "normal" {
		t.Fatalf("no-group default = %q, want normal", p)
	}
	// An explicit choice always wins (ticket gating happens later).
	if p := srv.resolvePriority("u", "urgent"); p != "urgent" {
		t.Fatalf("explicit = %q, want urgent", p)
	}
	// The admin system-default setting is honoured.
	st.SetSetting("run_default_priority", "other")
	if p := srv.resolvePriority("u", ""); p != "other" {
		t.Fatalf("system default = %q, want other", p)
	}
	// Group default beats system default; across groups the highest wins.
	gLow, _ := st.CreateUserGroup("low", "", 0)
	gHigh, _ := st.CreateUserGroup("high", "", 0)
	st.SetGroupPriority(gLow, "other")   // lower priority
	st.SetGroupPriority(gHigh, "normal") // higher priority (lower Value)
	st.SetUserGroups("u", []int64{gLow, gHigh})
	if p := srv.resolvePriority("u", ""); p != "normal" {
		t.Fatalf("group max = %q, want normal (highest across the user's groups)", p)
	}
}

// A group default may only be a registered, non-reserved tier — never 加急.
func TestGroupPriorityValid(t *testing.T) {
	srv := &Server{}
	for in, want := range map[string]string{
		"normal": "normal",
		"other":  "other",
		"urgent": "", // reserved → rejected (stays ticket-gated)
		"bogus":  "", // unknown → rejected
		"":       "", // empty stays empty
	} {
		if got := srv.groupPriorityValid(in); got != want {
			t.Errorf("groupPriorityValid(%q) = %q, want %q", in, got, want)
		}
	}
}
