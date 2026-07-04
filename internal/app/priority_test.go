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

// resolveBasePriority precedence: highest group default > system default; an empty
// setup falls back to the built-in default (50).
func TestResolveBasePriority(t *testing.T) {
	st := newTestStore(t)
	srv := &Server{st: st}

	// No group, no setting → built-in default 50.
	if p := srv.resolveBasePriority("u"); p != 50 {
		t.Fatalf("no-group default = %d, want 50", p)
	}
	// The admin system-default setting is honoured.
	st.SetSetting("run_default_priority", "30")
	if p := srv.resolveBasePriority("u"); p != 30 {
		t.Fatalf("system default = %d, want 30", p)
	}
	// Group default beats system default; across groups the highest wins.
	gLow, _ := st.CreateUserGroup("low", "", 0)
	gHigh, _ := st.CreateUserGroup("high", "", 0)
	st.SetGroupPriority(gLow, srv.groupPriorityValid("40"))
	st.SetGroupPriority(gHigh, srv.groupPriorityValid("80"))
	st.SetUserGroups("u", []int64{gLow, gHigh})
	if p := srv.resolveBasePriority("u"); p != 80 {
		t.Fatalf("group max = %d, want 80 (highest across the user's groups)", p)
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
