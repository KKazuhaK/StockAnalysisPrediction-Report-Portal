// Package ssorules decides what role and organizational unit an SSO identity gets.
//
// It is deliberately a package of its own, importing neither net/http nor any SAML/OIDC type. Two
// reasons. First, the SAML ACS, the OIDC callback and (later) a SCIM sync job must all reach the
// same answer, and the cheapest way to guarantee that is one pure function they all call. Second,
// this is the code that decides a tenancy boundary — in this portal the OU carries report
// visibility, the run allow-list and the daily quota (ADR 0022) — so it must be exhaustively
// table-testable with no HTTP, no XML and no network in the way.
//
// Everything here is side-effect free and idempotent: Resolve reports what SHOULD be true, and the
// caller applies the difference.
package ssorules

import "strings"

// Rule is one ordered mapping from an IdP attribute value to a role and/or an OU. An empty Attr
// means "match against the groups attribute", which is what the admin UI presents as the default.
type Rule struct {
	ID              int64
	Ord             int
	Enabled         bool
	Attr            string
	Value           string
	TargetRole      string // "" = do not change the role
	TargetGroup     int64  // 0 = do not change the OU
	KeepOnMiss      bool
	CaseInsensitive bool
	Note            string
}

// Facts is what an authenticated identity asserted, normalized away from protocol specifics.
type Facts struct {
	Groups []string
	Attrs  map[string][]string
}

// Current is the local account as it stands, if there is one.
type Current struct {
	Exists bool
	Role   string
	Group  int64
}

// Defaults are the provider-level fallbacks and guards.
type Defaults struct {
	Role            string
	Group           int64           // 0 = no default OU configured
	AllowAdminRole  bool            // whether this provider may grant a privileged role at all
	PrivilegedRoles map[string]bool // roles that confer admin rights, supplied by the caller
}

// Outcome is the assignment to apply. Deny means the login must be refused: it is reached only when
// nothing matched and there is no safe place to put a brand-new account, and it is deliberately
// preferred over falling through to an implicit default that could be the unrestricted root OU.
type Outcome struct {
	Role         string
	Group        int64
	Matched      bool
	RuleID       int64
	Deny         bool
	DenyReason   string
	AdminBlocked bool // a rule tried to grant a privileged role without the provider opt-in
}

// Resolve applies the first matching enabled rule, in Ord order.
func Resolve(rules []Rule, f Facts, cur Current, d Defaults) Outcome {
	if m, ok := firstMatch(rules, f); ok {
		return apply(m, cur, d)
	}
	// Nothing matched. An existing account with a keep-on-miss rule in play is left exactly as it
	// is, so a transient attribute change (or an IdP that stopped sending groups) cannot quietly
	// strip someone's access — or quietly widen it.
	if cur.Exists && anyKeepOnMiss(rules) {
		return Outcome{Role: cur.Role, Group: cur.Group}
	}
	if cur.Exists {
		return withDefaults(Outcome{}, cur, d)
	}
	// A brand-new account with nowhere defined to put it is refused rather than placed somewhere
	// arbitrary: in this portal the OU IS the permission boundary.
	if d.Group == 0 {
		return Outcome{Deny: true, DenyReason: "no rule matched and this provider has no default group"}
	}
	return Outcome{Role: d.Role, Group: d.Group}
}

// apply turns a matched rule into an assignment, filling anything the rule left unset from the
// current account (if any) and then the provider defaults, and enforcing the admin-elevation guard.
func apply(r Rule, cur Current, d Defaults) Outcome {
	out := Outcome{Matched: true, RuleID: r.ID, Role: r.TargetRole, Group: r.TargetGroup}
	// A rule may only elevate to a privileged role when the provider explicitly allows it. The
	// elevation is dropped rather than denying the login outright — a misconfigured rule should
	// under-grant, never lock the user out — and is reported so the caller can log it.
	if out.Role != "" && d.PrivilegedRoles[out.Role] && !d.AllowAdminRole {
		out.Role, out.AdminBlocked = "", true
	}
	return withDefaults(out, cur, d)
}

// withDefaults fills unset fields: the account's current value first (so a rule that speaks only
// about the OU cannot silently reset a role an admin set by hand), then the provider default.
func withDefaults(out Outcome, cur Current, d Defaults) Outcome {
	if out.Role == "" && cur.Exists {
		out.Role = cur.Role
	}
	if out.Role == "" {
		out.Role = d.Role
	}
	if out.Group == 0 && cur.Exists {
		out.Group = cur.Group
	}
	if out.Group == 0 {
		out.Group = d.Group
	}
	return out
}

func firstMatch(rules []Rule, f Facts) (Rule, bool) {
	best, found := Rule{}, false
	for _, r := range rules {
		if !r.Enabled || !matches(r, f) {
			continue
		}
		// Scan rather than sort: the caller's slice order is not guaranteed, and Ord is the
		// contract. Ties fall to the lower ID so the result is deterministic.
		if !found || r.Ord < best.Ord || (r.Ord == best.Ord && r.ID < best.ID) {
			best, found = r, true
		}
	}
	return best, found
}

// matches reports whether a rule's value appears in the attribute it names (or in the groups list
// when it names none). Comparison is exact — never a substring — because these values gate a
// tenancy boundary, and case-sensitive unless the rule opts out, because group values are often
// GUIDs or distinguished names.
func matches(r Rule, f Facts) bool {
	for _, v := range values(r.Attr, f) {
		if v == r.Value || (r.CaseInsensitive && strings.EqualFold(v, r.Value)) {
			return true
		}
	}
	return false
}

func values(attr string, f Facts) []string {
	if attr == "" {
		return f.Groups
	}
	return f.Attrs[attr]
}

func anyKeepOnMiss(rules []Rule) bool {
	for _, r := range rules {
		if r.Enabled && r.KeepOnMiss {
			return true
		}
	}
	return false
}

// Shadowed returns the ids of enabled rules that can never win because an earlier enabled rule
// already matches the same attribute and value. It backs an admin-UI warning: a silently
// unreachable rule looks like a granted permission that never applies.
func Shadowed(rules []Rule) []int64 {
	ordered := append([]Rule(nil), rules...)
	for i := 1; i < len(ordered); i++ { // insertion sort by (Ord, ID); rule lists are tiny
		for j := i; j > 0 && less(ordered[j], ordered[j-1]); j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	seen := map[string]bool{}
	var out []int64
	for _, r := range ordered {
		if !r.Enabled {
			continue
		}
		key := r.Attr + "\x00" + r.Value
		if seen[key] {
			out = append(out, r.ID)
			continue
		}
		seen[key] = true
	}
	return out
}

func less(a, b Rule) bool { return a.Ord < b.Ord || (a.Ord == b.Ord && a.ID < b.ID) }
