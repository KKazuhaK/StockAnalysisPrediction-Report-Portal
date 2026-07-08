package app

import "testing"

// parsePriority reads a stored priority string as (base, urgent), mapping the legacy
// tier names and clamping numbers (docs/adr/0008-multifactor-priority.md).
func TestParsePriority(t *testing.T) {
	cases := []struct {
		in   string
		base int
		urg  bool
	}{
		{"urgent", 0, true},
		{"normal", 50, false}, // legacy tier
		{"other", 20, false},  // legacy tier
		{"", 50, false},       // empty → default base
		{"75", 75, false},
		{"150", 100, false}, // clamped high
		{"-5", 0, false},    // clamped low
		{"bogus", 50, false},
	}
	for _, c := range cases {
		if b, u := parsePriority(c.in); b != c.base || u != c.urg {
			t.Errorf("parsePriority(%q) = (%d,%v), want (%d,%v)", c.in, b, u, c.base, c.urg)
		}
	}
}

// resolveBasePriority precedence (group model B): the primary group's priority
// override beats the system default; an empty setup falls back to the built-in 50.
func TestResolveBasePriority(t *testing.T) {
	st := newTestStore(t)
	srv := &Server{st: st}
	// The primary group is a column on the user row now (ADR 0013), so the user must exist
	// before a group can be assigned to it — real callers always upsert first.
	st.UpsertUser(User{Username: "u"})

	// No primary group, no setting → built-in default 50.
	if p := srv.resolveBasePriority("u"); p != 50 {
		t.Fatalf("no-group default = %d, want 50", p)
	}
	// The admin system-default setting is honoured.
	st.SetSetting("run_default_priority", "30")
	if p := srv.resolveBasePriority("u"); p != 30 {
		t.Fatalf("system default = %d, want 30", p)
	}
	// The primary group's priority override beats the system default.
	gLow, _ := st.CreateUserGroup("low", "", 0)
	gHigh, _ := st.CreateUserGroup("high", "", 0)
	st.SetGroupPriority(gLow, srv.groupPriorityValid("40"))
	st.SetGroupPriority(gHigh, srv.groupPriorityValid("80"))
	st.SetPrimaryGroup("u", gHigh)
	if p := srv.resolveBasePriority("u"); p != 80 {
		t.Fatalf("primary-group override = %d, want 80", p)
	}
	// Only the primary group counts — switching primary switches the override.
	st.SetPrimaryGroup("u", gLow)
	if p := srv.resolveBasePriority("u"); p != 40 {
		t.Fatalf("after switch = %d, want 40 (the new primary's override)", p)
	}
	// A primary group with no override inherits the system default.
	gPlain, _ := st.CreateUserGroup("plain", "", 0)
	st.SetPrimaryGroup("u", gPlain)
	if p := srv.resolveBasePriority("u"); p != 30 {
		t.Fatalf("no-override primary = %d, want 30 (system default)", p)
	}

	// The Default group carries no priority override even if one is set on it: a user
	// whose primary IS the Default group still uses the system default (symmetry with
	// unassigned users, whose priority also comes from the system default).
	def := st.DefaultGroupID()
	st.SetGroupPriority(def, "90")
	st.SetPrimaryGroup("u", def)
	if p := srv.resolveBasePriority("u"); p != 30 {
		t.Fatalf("default-group primary = %d, want 30 (Default has no priority override)", p)
	}
}

// groupPriorityValid normalizes a group default to a base-number string; 加急 and
// garbage are rejected, legacy tiers map, numbers clamp.
func TestGroupPriorityValid(t *testing.T) {
	srv := &Server{}
	for in, want := range map[string]string{
		"normal": "50", // legacy tier → number
		"other":  "20",
		"75":     "75",
		"150":    "100", // clamped
		"-5":     "0",   // clamped
		"urgent": "",    // 加急 can never be a silent default
		"bogus":  "",    // non-numeric → rejected
		"":       "",    // empty stays empty
	} {
		if got := srv.groupPriorityValid(in); got != want {
			t.Errorf("groupPriorityValid(%q) = %q, want %q", in, got, want)
		}
	}
}

// normalizePriorityInput accepts 加急 or a clamped number, and rejects garbage.
func TestNormalizePriorityInput(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"urgent", "urgent", true},
		{"60", "60", true},
		{"200", "100", true}, // clamped
		{"normal", "", false},
		{"", "", false},
		{"abc", "", false},
	}
	for _, c := range cases {
		if got, ok := normalizePriorityInput(c.in); got != c.want || ok != c.ok {
			t.Errorf("normalizePriorityInput(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
