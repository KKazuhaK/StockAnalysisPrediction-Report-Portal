package ssorules

import "testing"

// facts builds an identity carrying the given IdP groups.
func facts(groups ...string) Facts { return Facts{Groups: groups} }

func rule(ord int, attr, value, role string, group int64) Rule {
	return Rule{ID: int64(ord), Ord: ord, Enabled: true, Attr: attr, Value: value, TargetRole: role, TargetGroup: group}
}

// TestResolveFirstMatchWins locks the core contract: enabled rules are evaluated in ord order and
// the FIRST value match decides, even when a later rule would also match.
func TestResolveFirstMatchWins(t *testing.T) {
	rules := []Rule{
		rule(2, "", "analysts", "user", 20),
		rule(1, "", "admins", "operator", 10),
	}
	got := Resolve(rules, facts("analysts", "admins"), Current{}, Defaults{Group: 99})
	if !got.Matched || got.RuleID != 1 {
		t.Fatalf("matched rule = %d (matched %v), want the ord-1 rule", got.RuleID, got.Matched)
	}
	if got.Role != "operator" || got.Group != 10 {
		t.Errorf("outcome = role %q group %d, want operator/10", got.Role, got.Group)
	}
}

// TestResolveAssignsRoleAndOU is the reason this exists: in this codebase the OU carries the real
// permissions, so a rule must be able to set both, and either alone must not clobber the other.
func TestResolveAssignsRoleAndOU(t *testing.T) {
	// Role only: the OU falls back to the provider default rather than being zeroed.
	got := Resolve([]Rule{rule(1, "", "g", "operator", 0)}, facts("g"), Current{}, Defaults{Role: "user", Group: 7})
	if got.Role != "operator" || got.Group != 7 {
		t.Errorf("role-only rule = %q/%d, want operator/7 (OU from defaults)", got.Role, got.Group)
	}
	// OU only: the role falls back to the provider default.
	got = Resolve([]Rule{rule(1, "", "g", "", 42)}, facts("g"), Current{}, Defaults{Role: "user", Group: 7})
	if got.Role != "user" || got.Group != 42 {
		t.Errorf("OU-only rule = %q/%d, want user/42", got.Role, got.Group)
	}
}

// TestResolveNoMatch pins the miss semantics, which the reference UI leaves ambiguous.
func TestResolveNoMatch(t *testing.T) {
	keep := Rule{ID: 1, Ord: 1, Enabled: true, Value: "nobody", KeepOnMiss: true}

	// Existing user + keep_on_miss: leave their role and OU exactly as they are.
	cur := Current{Exists: true, Role: "operator", Group: 55}
	got := Resolve([]Rule{keep}, facts("other"), cur, Defaults{Role: "user", Group: 7})
	if got.Deny || got.Role != "operator" || got.Group != 55 {
		t.Errorf("keep_on_miss on an existing user = %+v, want their current role/OU untouched", got)
	}

	// New user + keep_on_miss is meaningless: fall to the provider defaults.
	got = Resolve([]Rule{keep}, facts("other"), Current{}, Defaults{Role: "user", Group: 7})
	if got.Deny || got.Role != "user" || got.Group != 7 {
		t.Errorf("new user on miss = %+v, want the provider defaults", got)
	}

	// New user, no match, and NO default OU: deny. Never fall through to an implicit default,
	// which could be the unrestricted root OU.
	got = Resolve([]Rule{keep}, facts("other"), Current{}, Defaults{Role: "user"})
	if !got.Deny {
		t.Error("a new user with no match and no default OU must be denied, not silently placed")
	}
}

// TestResolveSkipsDisabledAndMatchesExactly guards the matching rules themselves.
func TestResolveSkipsDisabledAndMatchesExactly(t *testing.T) {
	disabled := Rule{ID: 1, Ord: 1, Enabled: false, Value: "g", TargetGroup: 10}
	live := rule(2, "", "g", "", 20)
	if got := Resolve([]Rule{disabled, live}, facts("g"), Current{}, Defaults{Group: 9}); got.RuleID != 2 {
		t.Errorf("a disabled rule must be skipped, matched %d", got.RuleID)
	}
	// Case-sensitive by default: group values are frequently GUIDs and DNs.
	if got := Resolve([]Rule{rule(1, "", "Admins", "", 10)}, facts("admins"), Current{}, Defaults{Group: 9}); got.Matched {
		t.Error("matching must be case-sensitive unless the rule opts out")
	}
	ci := Rule{ID: 1, Ord: 1, Enabled: true, Value: "Admins", TargetGroup: 10, CaseInsensitive: true}
	if got := Resolve([]Rule{ci}, facts("admins"), Current{}, Defaults{Group: 9}); !got.Matched {
		t.Error("a case-insensitive rule must match regardless of case")
	}
	// Exact, not substring: "admin" must not match the group "admins".
	if got := Resolve([]Rule{rule(1, "", "admin", "", 10)}, facts("admins"), Current{}, Defaults{Group: 9}); got.Matched {
		t.Error("matching must be exact, never a substring")
	}
}

// TestResolveNamedAttribute covers rules that match a specific attribute rather than the groups
// list — an empty Attr means "the groups attribute", per the admin UI.
func TestResolveNamedAttribute(t *testing.T) {
	f := Facts{Groups: []string{"g"}, Attrs: map[string][]string{"department": {"research"}}}
	got := Resolve([]Rule{rule(1, "department", "research", "operator", 30)}, f, Current{}, Defaults{Group: 9})
	if !got.Matched || got.Group != 30 {
		t.Errorf("named-attribute rule = %+v, want a match on department", got)
	}
	// A rule naming an attribute the assertion did not carry simply does not match.
	if got := Resolve([]Rule{rule(1, "missing", "x", "", 30)}, f, Current{}, Defaults{Group: 9}); got.Matched {
		t.Error("a rule on an absent attribute must not match")
	}
}

// TestResolveAdminElevationRequiresOptIn proves an IdP group cannot hand out an admin role unless
// the operator explicitly allowed it on that provider — the elevation is dropped, not silently
// honoured, and is reported so the caller can log it.
func TestResolveAdminElevationRequiresOptIn(t *testing.T) {
	priv := map[string]bool{"admin": true}
	r := rule(1, "", "g", "admin", 10)

	got := Resolve([]Rule{r}, facts("g"), Current{}, Defaults{Role: "user", Group: 9, PrivilegedRoles: priv})
	if got.Role == "admin" {
		t.Error("an admin role must not be granted while allow_admin_role is off")
	}
	if got.Role != "user" || !got.AdminBlocked {
		t.Errorf("blocked elevation = %+v, want the default role and AdminBlocked set", got)
	}
	// The OU from the same rule still applies — only the role elevation is refused.
	if got.Group != 10 {
		t.Errorf("group = %d, want the rule's OU to still apply", got.Group)
	}

	got = Resolve([]Rule{r}, facts("g"), Current{}, Defaults{Role: "user", Group: 9, PrivilegedRoles: priv, AllowAdminRole: true})
	if got.Role != "admin" || got.AdminBlocked {
		t.Errorf("with the opt-in on, the elevation must be honoured, got %+v", got)
	}
}

// TestShadowedReportsUnreachableRules backs the admin UI hint: a rule that can never win because an
// earlier enabled rule already matches the same value is flagged rather than silently ignored.
func TestShadowedReportsUnreachableRules(t *testing.T) {
	rules := []Rule{
		rule(1, "", "g", "user", 10),
		rule(2, "", "g", "operator", 20), // same attr+value, can never be reached
		rule(3, "", "other", "user", 30),
	}
	got := Shadowed(rules)
	if len(got) != 1 || got[0] != 2 {
		t.Errorf("shadowed = %v, want just rule 2", got)
	}
}

// TestPrivilegedRoleNeedsTheOptInOnEveryPath proves the admin-elevation guard is a property of the
// engine, not of one branch. The role can arrive from a matched rule, from the provider default on a
// brand-new account, or from the provider default filling a rule that named only an OU — and a
// privileged role must be dropped on all three unless the provider opted in.
func TestPrivilegedRoleNeedsTheOptInOnEveryPath(t *testing.T) {
	priv := map[string]bool{"admin": true}
	ouOnly := []Rule{{ID: 1, Enabled: true, Value: "staff", TargetGroup: 7}}
	roleRule := []Rule{{ID: 2, Enabled: true, Value: "staff", TargetRole: "admin", TargetGroup: 7}}
	member := Facts{Groups: []string{"staff"}}

	for _, tc := range []struct {
		name  string
		rules []Rule
		facts Facts
		cur   Current
	}{
		{"from a matched rule", roleRule, member, Current{}},
		{"from the default on a new account", nil, Facts{}, Current{}},
		{"from the default filling a rule that set only the OU", ouOnly, member, Current{}},
	} {
		d := Defaults{Role: "admin", Group: 9, PrivilegedRoles: priv}
		if out := Resolve(tc.rules, tc.facts, tc.cur, d); out.Role == "admin" {
			t.Errorf("%s: granted admin with allow_admin_role off", tc.name)
		} else if !out.AdminBlocked {
			t.Errorf("%s: the blocked elevation must be reported so it can be logged", tc.name)
		}
		d.AllowAdminRole = true
		if out := Resolve(tc.rules, tc.facts, tc.cur, d); out.Role != "admin" {
			t.Errorf("%s: with the opt-in on, admin must be granted, got %q", tc.name, out.Role)
		}
	}
}
