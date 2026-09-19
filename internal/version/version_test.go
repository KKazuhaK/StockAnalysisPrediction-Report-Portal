package version

import "testing"

// Display is what a person reads; Version is what the build is. The pair has to stay distinguishable,
// so this pins the trimming rule: only a leading "v" followed by a digit is a tag to trim, and
// everything else — "dev", "ci", a diagnostic string that happens to start with v — passes through.
func TestDisplayTrimsOnlyACalVerTag(t *testing.T) {
	saved := Version
	t.Cleanup(func() { Version = saved })

	cases := []struct{ in, want string }{
		{"v2026.38.1", "2026.38.1"},
		{"v2026.38.10", "2026.38.10"},
		{"v0.4.72", "0.4.72"}, // a legacy tag is still a number
		{"dev", "dev"},
		{"ci", "ci"},
		{"unknown", "unknown"},
		{"vnext", "vnext"}, // a leading v that is not a tag
		{"v", "v"},
		{"", ""},
	}
	for _, tc := range cases {
		Version = tc.in
		if got := Display(); got != tc.want {
			t.Errorf("Display() with Version=%q = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// String is the one-line identity the startup log and `report-portal version` print, so all three
// fields have to survive in it — a build that reports its number but not its commit is a build
// nobody can pin.
func TestStringCarriesEveryField(t *testing.T) {
	saved := [3]string{Version, Commit, BuildDate}
	t.Cleanup(func() { Version, Commit, BuildDate = saved[0], saved[1], saved[2] })

	Version, Commit, BuildDate = "v2026.38.1", "abc1234", "2026-09-19T00:00:00Z"
	if got, want := String(), "2026.38.1 (abc1234, 2026-09-19T00:00:00Z)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
