package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/crewjam/saml"
	"golang.org/x/crypto/bcrypt"
)

// Second-round hardening of the ADR 0023 auth surface. Each test here is a probe from the security
// review turned into a permanent regression.

func hardeningServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		loginThr: newLoginThrottle()}
	st.SetSetting("public_url", "https://portal.example")
	return s
}

// TestOIDCDiscoveryCacheIsBoundToTheIssuer proves that repointing a provider at a different IdP
// takes effect. The cached discovery document names the authorization, token and JWKS endpoints of
// whoever it was fetched from; using it after the issuer changed would leave the OLD identity
// provider minting tokens the portal still accepts — the new one is configured but never consulted.
func TestOIDCDiscoveryCacheIsBoundToTheIssuer(t *testing.T) {
	s := hardeningServer(t)
	cached := `{"issuer":"https://old-idp.example","authorization_endpoint":"https://old-idp.example/a",` +
		`"token_endpoint":"https://old-idp.example/t","jwks_uri":"https://old-idp.example/jwks"}`

	// Same issuer: the cache is used, and no network call happens (the issuer below is
	// unreachable, so a refetch would error).
	p := SSOProvider{Slug: "acme", Kind: "oidc", Issuer: "https://old-idp.example", DiscoveryJSON: cached}
	if _, err := s.oidcDiscover(t.Context(), p); err != nil {
		t.Fatalf("a cache matching the configured issuer must be used: %v", err)
	}
	// Issuer changed: the stale document must NOT be used. It must refetch — which fails here,
	// and failing closed is the point.
	p.Issuer = "https://new-idp.example"
	if _, err := s.oidcDiscover(t.Context(), p); err == nil {
		t.Error("a discovery document from a different issuer must not be used")
	}
}

// TestSavingANewIssuerDropsTheCachedDiscovery proves the admin save path does not carry a stale
// document forward, so the very next login refetches instead of relying on the guard above alone.
func TestSavingANewIssuerDropsTheCachedDiscovery(t *testing.T) {
	s := hardeningServer(t)
	s.st.SaveSSOProvider(SSOProvider{Slug: "acme", Kind: "oidc", Issuer: "https://old-idp.example",
		Provisioning: "off", DefaultRole: "user"})
	p, _ := s.st.SSOProviderBySlug("acme")
	s.st.SaveOIDCDiscovery(p.ID, `{"issuer":"https://old-idp.example"}`)

	save := func(issuer string) SSOProvider {
		body, _ := json.Marshal(map[string]any{"slug": "acme", "kind": "oidc", "issuer": issuer,
			"provisioning": "off", "default_role": "user"})
		rec := httptest.NewRecorder()
		s.apiAdminSSOSave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/sso/providers", strings.NewReader(string(body))), "admin")
		if rec.Code != http.StatusOK {
			t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
		}
		got, _ := s.st.SSOProviderBySlug("acme")
		return got
	}
	if got := save("https://old-idp.example"); got.DiscoveryJSON == "" {
		t.Error("an unchanged issuer must keep the cached document — refetching on every save is a needless dependency on the IdP")
	}
	if got := save("https://new-idp.example"); got.DiscoveryJSON != "" {
		t.Errorf("changing the issuer must drop the cached document, got %q", got.DiscoveryJSON)
	}
}

// TestStepUpProofIsNotInTheQueryString proves the re-proved credential — a password, a live TOTP
// code or a single-use recovery code — is not carried where it would be written to every
// reverse-proxy access log, kept in browser history, and sent in the Referer of any subresource.
func TestStepUpProofIsNotInTheQueryString(t *testing.T) {
	s := hardeningServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: mustHash("correct-horse"), Role: "user"})

	q := httptest.NewRequest(http.MethodPost, "/x?proof=correct-horse", nil)
	if s.stepUpOK(q, "alice") {
		t.Error("a proof in the query string must not be accepted")
	}
	h := httptest.NewRequest(http.MethodPost, "/x", nil)
	h.Header.Set(stepUpHeader, "correct-horse")
	if !s.stepUpOK(h, "alice") {
		t.Error("a proof in the step-up header must be accepted")
	}
}

// TestStepUpIsThrottled proves a stolen session cannot be used to brute-force the account password
// or a 6-digit TOTP code. Step-up is an online guessing oracle exactly like the login form, so it
// must share the login form's lockout.
func TestStepUpIsThrottled(t *testing.T) {
	s := hardeningServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: mustHash("correct-horse"), Role: "user"})
	try := func(proof string) bool {
		r := httptest.NewRequest(http.MethodPost, "/x", nil)
		r.Header.Set(stepUpHeader, proof)
		return s.stepUpOK(r, "alice")
	}
	for i := 0; i < 12; i++ {
		if try(fmt.Sprintf("guess-%d", i)) {
			t.Fatal("a wrong proof must never pass")
		}
	}
	if try("correct-horse") {
		t.Error("step-up must lock out after repeated failures, even for the right proof")
	}
}

// TestStepUpGuardsEveryCredentialChange proves the guard is not wired to one route and forgotten on
// the rest. ADR 0023: "changing a password, managing 2FA/passkeys, and minting API tokens re-require
// a factor even inside a valid session". Enrolling a fresh second factor and revoking a registered
// one are both credential changes; a stolen cookie must not be able to do either.
func TestStepUpGuardsEveryCredentialChange(t *testing.T) {
	s := hardeningServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: mustHash("correct-horse"), Role: "user"})
	s.st.AddPasskey("alice", "A", fakeCred("cred-a", 0))
	id := s.st.PasskeyList("alice")[0]["id"].(int64)

	for _, tc := range []struct {
		name    string
		call    func(w http.ResponseWriter, r *http.Request)
		method  string
		path    string
		body    string
		stillOK func() bool
	}{
		{"2fa setup", func(w http.ResponseWriter, r *http.Request) { s.apiTOTPSetup(w, r, "alice") },
			http.MethodPost, "/api/me/2fa/setup", "", nil},
		{"passkey delete", func(w http.ResponseWriter, r *http.Request) { s.apiPasskeyDelete(w, r, "alice") },
			http.MethodDelete, "/api/me/passkeys/1", "", func() bool { return len(s.st.PasskeyList("alice")) == 1 }},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.SetPathValue("id", fmt.Sprint(id))
		rec := httptest.NewRecorder()
		tc.call(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s with only a session → %d, want 403", tc.name, rec.Code)
		}
		if tc.stillOK != nil && !tc.stillOK() {
			t.Errorf("%s must not have taken effect", tc.name)
		}
	}
}

// TestDefaultRoleCannotBypassAllowAdminRole proves the admin-elevation guard covers the provider
// DEFAULT role, not only a role coming from a matched rule. The default is applied to every user
// the rules do not speak about, which is the larger population.
func TestDefaultRoleCannotBypassAllowAdminRole(t *testing.T) {
	s := hardeningServer(t)
	gid, _ := s.st.CreateUserGroup("Externals", "", 0)
	s.st.SetGroupParent(gid, s.st.EnsureDefaultGroup())
	s.st.SetGroupRestricted(gid, true)
	s.st.SaveSSOProvider(SSOProvider{Slug: "acme", Kind: "oidc", Provisioning: "jit", Enabled: true,
		DefaultRole: "admin", DefaultGroup: gid, AllowAdminRole: false, AttrUPN: "preferred_username"})
	p, _ := s.st.SSOProviderBySlug("acme")

	rec := httptest.NewRecorder()
	s.completeSSOLogin(rec, httptest.NewRequest(http.MethodGet, "/cb", nil), p,
		ssoIdentity{Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-1",
			Claims: map[string]any{"preferred_username": "mallory"}}, "/")

	u := s.st.GetUser("mallory")
	if u == nil {
		t.Fatal("the account should have been provisioned")
	}
	if u.Role == "admin" {
		t.Error("a privileged default_role must be dropped when allow_admin_role is off")
	}
	if got := s.st.PrimaryGroupOf("mallory"); got != gid {
		t.Errorf("group = %d, want the restricted default %d", got, gid)
	}
}

// TestRefusedJITLoginLeavesNoAccount proves a login that is provisioned and then REFUSED does not
// leave the account behind. A leftover row is not inert: on the next attempt it is adopted as
// pre-existing, so the keep-on-miss branch runs instead of the new-user branch and the deny that
// protected the first attempt no longer applies.
func TestRefusedJITLoginLeavesNoAccount(t *testing.T) {
	s := hardeningServer(t)
	// jit with NO default group and no rules: the engine must deny a brand-new user rather than
	// leave them unscoped.
	s.st.SaveSSOProvider(SSOProvider{Slug: "acme", Kind: "oidc", Provisioning: "jit", Enabled: true,
		DefaultRole: "user", DefaultGroup: 0, AttrUPN: "preferred_username"})
	p, _ := s.st.SSOProviderBySlug("acme")

	id := ssoIdentity{Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-1",
		Claims: map[string]any{"preferred_username": "drifter"}}
	rec := httptest.NewRecorder()
	s.completeSSOLogin(rec, httptest.NewRequest(http.MethodGet, "/cb", nil), p, id, "/")

	if u := s.st.GetUser("drifter"); u != nil {
		t.Fatalf("a refused login must not leave an account behind: %+v", u)
	}
	// And the second attempt must be refused for the same reason, not sail through as "existing".
	rec = httptest.NewRecorder()
	s.completeSSOLogin(rec, httptest.NewRequest(http.MethodGet, "/cb", nil), p, id, "/")
	if u := s.st.GetUser("drifter"); u != nil {
		t.Error("the refusal must be stable across attempts")
	}
}

// TestSessionHoursShortensTheSignedToken proves a provider's session limit is enforced server-side.
// The cookie MaxAge is a browser-side hint that anyone holding the cookie value simply ignores, so
// a limit that only shortened MaxAge would be no limit at all — and until SCIM exists, a short
// session is the portal's only answer to an IdP-side disable.
func TestSessionHoursShortensTheSignedToken(t *testing.T) {
	s := hardeningServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	u := *s.st.GetUser("alice")

	rec := httptest.NewRecorder()
	s.issueSession(rec, httptest.NewRequest(http.MethodGet, "/", nil), u, SSOProvider{SessionHours: 2})
	cookie := rec.Result().Cookies()[0]

	if cookie.MaxAge > 2*3600 {
		t.Errorf("cookie MaxAge = %d, want at most 2h", cookie.MaxAge)
	}
	// The signed token itself must expire with it.
	name, _ := s.verify(cookie.Value)
	if name != "alice" {
		t.Fatalf("the session must verify now, got %q", name)
	}
	exp := sessionExpiry(t, cookie.Value)
	if exp.After(time.Now().Add(3 * time.Hour)) {
		t.Errorf("the signed session expires at %s — a provider's session limit must bind the token, not just the cookie", exp)
	}
}

func sessionExpiry(t *testing.T, cookie string) time.Time {
	t.Helper()
	msg, _, _ := strings.Cut(cookie, ".")
	raw, err := base64.RawURLEncoding.DecodeString(msg)
	if err != nil {
		t.Fatal(err)
	}
	seg := strings.Split(string(raw), "|")
	var unix int64
	fmt.Sscanf(seg[len(seg)-1], "%d", &unix)
	return time.Unix(unix, 0)
}

func mustHash(pw string) string {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return string(h)
}

// TestSAMLRejectsWeakSignatureAlgorithms proves the portal enforces its own algorithm policy.
// goxmldsig's default validation context maps rsa-sha1 to x509.SHA1WithRSA and applies no policy of
// its own, so an IdP left on the ADFS/legacy-Keycloak default would otherwise anchor every login on
// SHA-1 — for which collisions are practical — with nothing logged.
func TestSAMLRejectsWeakSignatureAlgorithms(t *testing.T) {
	resp := func(sigAlg, digestAlg string) []byte {
		return []byte(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol">
			<ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#"><ds:SignedInfo>
			<ds:SignatureMethod Algorithm="` + sigAlg + `"/>
			<ds:Reference><ds:DigestMethod Algorithm="` + digestAlg + `"/></ds:Reference>
			</ds:SignedInfo></ds:Signature></samlp:Response>`)
	}
	const sha256Sig = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
	const sha256Dig = "http://www.w3.org/2001/04/xmlenc#sha256"

	if err := rejectWeakSignatureAlgs(resp(sha256Sig, sha256Dig)); err != nil {
		t.Errorf("SHA-256 must be accepted: %v", err)
	}
	for _, tc := range []struct{ name, sig, dig string }{
		{"rsa-sha1 signature", "http://www.w3.org/2000/09/xmldsig#rsa-sha1", sha256Dig},
		{"sha1 digest", sha256Sig, "http://www.w3.org/2000/09/xmldsig#sha1"},
		{"md5 digest", sha256Sig, "http://www.w3.org/2001/04/xmldsig-more#md5"},
		{"an algorithm nobody considered", "http://example.test/my-own-crypto", sha256Dig},
	} {
		if err := rejectWeakSignatureAlgs(resp(tc.sig, tc.dig)); err == nil {
			t.Errorf("%s must be refused", tc.name)
		}
	}
	// A strong OUTER signature must not vouch for a SHA-1 inner one.
	nested := []byte(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol">
		<ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#"><ds:SignedInfo>
		<ds:SignatureMethod Algorithm="` + sha256Sig + `"/>
		<ds:Reference><ds:DigestMethod Algorithm="` + sha256Dig + `"/></ds:Reference></ds:SignedInfo></ds:Signature>
		<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">
		<ds:Signature xmlns:ds="http://www.w3.org/2000/09/xmldsig#"><ds:SignedInfo>
		<ds:SignatureMethod Algorithm="http://www.w3.org/2000/09/xmldsig#rsa-sha1"/>
		</ds:SignedInfo></ds:Signature></saml:Assertion></samlp:Response>`)
	if err := rejectWeakSignatureAlgs(nested); err == nil {
		t.Error("a SHA-1 assertion signature must be refused even under a SHA-256 response signature")
	}
}

// TestSAMLRejectsEncryptedAssertions proves the declared non-goal is an explicit refusal rather than
// an absence of support. crewjam looks for an encrypted assertion FIRST and would feed it to the SP
// private key, which is the decryption-oracle surface ADR 0023 chose not to take on.
func TestSAMLRejectsEncryptedAssertions(t *testing.T) {
	enc := []byte(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol">
		<saml:EncryptedAssertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">
		<xenc:EncryptedData xmlns:xenc="http://www.w3.org/2001/04/xmlenc#"/></saml:EncryptedAssertion></samlp:Response>`)
	if err := rejectEncryptedAssertion(enc); err == nil {
		t.Error("an encrypted assertion must be refused")
	}
	plain := []byte(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol">
		<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion"/></samlp:Response>`)
	if err := rejectEncryptedAssertion(plain); err != nil {
		t.Errorf("a plain assertion must pass: %v", err)
	}
}

// TestReplayEntryOutlivesTheAcceptanceWindow proves the replay cache cannot lapse while the library
// would still accept the assertion. crewjam accepts until NotOnOrAfter PLUS MaxClockSkew, and it
// checks SubjectConfirmationData's own NotOnOrAfter too — an entry keyed on the shorter of those
// would reopen the very replay it exists to close.
func TestReplayEntryOutlivesTheAcceptanceWindow(t *testing.T) {
	notAfter := time.Now().Add(4 * time.Minute)
	a := &saml.Assertion{Conditions: &saml.Conditions{NotOnOrAfter: notAfter}}
	if got := assertionExpiry(a); !got.After(notAfter.Add(saml.MaxClockSkew)) {
		t.Errorf("expiry %s must outlast NotOnOrAfter+MaxClockSkew (%s)", got, notAfter.Add(saml.MaxClockSkew))
	}
	// A later SubjectConfirmationData window must extend the entry as well.
	scNotAfter := time.Now().Add(9 * time.Minute)
	a.Subject = &saml.Subject{SubjectConfirmations: []saml.SubjectConfirmation{
		{SubjectConfirmationData: &saml.SubjectConfirmationData{NotOnOrAfter: scNotAfter}}}}
	if got := assertionExpiry(a); !got.After(scNotAfter.Add(saml.MaxClockSkew)) {
		t.Errorf("expiry %s must outlast the SubjectConfirmationData window (%s)", got, scNotAfter.Add(saml.MaxClockSkew))
	}
	// A hostile NotOnOrAfter still cannot pin a row for long.
	a = &saml.Assertion{Conditions: &saml.Conditions{NotOnOrAfter: time.Now().Add(100 * 365 * 24 * time.Hour)}}
	if got := assertionExpiry(a); got.After(time.Now().Add(2 * time.Hour)) {
		t.Errorf("a hostile NotOnOrAfter must stay clamped, got %s", got)
	}
}
