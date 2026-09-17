package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/KazuhaHub/authcore/saml"
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

// TestSAMLProviderRejectsWeakSignatureAlgorithm proves the portal's algorithm policy still applies
// once wired through authcore/saml. goxmldsig's default validation context maps rsa-sha1 straight
// to x509.SHA1WithRSA and applies no policy of its own, so an IdP left on the ADFS/legacy-Keycloak
// default would otherwise anchor every login on SHA-1 — for which collisions are practical — with
// nothing logged. The check runs on the raw XML before any signature is verified (authcore/saml,
// rawxml.go), so an unsigned-in-substance document that merely DECLARES a weak algorithm is enough
// to reach it without hand-building a real signature.
func TestSAMLProviderRejectsWeakSignatureAlgorithm(t *testing.T) {
	s := samlTestServer(t)
	p := samlTestProvider(t, s)
	provider, _, err := s.samlProvider(p)
	if err != nil {
		t.Fatalf("samlProvider: %v", err)
	}
	acs := s.samlACSURL(p.Slug)
	resp := func(sigAlg string) string {
		return `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol" Destination="` + acs + `">
			<Signature xmlns="http://www.w3.org/2000/09/xmldsig#"><SignedInfo>
			<SignatureMethod Algorithm="` + sigAlg + `"/>
			<Reference><DigestMethod Algorithm="http://www.w3.org/2001/04/xmlenc#sha256"/></Reference>
			</SignedInfo></Signature>
			<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="a1"/></Response>`
	}
	_, err = provider.ValidateResponse(context.Background(),
		samlPostResponse(acs, resp("http://www.w3.org/2000/09/xmldsig#rsa-sha1")), nil)
	if !errors.Is(err, saml.ErrWeakSignatureAlgorithm) {
		t.Errorf("rsa-sha1 must be refused as ErrWeakSignatureAlgorithm, got %v", err)
	}
	if got := samlFailureReason(err); got != "saml_weak_signature" {
		t.Errorf("samlFailureReason(%v) = %q, want saml_weak_signature", err, got)
	}
}

// TestSAMLProviderRejectsEncryptedAssertion proves the declared non-goal (ADR 0023) is still an
// explicit refusal, not an absence of support, once wired through authcore/saml. crewjam looks for
// an encrypted assertion FIRST and would feed it to the SP private key, which is the
// decryption-oracle surface ADR 0023 chose not to take on. Like the weak-algorithm check, this runs
// before any signature verification, so an intentionally-unsigned document reaches it.
func TestSAMLProviderRejectsEncryptedAssertion(t *testing.T) {
	s := samlTestServer(t)
	p := samlTestProvider(t, s)
	provider, _, err := s.samlProvider(p)
	if err != nil {
		t.Fatalf("samlProvider: %v", err)
	}
	acs := s.samlACSURL(p.Slug)
	xmlDoc := `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol" Destination="` + acs + `">
		<EncryptedAssertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion">
		<EncryptedData xmlns="http://www.w3.org/2001/04/xmlenc#"/></EncryptedAssertion></Response>`
	_, err = provider.ValidateResponse(context.Background(), samlPostResponse(acs, xmlDoc), nil)
	if !errors.Is(err, saml.ErrEncryptedAssertionNotAllowed) {
		t.Errorf("an encrypted assertion must be refused as ErrEncryptedAssertionNotAllowed, got %v", err)
	}
	if got := samlFailureReason(err); got != "saml_encrypted" {
		t.Errorf("samlFailureReason(%v) = %q, want saml_encrypted", err, got)
	}
}

// TestSAMLProviderRequiresDestination proves the gap crewjam leaves open is still closed once
// wired through authcore/saml: crewjam only enforces Destination when the Response itself is
// signed OR the attribute is present, so an attacker can omit it on an unsigned Response (still
// valid when only the Assertion is signed) to skip the check entirely. authcore/saml's
// RequireDestination (on by default, left unset here — see samlConfig) closes that independently of
// crewjam, and this reaches it the same way the two tests above do: before any signature check.
func TestSAMLProviderRequiresDestination(t *testing.T) {
	s := samlTestServer(t)
	p := samlTestProvider(t, s)
	provider, _, err := s.samlProvider(p)
	if err != nil {
		t.Fatalf("samlProvider: %v", err)
	}
	acs := s.samlACSURL(p.Slug)
	oneAssertion := func(destAttr string) string {
		return `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol"` + destAttr + `>
			<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="a1"/></Response>`
	}
	_, err = provider.ValidateResponse(context.Background(), samlPostResponse(acs, oneAssertion("")), nil)
	if !errors.Is(err, saml.ErrMissingDestination) {
		t.Errorf("an absent Destination must be refused as ErrMissingDestination, got %v", err)
	}
	_, err = provider.ValidateResponse(context.Background(),
		samlPostResponse(acs, oneAssertion(` Destination="https://evil.example/acs"`)), nil)
	if !errors.Is(err, saml.ErrDestinationMismatch) {
		t.Errorf("a Destination for another SP must be refused as ErrDestinationMismatch, got %v", err)
	}
	if got := samlFailureReason(saml.ErrMissingDestination); got != "saml_destination" {
		t.Errorf("samlFailureReason(ErrMissingDestination) = %q, want saml_destination", got)
	}
}
