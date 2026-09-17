package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/KazuhaHub/authcore/saml"
)

// samlPostResponse builds an httptest ACS POST carrying xmlDoc as the (unsigned, hand-built)
// SAMLResponse. Used by the tests below that exercise authcore/saml.Provider.ValidateResponse's
// PRE-signature structural checks (assertion count, Destination, weak-algorithm allowlist,
// encrypted-assertion refusal) — all of which run on the raw XML before crewjam ever verifies a
// signature, so a deliberately unsigned document is enough to reach them.
func samlPostResponse(acsURL, xmlDoc string) *http.Request {
	body := "SAMLResponse=" + url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(xmlDoc)))
	req := httptest.NewRequest(http.MethodPost, acsURL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// TestSAMLSubjectRejectsTransient proves a NameID that changes on every login is refused as an
// account key — linking to it would mint a new account per sign-in.
func TestSAMLSubjectRejectsTransient(t *testing.T) {
	mk := func(format, value string) *saml.Assertion {
		return &saml.Assertion{NameID: value, NameIDFormat: format}
	}
	if _, _, err := samlSubject(mk("urn:oasis:names:tc:SAML:2.0:nameid-format:persistent", "abc")); err != nil {
		t.Errorf("a persistent NameID must be accepted: %v", err)
	}
	for name, a := range map[string]*saml.Assertion{
		"transient":   mk("urn:oasis:names:tc:SAML:2.0:nameid-format:transient", "xyz"),
		"empty value": mk("urn:oasis:names:tc:SAML:2.0:nameid-format:persistent", "  "),
		"zero value":  {},
	} {
		if _, _, err := samlSubject(a); err == nil {
			t.Errorf("%s must be rejected as an account key", name)
		}
	}
	if _, _, err := samlSubject(nil); err == nil {
		t.Error("a nil assertion must be rejected, not panic")
	}
}

// TestSAMLClaimsFlattensAttributes proves samlClaims reshapes authcore's Attributes bag the way
// completeSSOLogin expects: single-valued as a bare string, multi-valued as a slice. Rejecting a
// duplicate attribute Name is no longer this function's job — samlProvider sets
// Config.StrictAttributes: true, so authcore/saml itself refuses that assertion before samlClaims
// ever sees it (see TestSAMLProviderConfigIsStrict).
//
// That refusal is covered here only at the config level. Proving it end to end needs a Response
// whose signature actually validates, because authcore checks attributes after signature
// validation, and these tests have no signing harness — every other rejection they exercise
// (assertion count, weak algorithm, encrypted assertion, Destination) happens before it.
func TestSAMLClaimsFlattensAttributes(t *testing.T) {
	a := &saml.Assertion{Attributes: map[string][]string{
		// authcore indexes one <Attribute> under BOTH its Name and FriendlyName when they differ —
		// this is what that looks like once ValidateResponse has already built the Assertion.
		"groups": {"analysts", "admins"},
		"http://schemas.xmlsoap.org/claims/Group": {"analysts", "admins"},
		"mail": {"a@b.c"},
	}}
	claims := samlClaims(a)
	groups, ok := claims["groups"].([]any)
	if !ok || len(groups) != 2 {
		t.Errorf("groups = %#v, want a 2-element slice", claims["groups"])
	}
	if claims["http://schemas.xmlsoap.org/claims/Group"] == nil {
		t.Errorf("both the attribute Name and FriendlyName must be indexed: %v", claims)
	}
	if claims["mail"] != "a@b.c" {
		t.Errorf("single-valued attribute = %v, want the bare string", claims["mail"])
	}
}

// TestSAMLReplayIsRefused proves the cache authcore/saml delegates to us actually persists and
// actually refuses a second use, and that the guard is scoped per IdP — see samlReplayCache's doc
// comment in saml_replay.go for why per-IdP scoping is what keeps this migration from repeating the
// audit package's tenancy failure.
func TestSAMLReplayIsRefused(t *testing.T) {
	st := newTestStore(t)
	now := time.Now()
	exp := now.Add(10 * time.Minute)

	c := samlReplayCache{st: st, idpEntityID: "https://idp.example"}
	if seen, err := c.SeenOrAdd(context.Background(), "_assert-1", exp, now); err != nil || seen {
		t.Fatalf("the first use of an assertion must be accepted, got seen=%v err=%v", seen, err)
	}
	if seen, err := c.SeenOrAdd(context.Background(), "_assert-1", exp, now); err != nil || !seen {
		t.Errorf("replaying an assertion must be refused, got seen=%v err=%v", seen, err)
	}
	// A different IdP's id space must not collide with this one's.
	other := samlReplayCache{st: st, idpEntityID: "https://idp-b.example"}
	if seen, err := other.SeenOrAdd(context.Background(), "_assert-1", exp, now); err != nil || seen {
		t.Errorf("the same assertion id under a different IdP must be a fresh sighting, got seen=%v err=%v", seen, err)
	}
}

// TestSAMLReplayCacheClampsTTL proves a hostile (or just misconfigured) far-future expiry cannot
// pin a replay row in the database forever. authcore/saml.Provider computes only a LOWER bound on
// how long an id must be remembered (Provider.expiryFor has no ceiling); the 30-minute ceiling is
// this project's own policy, carried over unchanged from the pre-authcore assertionExpiry, and it
// lives in samlReplayCache.SeenOrAdd now. This reads the persisted row back — not just SeenOrAdd's
// return value — to prove the clamp was actually applied to what got written.
func TestSAMLReplayCacheClampsTTL(t *testing.T) {
	st := newTestStore(t)
	c := samlReplayCache{st: st, idpEntityID: "https://idp.example"}
	now := time.Now()
	if _, err := c.SeenOrAdd(context.Background(), "_assert-hostile", now.Add(72*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("https://idp.example\x00_assert-hostile"))
	var expUnix int64
	err := st.queryRow(`SELECT expires_at FROM sso_assertion_seen WHERE seen_key=?`, hex.EncodeToString(sum[:])).Scan(&expUnix)
	if err != nil {
		t.Fatal(err)
	}
	if got := time.Unix(expUnix, 0); got.After(now.Add(35 * time.Minute)) {
		t.Errorf("stored replay expiry = %v, want it clamped to ~30 minutes from now", got)
	}
}

// TestSPKeypairGeneration proves the generated SP credential is what IdPs can actually consume, and
// that the private key round-trips through the sealed store.
func TestSPKeypairGeneration(t *testing.T) {
	keyPEM, certPEM, notAfter, err := generateSPKeypair("https://portal.example/metadata", 3*365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(keyPEM, "RSA PRIVATE KEY") || !strings.Contains(certPEM, "CERTIFICATE") {
		t.Fatal("keypair must be PEM encoded")
	}
	if notAfter.Before(time.Now().Add(365 * 24 * time.Hour)) {
		t.Error("the SP certificate should be long-lived; a short one silently breaks logins")
	}

	// It must load back through the same path the ACS uses.
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	enc, err := s.sealSecret("acme", "saml_sp_key", keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.samlKeypair(SSOProvider{Slug: "acme", SPKeyEnc: enc, SPCertPEM: certPEM}); err != nil {
		t.Errorf("the sealed keypair must load: %v", err)
	}
	// And a provider with no certificate yet must fail cleanly rather than panic.
	if _, _, err := s.samlKeypair(SSOProvider{Slug: "acme"}); err == nil {
		t.Error("a provider without an SP certificate must report that clearly")
	}
}

// samlTestServer / samlTestProvider give a provider complete enough for samlProvider: a public URL,
// an SP keypair (minted on save) and parseable IdP metadata.
func samlTestServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	st.EnsureDefaultGroup()
	st.SetSetting("public_url", "https://portal.example")
	return s
}

func samlTestProvider(t *testing.T, s *Server) SSOProvider {
	t.Helper()
	rec := httptest.NewRecorder()
	body := `{"kind":"saml","slug":"saml","name":"Corp"}`
	s.apiAdminSSOSave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/sso/providers", strings.NewReader(body)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("fixture provider save → %d (%s)", rec.Code, rec.Body.String())
	}
	p, ok := s.st.SSOProviderBySlug("saml")
	if !ok {
		t.Fatal("fixture provider was not stored")
	}
	p.IdPMetadataXML = testIdPMetadata
	if _, err := s.st.SaveSSOProvider(p); err != nil {
		t.Fatalf("store metadata: %v", err)
	}
	p, _ = s.st.SSOProviderBySlug("saml")
	return p
}

// TestSAMLProviderConfigIsStrict pins samlConfig's StrictAttributes: true. authcore/saml defaults
// StrictAttributes to false, and samlClaims itself no longer checks for a duplicate attribute Name
// — that responsibility moved entirely into authcore/saml's Config. If a future edit ever drops
// this field (or its value), attribute-pollution rejection silently disappears rather than failing
// loudly, which is exactly the failure mode this test exists to catch. Every Config field is
// exported, so this needs no reflection into authcore/saml's otherwise-private Provider internals
// — see samlConfig's doc comment in saml.go.
func TestSAMLProviderConfigIsStrict(t *testing.T) {
	s := samlTestServer(t)
	p := samlTestProvider(t, s)
	m, err := s.samlLoadMaterial(p)
	if err != nil {
		t.Fatalf("samlLoadMaterial: %v", err)
	}
	if cfg := s.samlConfig(p, m); !cfg.StrictAttributes {
		t.Error("samlConfig must set StrictAttributes: true, or a duplicate attribute name silently merges instead of being refused")
	}
}

// TestSAMLProviderRejectsMultipleAssertions is the regression this migration exists to add: crewjam
// alone parses every Assertion/EncryptedAssertion in a Response and accepts the first one that
// validates (GHSA-j2jp-wvqg-wc2g class — a signature bypass via a smuggled second assertion), and
// the pre-authcore saml.go had NO check for this at all (confirmed by grep over its history: there
// was no assertion-count logic anywhere). authcore/saml closes it unconditionally, before any
// signature is verified, so an unsigned two-Assertion document is enough to prove the wiring here
// actually reaches that check.
func TestSAMLProviderRejectsMultipleAssertions(t *testing.T) {
	s := samlTestServer(t)
	p := samlTestProvider(t, s)
	provider, _, err := s.samlProvider(p)
	if err != nil {
		t.Fatalf("samlProvider: %v", err)
	}
	xmlDoc := `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol">
		<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="a1"/>
		<Assertion xmlns="urn:oasis:names:tc:SAML:2.0:assertion" ID="a2"/>
	</Response>`
	req := samlPostResponse(s.samlACSURL(p.Slug), xmlDoc)

	_, err = provider.ValidateResponse(context.Background(), req, nil)
	if !errors.Is(err, saml.ErrTooManyAssertions) {
		t.Errorf("a Response with two Assertion elements must be refused as ErrTooManyAssertions, got %v", err)
	}
	if got := samlFailureReason(err); got != "saml_multiple_assertions" {
		t.Errorf("samlFailureReason(%v) = %q, want saml_multiple_assertions", err, got)
	}
}

// TestAuthnRequestDoesNotAskForATransientNameID guards against the closed loop the portal hit
// before: crewjam defaults AuthnNameIDFormat to TRANSIENT when it is unset ("to maintain library
// back-compat", per its own comment), so an AuthnRequest that omitted it would demand a transient
// NameID, and samlSubject rejects transient because a value that changes on every login cannot key
// an account. authcore/saml.Config leaves NameIDFormat unset, which it documents defaults to
// Unspecified rather than crewjam's raw zero value — this proves that default actually reaches the
// wire, by decoding the redirect-bound AuthnRequest back out of the URL authcore hands us.
func TestAuthnRequestDoesNotAskForATransientNameID(t *testing.T) {
	s := samlTestServer(t)
	p := samlTestProvider(t, s)
	provider, _, err := s.samlProvider(p)
	if err != nil {
		t.Fatalf("samlProvider: %v", err)
	}
	authReq, err := provider.NewAuthnRequest("relay-state")
	if err != nil {
		t.Fatalf("NewAuthnRequest: %v", err)
	}
	u, err := url.Parse(authReq.RedirectURL)
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}
	xmlBytes, err := saml.DecodeRedirectMessage(u.Query().Get("SAMLRequest"), 0)
	if err != nil {
		t.Fatalf("decode SAMLRequest: %v", err)
	}
	if strings.Contains(string(xmlBytes), "nameid-format:transient") {
		t.Errorf("the AuthnRequest still demands a transient NameID:\n%s", xmlBytes)
	}
}

// TestParseIdPMetadataUnwrapsFederation proves both the bare descriptor and the federation wrapper
// parse — crewjam models only the former, and Shibboleth/InCommon publish the latter.
func TestParseIdPMetadataUnwrapsFederation(t *testing.T) {
	const idp = `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example">
		<IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
			<SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example/sso"/>
		</IDPSSODescriptor></EntityDescriptor>`
	got, err := parseIdPMetadata(idp)
	if err != nil || got.EntityID != "https://idp.example" {
		t.Fatalf("bare descriptor = %v, %v", got, err)
	}
	wrapped := `<EntitiesDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata">` + idp + `</EntitiesDescriptor>`
	got, err = parseIdPMetadata(wrapped)
	if err != nil || got.EntityID != "https://idp.example" {
		t.Fatalf("federation wrapper = %v, %v", got, err)
	}
	for name, doc := range map[string]string{
		"empty":   "",
		"not xml": "<<<",
		"no idp":  `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="x"/>`,
	} {
		if _, err := parseIdPMetadata(doc); err == nil {
			t.Errorf("%s metadata must be rejected", name)
		}
	}
}

// TestSAMLRoutesAreInvisibleWhenDisabled proves a portal with SAML off is indistinguishable from
// one that never had it.
func TestSAMLRoutesAreInvisibleWhenDisabled(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	if _, ok := s.enabledProvider("nope", "saml"); ok {
		t.Error("an unknown slug must not resolve")
	}
	st.SaveSSOProvider(SSOProvider{Kind: "saml", Slug: "acme", Enabled: false})
	if _, ok := s.enabledProvider("acme", "saml"); ok {
		t.Error("a disabled provider must not resolve")
	}
	st.SaveSSOProvider(SSOProvider{Kind: "saml", Slug: "acme", Enabled: true})
	if _, ok := s.enabledProvider("acme", "oidc"); ok {
		t.Error("a SAML provider must not be reachable through the OIDC routes")
	}
}
