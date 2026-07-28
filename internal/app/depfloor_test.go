package app

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Version floors for the two XML libraries crewjam/saml pulls in. crewjam v0.5.1's own go.mod asks
// for versions that carry known problems, and minimal version selection would happily give us those:
//
//   - russellhaering/goxmldsig v1.4.0 — CVE-2026-33487, a signature bypass in the reference-matching
//     loop, which verifies the digest against a Reference the attacker chose. This is THE library
//     that decides whether a SAML assertion is authentic.
//   - beevik/etree v1.5.0 — unbounded write recursion, reachable from attacker-supplied XML.
//
// Our go.mod requires higher versions, so MVS selects those. That is easy to undo by accident: a
// `go mod tidy` that recomputes an indirect requirement, a dependency bump, a merge that takes the
// other side of go.mod. This test makes the floor a CI failure instead of a silent downgrade, which
// is the whole reason the pins exist. govulncheck also runs in CI, but only flags a vulnerability
// whose symbols it can prove we reach — a floor is the stronger statement.
func TestSecurityCriticalDependencyFloors(t *testing.T) {
	raw, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ module, min string }{
		{"github.com/russellhaering/goxmldsig", "v1.6.0"},
		{"github.com/beevik/etree", "v1.7.0"},
	} {
		re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(want.module) + `\s+(v\S+)`)
		m := re.FindSubmatch(raw)
		if m == nil {
			t.Errorf("%s must stay pinned in go.mod — without it MVS falls back to crewjam's vulnerable version", want.module)
			continue
		}
		if got := string(m[1]); compareSemver(got, want.min) < 0 {
			t.Errorf("%s is %s, below the required floor %s", want.module, got, want.min)
		}
	}
}

// compareSemver orders two vN.N.N strings. Only enough of semver to compare release versions of two
// known modules; a pre-release suffix sorts as its numeric prefix, which is conservative here.
func compareSemver(a, b string) int {
	as, bs := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		if d := atoiPrefix(at(as, i)) - atoiPrefix(at(bs, i)); d != 0 {
			return d
		}
	}
	return 0
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "0"
}

func atoiPrefix(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
