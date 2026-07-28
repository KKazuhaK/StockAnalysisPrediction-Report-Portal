package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

func tenancyServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	st.SetSetting("public_url", "https://portal.example")
	return s
}

// Regression tests for four tenancy and credential defects found by an adversarial review of the
// finished authentication stack (ADR 0023). Each one was reproducible before its fix, and each
// failed in the same direction: quietly granting MORE access than intended.

// A just-created account must not be treated as an EXISTING one by the rule engine. It is written
// to the users table moments earlier with no group, so a keep-on-miss rule took the
// leave-them-as-they-are branch and left a brand-new external user in group 0 — unrestricted,
// able to read every report.
func TestJITUserWithKeepOnMissLandsRestricted(t *testing.T) {
	s := tenancyServer(t)
	root := s.st.EnsureDefaultGroup()
	ext, err := s.st.CreateUserGroup("ext-org", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.st.SetGroupParent(ext, root)
	s.st.SetGroupRestricted(ext, true)

	pid, err := s.st.SaveSSOProvider(SSOProvider{
		Kind: "oidc", Slug: "acme", Name: "Acme", Enabled: true, Provisioning: "jit",
		DefaultGroup: ext, DefaultRole: "user", Issuer: "https://idp.example", ClientID: "cid",
		AttrGroups: "groups", AttrUPN: "preferred_username",
	})
	if err != nil {
		t.Fatal(err)
	}
	// One enabled rule with keep_on_miss that will NOT match this login.
	if _, err := s.st.exec(`INSERT INTO sso_group_rules(provider_id,ord,enabled,attr,value,target_role,target_group,keep_on_miss,ci,note)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, pid, 0, 1, "", "acme-external", "", ext, 1, 0, ""); err != nil {
		t.Fatal(err)
	}
	p, _ := s.st.SSOProviderBySlug("acme")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/acme/callback", nil)
	s.completeSSOLogin(rec, r, p, ssoIdentity{
		Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-1",
		Claims: map[string]any{"preferred_username": "mallory@evil.test", "groups": []any{"nothing-matching"}},
	}, "/")

	t.Logf("status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	t.Logf("users: primaryGroup=%d restricted=%v", s.st.PrimaryGroupOf("mallory"), s.st.EffectiveGroupSettings("mallory").Restricted)
	if u := s.st.GetUser("mallory"); u != nil {
		t.Logf("user row: role=%q source=%q active=%v", u.Role, u.Source, u.Active)
	}
	if s.viewerScope("mallory") == nil {
		t.Errorf("mallory is UNSCOPED (unrestricted) — sees every report")
	}
}

// A rule naming an attribute could never match, because the login path populated only Groups and
// left Facts.Attrs empty. The admin UI happily accepts such rules, so they looked configured and
// silently did nothing.
func TestAttributeNamedRuleMatches(t *testing.T) {
	s := tenancyServer(t)
	root := s.st.EnsureDefaultGroup()
	ext, _ := s.st.CreateUserGroup("ext-org", "", 0)
	s.st.SetGroupParent(ext, root)
	s.st.SetGroupRestricted(ext, true)
	staff, _ := s.st.CreateUserGroup("staff", "", 0)
	s.st.SetGroupParent(staff, root)

	pid, _ := s.st.SaveSSOProvider(SSOProvider{
		Kind: "oidc", Slug: "acme", Name: "Acme", Enabled: true, Provisioning: "jit",
		DefaultGroup: staff, DefaultRole: "user", Issuer: "https://idp.example", ClientID: "cid",
		AttrGroups: "groups", AttrUPN: "preferred_username",
	})
	// Rule: department == "contractor" -> the restricted external OU.
	s.st.exec(`INSERT INTO sso_group_rules(provider_id,ord,enabled,attr,value,target_role,target_group,keep_on_miss,ci,note)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, pid, 0, 1, "department", "contractor", "", ext, 0, 0, "")
	p, _ := s.st.SSOProviderBySlug("acme")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	s.completeSSOLogin(rec, r, p, ssoIdentity{
		Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-2",
		Claims: map[string]any{"preferred_username": "carol@acme.test", "department": "contractor"},
	}, "/")
	t.Logf("carol primaryGroup=%d (ext=%d staff=%d) restricted=%v",
		s.st.PrimaryGroupOf("carol"), ext, staff, s.st.EffectiveGroupSettings("carol").Restricted)
	if s.st.PrimaryGroupOf("carol") != ext {
		t.Errorf("department=contractor rule did not place carol in the restricted OU")
	}
}

// A rule pointing at a group that no longer exists must fail the login, not clear the user's
// group: SetPrimaryGroup treats an unknown id as "clear", which dropped the user into the
// unrestricted Default group and turned a typo into a privilege escalation.
func TestDanglingRuleTargetDoesNotUnrestrict(t *testing.T) {
	s := tenancyServer(t)
	root := s.st.EnsureDefaultGroup()
	ext, _ := s.st.CreateUserGroup("ext-org", "", 0)
	s.st.SetGroupParent(ext, root)
	s.st.SetGroupRestricted(ext, true)

	s.st.UpsertUser(User{Username: "ext1", PasswordHash: "", Role: "user"})
	s.st.SetUserSource("ext1", "jit", "acme")
	s.st.SetPrimaryGroup("ext1", ext)
	if !s.st.EffectiveGroupSettings("ext1").Restricted {
		t.Fatal("setup: ext1 should start restricted")
	}

	pid, _ := s.st.SaveSSOProvider(SSOProvider{
		Kind: "oidc", Slug: "acme", Enabled: true, Provisioning: "jit",
		DefaultGroup: ext, DefaultRole: "user", Issuer: "https://idp.example", ClientID: "cid",
		AttrGroups: "groups", AttrUPN: "preferred_username",
	})
	// A rule pointing at a group id that no longer exists (typo / deleted OU).
	s.st.exec(`INSERT INTO sso_group_rules(provider_id,ord,enabled,attr,value,target_role,target_group,keep_on_miss,ci,note)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, pid, 0, 1, "", "acme-external", "", 9999, 0, 0, "")
	p, _ := s.st.SSOProviderBySlug("acme")
	s.st.LinkIdentity(Identity{Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-3", Username: "ext1", ProviderSlug: "acme"})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	s.completeSSOLogin(rec, r, p, ssoIdentity{
		Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-3",
		Claims: map[string]any{"preferred_username": "ext1", "groups": []any{"acme-external"}},
	}, "/")
	t.Logf("status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	t.Logf("ext1 primaryGroup=%d restricted=%v", s.st.PrimaryGroupOf("ext1"), s.st.EffectiveGroupSettings("ext1").Restricted)
	if !s.st.EffectiveGroupSettings("ext1").Restricted {
		t.Errorf("ext1 was silently un-restricted by a dangling target_group")
	}
}

// The stored credential blob is written once at registration, so loading the sign counter from it
// compared every later ceremony against the registration-time value — making clone detection
// permanently useless. The column is the authoritative counter.
func TestPasskeyCounterLoadsFromColumn(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.AddPasskey("alice", "A", fakeCred("cred-a", 5))
	s.st.TouchPasskey([]byte("cred-a"), 9)
	creds, _ := s.st.PasskeyCredentials("alice")
	if len(creds) != 1 {
		t.Fatal("no credential")
	}
	t.Logf("counter loaded for the next ceremony = %d (column says 9)", creds[0].Authenticator.SignCount)
	if creds[0].Authenticator.SignCount != 9 {
		t.Errorf("clone detection compares against the STALE counter %d", creds[0].Authenticator.SignCount)
	}
}
