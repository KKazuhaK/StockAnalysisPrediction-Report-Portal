package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/beevik/etree"
	"github.com/crewjam/saml"
)

// TestRequireDestination covers the gap crewjam leaves open: when a Response is unsigned it SKIPS
// the Destination check entirely if the attribute is absent (service_provider.go:1008), so an
// assertion captured at another SP could be replayed at ours. Absence must be a rejection.
func TestRequireDestination(t *testing.T) {
	const acs = "https://portal.example/api/auth/saml/acme/acs"
	ok := `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol" Destination="` + acs + `"/>`
	if err := requireDestination([]byte(ok), acs); err != nil {
		t.Errorf("a matching Destination must pass: %v", err)
	}
	for name, doc := range map[string]string{
		"absent":    `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol"/>`,
		"empty":     `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol" Destination=""/>`,
		"other SP":  `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol" Destination="https://evil.example/acs"/>`,
		"not xml":   `not xml at all`,
		"near miss": `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol" Destination="` + acs + `/"/>`,
	} {
		if err := requireDestination([]byte(doc), acs); err == nil {
			t.Errorf("%s Destination must be rejected", name)
		}
	}
}

// TestSAMLSubjectRejectsTransient proves a NameID that changes on every login is refused as an
// account key — linking to it would mint a new account per sign-in.
func TestSAMLSubjectRejectsTransient(t *testing.T) {
	mk := func(format, value string) *saml.Assertion {
		return &saml.Assertion{Subject: &saml.Subject{
			NameID: &saml.NameID{Format: format, Value: value}}}
	}
	if _, _, err := samlSubject(mk("urn:oasis:names:tc:SAML:2.0:nameid-format:persistent", "abc")); err != nil {
		t.Errorf("a persistent NameID must be accepted: %v", err)
	}
	for name, a := range map[string]*saml.Assertion{
		"transient":  mk("urn:oasis:names:tc:SAML:2.0:nameid-format:transient", "xyz"),
		"empty":      mk("urn:oasis:names:tc:SAML:2.0:nameid-format:persistent", "  "),
		"no nameid":  {Subject: &saml.Subject{}},
		"no subject": {},
	} {
		if _, _, err := samlSubject(a); err == nil {
			t.Errorf("%s must be rejected as an account key", name)
		}
	}
}

// TestSAMLClaimsRejectDuplicateAttributes proves attribute pollution is refused rather than
// resolved last-wins. Rules map an attribute to a role AND an OU, so a duplicate is aimed straight
// at the tenancy boundary.
func TestSAMLClaimsRejectDuplicateAttributes(t *testing.T) {
	attr := func(name, friendly string, vals ...string) saml.Attribute {
		a := saml.Attribute{Name: name, FriendlyName: friendly}
		for _, v := range vals {
			a.Values = append(a.Values, saml.AttributeValue{Value: v})
		}
		return a
	}
	good := &saml.Assertion{AttributeStatements: []saml.AttributeStatement{{Attributes: []saml.Attribute{
		attr("http://schemas.xmlsoap.org/claims/Group", "groups", "analysts", "admins"),
		attr("mail", "", "a@b.c"),
	}}}}
	claims, err := samlClaims(good)
	if err != nil {
		t.Fatalf("a well-formed assertion must parse: %v", err)
	}
	// Both the URN and the FriendlyName are addressable, since IdPs send the long form while
	// admins configure the short one.
	if claims["groups"] == nil || claims["http://schemas.xmlsoap.org/claims/Group"] == nil {
		t.Errorf("both the attribute Name and FriendlyName must be indexed: %v", claims)
	}
	if claims["mail"] != "a@b.c" {
		t.Errorf("single-valued attribute = %v, want the bare string", claims["mail"])
	}

	dup := &saml.Assertion{AttributeStatements: []saml.AttributeStatement{{Attributes: []saml.Attribute{
		attr("groups", "", "analysts"),
		attr("groups", "", "admins"),
	}}}}
	if _, err := samlClaims(dup); err == nil {
		t.Error("a duplicate attribute name must be rejected, not resolved last-wins")
	}
}

// TestSAMLReplayIsRefused proves the cache crewjam does not provide: the same assertion cannot be
// consumed twice, and the guard is keyed per IdP.
func TestSAMLReplayIsRefused(t *testing.T) {
	st := newTestStore(t)
	a := &saml.Assertion{ID: "_assert-1"}
	exp := assertionExpiry(a)
	if !st.MarkAssertionSeen("https://idp.example", a.ID, exp) {
		t.Fatal("the first use of an assertion must be accepted")
	}
	if st.MarkAssertionSeen("https://idp.example", a.ID, exp) {
		t.Error("replaying an assertion must be refused")
	}
}

// TestAssertionExpiryIsClamped proves a hostile NotOnOrAfter cannot pin a replay row forever, and
// that a missing one still yields a usable window.
func TestAssertionExpiryIsClamped(t *testing.T) {
	far := &saml.Assertion{Conditions: &saml.Conditions{NotOnOrAfter: time.Now().Add(72 * time.Hour)}}
	if got := assertionExpiry(far); got.After(time.Now().Add(35 * time.Minute)) {
		t.Errorf("expiry = %v, want it clamped to ~30 minutes", got)
	}
	// Conditions is optional in the schema (a nil pointer), and must not panic.
	if got := assertionExpiry(&saml.Assertion{}); !got.After(time.Now()) {
		t.Error("an assertion with no Conditions must still get a future replay window")
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

// samlTestServer / samlTestProvider give a provider complete enough for samlSP: a public URL, an
// SP keypair (minted on save) and parseable IdP metadata.
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

// The portal asked for exactly the thing it then refused.
//
// crewjam defaults AuthnNameIDFormat to TRANSIENT when it is unset — "to maintain library
// back-compat", per its own comment — so every AuthnRequest carried a NameIDPolicy demanding a
// transient NameID. A compliant IdP obeys, and samlSubject rejects transient because a value that
// changes on every login cannot key an account. A closed loop: correct IdP configuration could
// never satisfy it, and the only clue was "bad_response" on the login page.
func TestAuthnRequestDoesNotAskForATransientNameID(t *testing.T) {
	s := samlTestServer(t)
	p := samlTestProvider(t, s)
	sp, err := s.samlSP(p)
	if err != nil {
		t.Fatalf("samlSP: %v", err)
	}
	req, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		t.Fatalf("MakeAuthenticationRequest: %v", err)
	}
	doc := etree.NewDocument()
	doc.SetRoot(req.Element())
	xmlStr, err := doc.WriteToString()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	if strings.Contains(xmlStr, "nameid-format:transient") {
		t.Errorf("the AuthnRequest still demands a transient NameID:\n%s", xmlStr)
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
