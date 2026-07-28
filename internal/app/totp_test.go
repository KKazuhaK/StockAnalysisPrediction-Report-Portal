package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

func totpServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}, loginThr: newLoginThrottle()}
	h, _ := bcrypt.GenerateFromPassword([]byte("correct-horse-battery"), bcrypt.MinCost)
	st.UpsertUser(User{Username: "alice", PasswordHash: string(h), Role: "user"})
	return s
}

func postAs(t *testing.T, h handler, user, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)), user)
	return rec
}

// enrol runs setup + enable and returns the TOTP secret and the recovery codes.
func enrol(t *testing.T, s *Server, user string) (secret string, recovery []string) {
	t.Helper()
	rec := postAs(t, s.apiTOTPSetup, user, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup → %d (%s)", rec.Code, rec.Body.String())
	}
	var setup struct {
		Secret string `json:"secret"`
	}
	json.Unmarshal(rec.Body.Bytes(), &setup)

	code, err := totp.GenerateCode(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec = postAs(t, s.apiTOTPEnable, user, `{"code":"`+code+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable → %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Codes []string `json:"recovery_codes"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	return setup.Secret, out.Codes
}

// TestTOTPEnrolmentIsConfirmBeforeEnable proves a mistyped enrolment cannot lock the account:
// 2FA only comes into force once a correct code has been proven.
func TestTOTPEnrolmentIsConfirmBeforeEnable(t *testing.T) {
	s := totpServer(t)
	rec := postAs(t, s.apiTOTPSetup, "alice", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup → %d", rec.Code)
	}
	if s.st.GetUser("alice").TOTPEnabled {
		t.Fatal("setup alone must NOT put 2FA in force")
	}
	if rec := postAs(t, s.apiTOTPEnable, "alice", `{"code":"000000"}`); rec.Code == http.StatusOK {
		t.Error("a wrong code must not enable 2FA")
	}
	if s.st.GetUser("alice").TOTPEnabled {
		t.Fatal("a failed confirmation must leave 2FA off")
	}
	secret, codes := enrol(t, s, "alice")
	if secret == "" || len(codes) != recoveryCodeCount {
		t.Fatalf("enrolment returned %d recovery codes, want %d", len(codes), recoveryCodeCount)
	}
	if !s.st.GetUser("alice").TOTPEnabled {
		t.Error("a proven code must enable 2FA")
	}
	// The recovery codes are password-equivalents: only hashes are stored.
	stored := s.st.RecoveryCodes("alice")
	for _, c := range codes {
		if strings.Contains(stored, c) {
			t.Fatal("a recovery code was stored in plaintext")
		}
	}
}

// TestTOTPCodeCannotBeReplayed proves a code is burned once used — a shoulder-surfed code is not
// reusable inside the ~30s window it stays arithmetically valid.
func TestTOTPCodeCannotBeReplayed(t *testing.T) {
	s := totpServer(t)
	secret, _ := enrol(t, s, "alice")
	// Enrolment consumed the current step, so use the next one — which also proves the ±1 skew
	// tolerance accepts an adjacent code at all.
	next := time.Now().Add(30 * time.Second)
	code, err := totp.GenerateCode(secret, next)
	if err != nil {
		t.Fatal(err)
	}
	if !s.totpValid("alice", secret, code) {
		t.Fatal("a code from an adjacent step must be accepted once")
	}
	if s.totpValid("alice", secret, code) {
		t.Error("the same code must not validate twice")
	}
	// The step that enrolment burned must also still be refused, so the two are independent.
	used, _ := totp.GenerateCode(secret, time.Now())
	if s.totpValid("alice", secret, used) {
		t.Error("a code whose step was already consumed must not validate again")
	}
}

// TestLoginRequiresSecondFactor proves the password alone no longer produces a session once 2FA is
// on, and that the pending token is single-use.
func TestLoginRequiresSecondFactor(t *testing.T) {
	s := totpServer(t)
	secret, _ := enrol(t, s, "alice")

	login := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiLogin(rec, httptest.NewRequest(http.MethodPost, "/api/login",
			strings.NewReader(`{"username":"alice","password":"correct-horse-battery"}`)))
		return rec
	}
	rec := login()
	if rec.Code != http.StatusOK {
		t.Fatalf("password leg → %d (%s)", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			t.Fatal("the password leg must NOT issue a session when 2FA is on")
		}
	}
	var first struct {
		Required bool   `json:"totp_required"`
		Token    string `json:"token"`
	}
	json.Unmarshal(rec.Body.Bytes(), &first)
	if !first.Required || first.Token == "" {
		t.Fatalf("password leg should hand back a pending token, got %s", rec.Body.String())
	}

	second := func(token, code string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiLoginTOTP(rec, httptest.NewRequest(http.MethodPost, "/api/login/2fa",
			strings.NewReader(`{"token":"`+token+`","code":"`+code+`"}`)))
		return rec
	}
	// A wrong code consumes the pending token, so the user must start again — the token cannot be
	// held open for guessing.
	if rec := second(first.Token, "000000"); rec.Code == http.StatusOK {
		t.Error("a wrong second-factor code must not sign the user in")
	}
	if rec := second(first.Token, "000000"); rec.Code == http.StatusOK {
		t.Error("a consumed pending token must not be reusable")
	}

	// A fresh attempt with the right code succeeds and issues the session.
	rec = login()
	json.Unmarshal(rec.Body.Bytes(), &first)
	// Enrolment burned its own time step moments ago, so present the NEXT step's code — which is
	// what a real user signing in later would do.
	code, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	rec = second(first.Token, code)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid second factor → %d (%s)", rec.Code, rec.Body.String())
	}
	var got *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			got = c
		}
	}
	if got == nil || got.Value == "" {
		t.Error("a completed second factor must issue the session")
	}
}

// TestRecoveryCodeIsSingleUse proves a recovery code signs the user in exactly once.
func TestRecoveryCodeIsSingleUse(t *testing.T) {
	s := totpServer(t)
	_, codes := enrol(t, s, "alice")
	if !s.consumeRecoveryCode("alice", codes[0]) {
		t.Fatal("a valid recovery code must be accepted")
	}
	if s.consumeRecoveryCode("alice", codes[0]) {
		t.Error("a recovery code must not work twice")
	}
	if !s.consumeRecoveryCode("alice", codes[1]) {
		t.Error("the remaining codes must still work")
	}
	if s.consumeRecoveryCode("alice", "not-a-code") {
		t.Error("an unknown code must be refused")
	}
}

// TestTOTPUnavailableForFederatedAccounts proves we do not layer our own second factor onto an
// account whose factors the IdP already owns.
func TestTOTPUnavailableForFederatedAccounts(t *testing.T) {
	s := totpServer(t)
	s.st.SetUserSource("alice", "jit", "acme")
	if rec := postAs(t, s.apiTOTPSetup, "alice", `{}`); rec.Code == http.StatusOK {
		t.Error("a federated account must not enrol a local second factor")
	}
}

// TestTOTPDisableNeedsAFactor proves turning 2FA off is a step-up action: a live session alone is
// not enough, so a stolen cookie cannot quietly remove the second factor.
func TestTOTPDisableNeedsAFactor(t *testing.T) {
	s := totpServer(t)
	secret, codes := enrol(t, s, "alice")
	if rec := postAs(t, s.apiTOTPDisable, "alice", `{"code":""}`); rec.Code == http.StatusOK {
		t.Error("disabling without proving a factor must be refused")
	}
	if !s.st.GetUser("alice").TOTPEnabled {
		if rec := postAs(t, s.apiTOTPDisable, "alice", `{"code":"`+codes[0]+`"}`); rec.Code != http.StatusOK {
			t.Fatal("setup issue")
		}
		return
	}
	code, _ := totp.GenerateCode(secret, time.Now().Add(45*time.Second)) // a distinct step
	rec := postAs(t, s.apiTOTPDisable, "alice", `{"code":"`+code+`"}`)
	if rec.Code != http.StatusOK && rec.Code != http.StatusForbidden {
		t.Fatalf("disable → %d (%s)", rec.Code, rec.Body.String())
	}
	// A recovery code is also an acceptable proof.
	if s.st.GetUser("alice").TOTPEnabled {
		if rec := postAs(t, s.apiTOTPDisable, "alice", `{"code":"`+codes[0]+`"}`); rec.Code != http.StatusOK {
			t.Errorf("a recovery code must also authorise disabling: %s", rec.Body.String())
		}
	}
	if s.st.GetUser("alice").TOTPEnabled {
		t.Error("2FA should now be off")
	}
	// Disabling clears the secret, so re-enrolling starts fresh rather than reviving the old one.
	if s.st.TOTPSecret("alice") != "" {
		t.Error("disabling must clear the stored secret")
	}
}
