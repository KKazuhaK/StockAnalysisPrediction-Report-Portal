package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/captcha"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// Self-service registration and the captcha gate on the public forms.

func regServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		loginThr: newLoginThrottle(), captchaSvc: captcha.New(), names: LoadNames(t.TempDir(), st)}
	s.names.fetch = func(string) string { return "" }
	st.SetSetting("public_url", "https://portal.example")
	st.EnsureDefaultGroup()
	// A mail sender that records rather than sends, so verification is testable offline.
	s.mailFn = func(to []string, subject, body string) error { return nil }
	st.SetSetting("smtp_enabled", "1")
	st.SetSetting("smtp_host", "localhost")
	st.SetSetting("smtp_port", "25")
	st.SetSetting("smtp_from", "portal@example")
	return s
}

func postPublic(t *testing.T, h http.HandlerFunc, path, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	var m map[string]any
	json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

// TestRegistrationIsOffByDefault proves the route does not exist until an admin turns it on. A
// portal that gains a public signup form on upgrade would be a surprise of the worst kind.
func TestRegistrationIsOffByDefault(t *testing.T) {
	s := regServer(t)
	code, _ := postPublic(t, s.apiRegister, "/api/register",
		`{"email":"a@example.com","password":"a-long-enough-password"}`)
	if code != http.StatusNotFound {
		t.Errorf("registration while disabled → %d, want 404", code)
	}
	rec := httptest.NewRecorder()
	s.apiRegisterConfig(rec, httptest.NewRequest(http.MethodGet, "/api/register/config", nil))
	var cfg map[string]any
	json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg["enabled"] != false {
		t.Errorf("config says enabled=%v on a fresh portal", cfg["enabled"])
	}
}

// TestUnplacedRegistrantSeesNothing is the security property of the whole feature. With no OU
// configured, a new account is created restricted and granted nothing — it can sign in and read
// zero reports until an admin decides otherwise.
func TestUnplacedRegistrantSeesNothing(t *testing.T) {
	s := regServer(t)
	s.st.SetSetting(setRegEnabled, "1")
	s.st.SetSetting(setRegVerify, "0") // verification is covered separately
	s.st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-28", RType: "投资决策",
		Title: "内部投资决策", MD: "scoring table"})

	code, _ := postPublic(t, s.apiRegister, "/api/register",
		`{"email":"newbie@example.com","password":"a-long-enough-password"}`)
	if code != http.StatusOK {
		t.Fatalf("registration → %d", code)
	}
	u := s.st.GetUser("newbie@example.com")
	if u == nil {
		t.Fatal("the account was not created")
	}
	if !u.Restricted {
		t.Error("an unplaced registrant must be scoped, or they read every internal report")
	}
	if sc := s.viewerScope("newbie@example.com"); sc == nil {
		t.Fatal("an unplaced registrant must have a read scope")
	}
	reps, _ := s.st.SearchNew(Filters{}, s.viewerScope("newbie@example.com"))
	if len(reps) != 0 {
		t.Errorf("an unplaced registrant sees %d reports, want 0", len(reps))
	}
}

// TestPlacedRegistrantJoinsTheConfiguredOU is the other half of the setting: an admin who wants
// signups to be usable immediately names an OU, and the account inherits its grants.
func TestPlacedRegistrantJoinsTheConfiguredOU(t *testing.T) {
	s := regServer(t)
	root := s.st.EnsureDefaultGroup()
	ou, _ := s.st.CreateUserGroup("clients", "", 0)
	s.st.SetGroupParent(ou, root)
	s.st.SetGroupRestricted(ou, true)
	s.st.SaveVersion(ReportVersion{Name: "对外版", Ord: 1, Visibility: VisibilityAll})
	s.st.SetVersionGrants("对外版", []string{groupPrincipal(ou)})
	s.st.SetSetting(setRegEnabled, "1")
	s.st.SetSetting(setRegVerify, "0")
	s.st.SetSetting(setRegGroup, itoa(ou))

	id, _, _ := s.st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-28", RType: "投资决策",
		Title: "对外结论", Version: "对外版", MD: "conclusion"})
	if code, _ := postPublic(t, s.apiRegister, "/api/register",
		`{"email":"client@example.com","password":"a-long-enough-password"}`); code != http.StatusOK {
		t.Fatalf("registration → %d", code)
	}
	if got := s.st.PrimaryGroupOf("client@example.com"); got != ou {
		t.Errorf("registrant joined OU %d, want %d", got, ou)
	}
	if r, _ := s.st.GetNew(id, s.viewerScope("client@example.com")); r == nil {
		t.Error("a placed registrant must inherit the OU's grants and read the published version")
	}
}

// TestVerificationGatesTheAccount proves the account is unusable until the emailed link is followed
// — otherwise anyone can create an account against someone else's address.
func TestVerificationGatesTheAccount(t *testing.T) {
	s := regServer(t)
	s.st.SetSetting(setRegEnabled, "1")

	code, body := postPublic(t, s.apiRegister, "/api/register",
		`{"email":"pending@example.com","password":"a-long-enough-password"}`)
	if code != http.StatusOK || body["requires_verification"] != true {
		t.Fatalf("registration → %d %v", code, body)
	}
	u := s.st.GetUser("pending@example.com")
	if u == nil || u.Active {
		t.Fatal("an unverified account must be created disabled")
	}
	// It cannot be used yet.
	rec := httptest.NewRecorder()
	s.apiLogin(rec, httptest.NewRequest(http.MethodPost, "/api/login",
		strings.NewReader(`{"username":"pending@example.com","password":"a-long-enough-password"}`)))
	if rec.Code == http.StatusOK {
		t.Error("an unverified account must not be able to sign in")
	}

	var token string
	s.st.queryRow("SELECT token FROM auth_requests WHERE kind='verify' AND username=?",
		"pending@example.com").Scan(&token)
	if token == "" {
		t.Fatal("no verification token was parked")
	}
	if code, _ := postPublic(t, s.apiRegisterVerify, "/api/register/verify",
		`{"token":"`+token+`"}`); code != http.StatusOK {
		t.Fatalf("verify → %d", code)
	}
	if u = s.st.GetUser("pending@example.com"); u == nil || !u.Active {
		t.Error("verification must enable the account")
	}
	// The link is single-use, so a forwarded email cannot re-activate a later-disabled account.
	if code, _ := postPublic(t, s.apiRegisterVerify, "/api/register/verify",
		`{"token":"`+token+`"}`); code == http.StatusOK {
		t.Error("a verification link must not be reusable")
	}
	if code, _ := postPublic(t, s.apiRegisterVerify, "/api/register/verify",
		`{"token":"forged"}`); code == http.StatusOK {
		t.Error("a forged verification token must be refused")
	}
}

// TestRegistrationValidation covers the refusals a signup form owes its users, and the one it owes
// the operator: an address it cannot mail must not become a stranded account.
func TestRegistrationValidation(t *testing.T) {
	s := regServer(t)
	s.st.SetSetting(setRegEnabled, "1")
	s.st.SetSetting(setRegVerify, "0")

	for _, tc := range []struct{ name, body string }{
		{"not an email", `{"email":"nope","password":"a-long-enough-password"}`},
		{"empty email", `{"email":"","password":"a-long-enough-password"}`},
		{"short password", `{"email":"a@example.com","password":"short"}`},
	} {
		if code, _ := postPublic(t, s.apiRegister, "/api/register", tc.body); code == http.StatusOK {
			t.Errorf("%s was accepted", tc.name)
		}
	}
	// The domain allow-list.
	s.st.SetSetting(setRegDomains, "example.com, corp.example")
	if code, _ := postPublic(t, s.apiRegister, "/api/register",
		`{"email":"a@elsewhere.test","password":"a-long-enough-password"}`); code == http.StatusOK {
		t.Error("an out-of-list domain was accepted")
	}
	if code, _ := postPublic(t, s.apiRegister, "/api/register",
		`{"email":"a@corp.example","password":"a-long-enough-password"}`); code != http.StatusOK {
		t.Error("an allowed domain was refused")
	}
	// A taken address IS revealed — the expected signup experience, and an attacker learns the same
	// fact by simply trying.
	if code, _ := postPublic(t, s.apiRegister, "/api/register",
		`{"email":"a@corp.example","password":"a-long-enough-password"}`); code != http.StatusConflict {
		t.Error("a duplicate registration must say so")
	}
	// Verification on with no SMTP configured: refuse rather than strand an account whose
	// activation email can never be sent.
	s2 := regServer(t)
	s2.st.SetSetting(setRegEnabled, "1")
	s2.st.SetSetting("smtp_enabled", "0")
	if code, _ := postPublic(t, s2.apiRegister, "/api/register",
		`{"email":"a@example.com","password":"a-long-enough-password"}`); code == http.StatusOK {
		t.Error("registration must refuse when it cannot send the verification email")
	}
}

// TestCaptchaGatesThePublicForms proves the gate is wired to all three, refuses with the marker the
// SPA needs, and — the part that matters — fails CLOSED when its verifier is misconfigured.
func TestCaptchaGatesThePublicForms(t *testing.T) {
	s := regServer(t)
	s.st.SetSetting(setRegEnabled, "1")
	s.st.SetSetting(setRegVerify, "0")
	for _, k := range []string{setCaptchaLogin, setCaptchaForgot, setCaptchaRegister} {
		s.st.SetSetting(k, "1")
	}

	for _, tc := range []struct {
		name       string
		h          http.HandlerFunc
		path, body string
	}{
		{"login", s.apiLogin, "/api/login", `{"username":"admin","password":"x"}`},
		{"forgot", s.apiForgotPassword, "/api/password/forgot", `{"account":"admin"}`},
		{"register", s.apiRegister, "/api/register", `{"email":"a@example.com","password":"a-long-enough-password"}`},
	} {
		code, body := postPublic(t, tc.h, tc.path, tc.body)
		if code != http.StatusBadRequest || body["captcha_required"] != true {
			t.Errorf("%s without a captcha → %d %v, want 400 + captcha_required", tc.name, code, body)
		}
	}
	// Forgot-password normally answers 200 for everything so it cannot be used to enumerate
	// accounts; the captcha refusal is the one exception, and it reveals nothing about the account.
	if u := s.st.GetUser("a@example.com"); u != nil {
		t.Error("a refused registration must not have created an account")
	}

	// Misconfigured provider: the gate must refuse, never open.
	s.st.SetSetting(setCaptchaProvider, "turnstile") // token provider with no secret
	code, body := postPublic(t, s.apiRegister, "/api/register",
		`{"email":"b@example.com","password":"a-long-enough-password","captcha_token":"anything"}`)
	if code != http.StatusBadRequest || body["captcha_required"] != true {
		t.Errorf("a misconfigured provider → %d %v, want a refusal (fail closed)", code, body)
	}
	if s.st.GetUser("b@example.com") != nil {
		t.Error("a fail-closed captcha must not have let the registration through")
	}
}

// TestCaptchaAfterFailuresStaysQuietUntilItIsNeeded proves the login trigger: an ordinary sign-in
// on a quiet portal is never asked for a captcha, and the demand appears once a source starts
// guessing.
func TestCaptchaAfterFailuresStaysQuietUntilItIsNeeded(t *testing.T) {
	s := regServer(t)
	s.st.SetSetting(setCaptchaLogin, "1")
	s.st.SetSetting(setCaptchaTrigger, triggerAfterFailure)
	s.st.SetSetting(setCaptchaThreshold, "3")

	r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	if s.captchaRequired(r, ctxLogin, "alice") {
		t.Error("a first attempt must not be asked for a captcha in after_failures mode")
	}
	for i := 0; i < 3; i++ {
		s.loginThr.record("u:alice", time.Now())
	}
	if !s.captchaRequired(r, ctxLogin, "alice") {
		t.Error("the threshold must turn the captcha on for that account")
	}
	// A different account from the same quiet address is unaffected.
	if s.captchaRequired(r, ctxLogin, "bob") {
		t.Error("one account's failures must not gate a different account")
	}
	// The other contexts have no failure history to consult, so enabling them is always-on.
	s.st.SetSetting(setCaptchaRegister, "1")
	if !s.captchaRequired(r, ctxRegister, "") {
		t.Error("registration must be gated as soon as it is enabled")
	}
}
