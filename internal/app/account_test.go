package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"golang.org/x/crypto/bcrypt"
)

// The self-service security surface (ADR 0023): the account page needs to know which credentials an
// account HAS before it can offer to change them, and a user must be able to change their own
// password without an admin or an emailed link.

func accountServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		loginThr: newLoginThrottle()}
	st.SetSetting("public_url", "https://portal.example")
	st.UpsertUser(User{Username: "alice", PasswordHash: mustHash("correct-horse-battery"), Role: "user"})
	return s
}

// TestMeReportsSecurityState proves /api/me carries what the account page must branch on. Without
// it the UI would have to guess: offering a password change to a federated account, or a 2FA
// enrolment to someone who already has one, is a dead end the user only discovers on submit.
func TestMeReportsSecurityState(t *testing.T) {
	s := accountServer(t)
	me := func() map[string]any {
		var out map[string]any
		b, _ := json.Marshal(s.meJSON("alice"))
		json.Unmarshal(b, &out)
		return out
	}
	got := me()
	for _, k := range []string{"federated", "totp_enabled", "passkeys"} {
		if _, ok := got[k]; !ok {
			t.Errorf("/api/me must report %q", k)
		}
	}
	if got["federated"] != false || got["totp_enabled"] != false || got["passkeys"] != float64(0) {
		t.Errorf("a plain local account reads %v", got)
	}

	s.st.AddPasskey("alice", "YubiKey", fakeCred("cred-a", 0))
	s.st.SetTOTPSecret("alice", "sealed")
	s.st.EnableTOTP("alice", `["h1"]`)
	if got = me(); got["totp_enabled"] != true || got["passkeys"] != float64(1) {
		t.Errorf("after enrolment /api/me reads %v", got)
	}

	s.st.SetUserSource("alice", "jit", "acme")
	if got = me(); got["federated"] != true {
		t.Error("a federated account must be reported as such so the UI hides local credential controls")
	}
}

// TestChangePasswordRequiresTheCurrentOne proves the endpoint re-authenticates. The current password
// IS the step-up here — it is the credential being replaced, so proving it is both necessary and
// sufficient, and it is what every other product asks for.
func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	s := accountServer(t)
	change := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiChangePassword(rec, httptest.NewRequest(http.MethodPost, "/api/me/password", strings.NewReader(body)), "alice")
		return rec
	}
	if rec := change(`{"current":"wrong","new":"a-brand-new-passphrase"}`); rec.Code == http.StatusOK {
		t.Error("a wrong current password must be refused")
	}
	if rec := change(`{"current":"correct-horse-battery","new":"x"}`); rec.Code == http.StatusOK {
		t.Error("a new password that fails the policy must be refused")
	}
	if rec := change(`{"current":"correct-horse-battery","new":"a-brand-new-passphrase"}`); rec.Code != http.StatusOK {
		t.Fatalf("a correct change → %d %s", rec.Code, rec.Body.String())
	}
	u := s.st.GetUser("alice")
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("a-brand-new-passphrase")) != nil {
		t.Error("the new password must be in force")
	}
	// The old one must be dead, and the change must not be replayable with it.
	if rec := change(`{"current":"correct-horse-battery","new":"another-passphrase"}`); rec.Code == http.StatusOK {
		t.Error("the old password must stop working")
	}
}

// TestChangePasswordRevokesOtherSessions proves a password change ends the sessions an attacker may
// be holding. A change that left a stolen cookie working would be theatre: the user believes they
// have locked the intruder out, and they have not.
func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	s := accountServer(t)
	stolen := s.signUser(*s.st.GetUser("alice"))
	if name, _ := s.verify(stolen); name != "alice" {
		t.Fatal("the session must be valid to begin with")
	}
	rec := httptest.NewRecorder()
	s.apiChangePassword(rec, httptest.NewRequest(http.MethodPost, "/api/me/password",
		strings.NewReader(`{"current":"correct-horse-battery","new":"a-brand-new-passphrase"}`)), "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("change → %d %s", rec.Code, rec.Body.String())
	}
	if s.sessionValid(stolen) {
		t.Error("a password change must invalidate sessions issued before it")
	}
	// And the caller keeps a working session, or they would be logged out of the page they are on.
	if len(rec.Result().Cookies()) == 0 {
		t.Error("the change must re-issue the caller's own session")
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && !s.sessionValid(c.Value) {
			t.Error("the re-issued session must be valid")
		}
	}
}

// TestChangePasswordRefusesFederatedAccounts proves we do not hand a local password to an account
// whose credentials the IdP owns — the SSO login path would refuse it, so it would be a password
// that cannot be used.
func TestChangePasswordRefusesFederatedAccounts(t *testing.T) {
	s := accountServer(t)
	s.st.SetUserSource("alice", "jit", "acme")
	rec := httptest.NewRecorder()
	s.apiChangePassword(rec, httptest.NewRequest(http.MethodPost, "/api/me/password",
		strings.NewReader(`{"current":"correct-horse-battery","new":"a-brand-new-passphrase"}`)), "alice")
	if rec.Code == http.StatusOK {
		t.Error("a federated account must not get a local password")
	}
}

// TestChangePasswordIsThrottled proves the endpoint is not a lockout-free oracle for the current
// password, which is exactly what the login form refuses to be.
func TestChangePasswordIsThrottled(t *testing.T) {
	s := accountServer(t)
	body := `{"current":"guess","new":"a-brand-new-passphrase"}`
	for i := 0; i < 12; i++ {
		rec := httptest.NewRecorder()
		s.apiChangePassword(rec, httptest.NewRequest(http.MethodPost, "/api/me/password", strings.NewReader(body)), "alice")
	}
	rec := httptest.NewRecorder()
	s.apiChangePassword(rec, httptest.NewRequest(http.MethodPost, "/api/me/password",
		strings.NewReader(`{"current":"correct-horse-battery","new":"a-brand-new-passphrase"}`)), "alice")
	if rec.Code == http.StatusOK {
		t.Error("repeated wrong guesses must lock the endpoint, even for the right password")
	}
}
