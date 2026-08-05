package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func mustAddUser(t *testing.T, s *Server, name string, admin bool) {
	t.Helper()
	role := "user"
	if admin {
		role = "admin"
	}
	if err := s.st.UpsertUser(User{Username: name, PasswordHash: "hash", Role: role, Active: true}); err != nil {
		t.Fatalf("seed user %s: %v", name, err)
	}
}

func seedProvider(t *testing.T, s *Server) SSOProvider {
	t.Helper()
	p := SSOProvider{Kind: "oidc", Slug: "corp", Name: "Corp SSO", Enabled: true, LinkBy: LinkByUsername}
	id, err := s.st.SaveSSOProvider(p)
	if err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	p.ID = id
	return p
}

// Under force-SSO the portal has stopped accepting a password at /api/login. Still accepting one
// here would make re-proving your identity weaker than signing in — the wrong way round for a
// control whose entire job is to be harder than the session it protects.
func TestForceSSOClosesThePasswordChannel(t *testing.T) {
	s := tenancyServer(t)
	seedProvider(t, s)
	mustAddUser(t, s, "alice", false)
	s.st.SetSetting(setLoginMode, loginSSORedirect)

	pol := s.stepUpPolicyFor("alice")
	if pol.Password {
		t.Errorf("force-SSO still offers the password channel to a normal account")
	}
	if !pol.SSO || pol.Reason != "sso_required" {
		t.Errorf("no SSO channel offered in its place: %+v", pol)
	}
}

// The exemption sso_only already carries, not a second differently-shaped one: an IdP that breaks
// must not cost an operator the ability to sign in and fix it.
func TestForceSSOKeepsThePasswordChannelForAnAdmin(t *testing.T) {
	s := tenancyServer(t)
	seedProvider(t, s)
	mustAddUser(t, s, "root", true)
	s.st.SetSetting(setLoginMode, loginSSORedirect)

	if pol := s.stepUpPolicyFor("root"); !pol.Password {
		t.Errorf("an admin was locked out of the password channel: %+v", pol)
	}
}

// With no provider configured the mode degrades, exactly as loginMode() already degrades: refusing
// the password channel while offering nothing in its place would strand every non-admin.
func TestForceSSOWithoutAProviderLeavesThePasswordChannelOpen(t *testing.T) {
	s := tenancyServer(t)
	mustAddUser(t, s, "alice", false)
	s.st.SetSetting(setLoginMode, loginSSORedirect)

	if pol := s.stepUpPolicyFor("alice"); !pol.Password || pol.SSO {
		t.Errorf("degraded wrongly with no provider: %+v", pol)
	}
}

func proofRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/account/passkeys", nil)
	r.AddCookie(&http.Cookie{Name: stepUpCookie, Value: token})
	return r
}

func mintProof(t *testing.T, s *Server, user string) string {
	t.Helper()
	tok, err := newAuthToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if err := s.st.CreateAuthRequest(AuthRequest{
		Token: tok, Kind: "oidc", Username: user, Purpose: authPurposeProved,
	}, time.Now().Add(stepUpProofTTL)); err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok
}

// A completed round-trip is the proof, and it is spent by being read. A proof that survived its
// first use would be a standing authorisation sitting in a browser for five minutes.
func TestSSOProofIsSingleUse(t *testing.T) {
	s := tenancyServer(t)
	mustAddUser(t, s, "alice", false)
	tok := mintProof(t, s, "alice")

	if !s.stepUpCookieProof(proofRequest(t, tok), "alice") {
		t.Fatal("a fresh proof was rejected")
	}
	if s.stepUpCookieProof(proofRequest(t, tok), "alice") {
		t.Error("the same proof was accepted twice")
	}
}

// Bound to a name, so a proof minted for one account cannot be spent on another — including inside
// one browser signed into both in turn.
func TestSSOProofIsBoundToItsAccount(t *testing.T) {
	s := tenancyServer(t)
	mustAddUser(t, s, "alice", false)
	mustAddUser(t, s, "bob", false)
	tok := mintProof(t, s, "alice")

	if s.stepUpCookieProof(proofRequest(t, tok), "bob") {
		t.Error("alice's proof stepped bob up")
	}
}

// A row that is merely a pending flow is not a proof. Reading the purpose is what separates "we
// asked the IdP" from "the IdP answered".
func TestAPendingFlowRowIsNotAProof(t *testing.T) {
	s := tenancyServer(t)
	mustAddUser(t, s, "alice", false)
	tok, _ := newAuthToken()
	if err := s.st.CreateAuthRequest(AuthRequest{
		Token: tok, Kind: "oidc", Username: "alice", Purpose: authPurposeStepUp,
	}, time.Now().Add(stepUpProofTTL)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if s.stepUpCookieProof(proofRequest(t, tok), "alice") {
		t.Error("an unfinished round-trip counted as a completed one")
	}
}

// And the gate reads it: no header at all, yet the request goes through.
func TestStepUpAcceptsTheSSOProofWithoutAHeader(t *testing.T) {
	s := tenancyServer(t)
	mustAddUser(t, s, "alice", false)
	tok := mintProof(t, s, "alice")

	if !s.stepUpOK(proofRequest(t, tok), "alice") {
		t.Error("a completed SSO step-up did not satisfy the gate")
	}
}
