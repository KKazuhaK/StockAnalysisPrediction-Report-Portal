package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// A minimal IdP metadata document: one IDPSSODescriptor, which is the only thing
// parseIdPMetadata requires.
const testIdPMetadata = `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example">
	<IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
		<SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example/sso"/>
	</IDPSSODescriptor></EntityDescriptor>`

// allowSSOFetchTo opens the SSRF guard just far enough to reach a loopback httptest server over
// plain http. Both switches are the ones production already has: the private-address setting a
// self-hosted IdP needs, and the test-only insecure flag.
func (s *Server) allowSSOFetchTo(_ string) {
	s.st.SetSetting("sso_allow_private", "1")
	s.ssoInsecureForTest = true
}

func adminSSOServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	st.EnsureDefaultGroup()
	st.SetSetting("public_url", "https://portal.example")
	return s
}

func saveProvider(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiAdminSSOSave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/sso/providers", strings.NewReader(body)), "admin")
	return rec
}

func listProviders(t *testing.T, s *Server) (string, []map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiAdminSSOProviders(rec, httptest.NewRequest(http.MethodGet, "/api/admin/sso/providers", nil), "admin")
	var resp struct {
		Providers []map[string]any `json:"providers"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Body.String(), resp.Providers
}

// The addresses an admin pastes into the IdP depend on the public URL and the slug — not on
// anything stored — and they are needed BEFORE a provider is saved, because configuring the IdP is
// step one and saving the portal side is step four. Sending them only for stored rows left the
// setup guide showing two empty boxes on exactly the install that had never configured SSO.
func TestSSOAdminSendsSPAddressesBeforeAnythingIsSaved(t *testing.T) {
	s := adminSSOServer(t)
	rec := httptest.NewRecorder()
	s.apiAdminSSOProviders(rec, httptest.NewRequest(http.MethodGet, "/api/admin/sso/providers", nil), "admin")

	var resp struct {
		Providers []map[string]any             `json:"providers"`
		Defaults  map[string]map[string]string `json:"sp_defaults"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(resp.Providers) != 0 {
		t.Fatalf("fixture should have no stored providers, got %d", len(resp.Providers))
	}
	for _, want := range []struct{ kind, key, url string }{
		{"saml", "sp_entity_id", "https://portal.example/api/auth/saml/saml/metadata"},
		{"saml", "sp_acs_url", "https://portal.example/api/auth/saml/saml/acs"},
		{"oidc", "redirect_url", "https://portal.example/api/auth/oidc/oidc/callback"},
	} {
		if got := resp.Defaults[want.kind][want.key]; got != want.url {
			t.Errorf("sp_defaults[%s][%s] = %q, want %q", want.kind, want.key, got, want.url)
		}
	}
}

// And a saved provider's addresses must be the SAME strings, or the guide would teach one value
// while the server accepts another.
func TestSSOAdminDefaultsMatchTheSavedProvider(t *testing.T) {
	s := adminSSOServer(t)
	saveProvider(t, s, `{"kind":"saml","slug":"saml","name":"Corp"}`)

	rec := httptest.NewRecorder()
	s.apiAdminSSOProviders(rec, httptest.NewRequest(http.MethodGet, "/api/admin/sso/providers", nil), "admin")
	var resp struct {
		Providers []map[string]any             `json:"providers"`
		Defaults  map[string]map[string]string `json:"sp_defaults"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Providers) != 1 {
		t.Fatalf("want 1 stored provider, got %d", len(resp.Providers))
	}
	for _, k := range []string{"sp_entity_id", "sp_acs_url"} {
		if resp.Providers[0][k] != resp.Defaults["saml"][k] {
			t.Errorf("%s: stored %v, default %v — they must be one string", k, resp.Providers[0][k], resp.Defaults["saml"][k])
		}
	}
}

// First-time SAML setup deadlocked. Enabling SAML requires stored IdP metadata
// (validateProviderForEnable), and fetching that metadata required a stored provider row — but the
// row cannot be saved while "enabled" is on and the metadata is missing. With the enable switch at
// the TOP of the form, an admin who turns it on before filling anything in gets "no such provider"
// from the fetch button and "fetch or paste the IdP metadata first" from save, and neither says
// which order to do them in. The fetch button now carries the URL itself and creates the draft.
func TestFetchMetadataWorksBeforeTheProviderIsSaved(t *testing.T) {
	s := adminSSOServer(t)
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testIdPMetadata))
	}))
	defer idp.Close()
	s.allowSSOFetchTo(idp.URL)

	rec := httptest.NewRecorder()
	body := `{"kind":"saml","idp_metadata_url":"` + idp.URL + `/federationmetadata.xml"}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sso/providers/saml/metadata", strings.NewReader(body))
	req.SetPathValue("slug", "saml")
	s.apiAdminSSOFetchMetadata(rec, req, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch before save → %d (%s)", rec.Code, rec.Body.String())
	}

	// It stored a DRAFT: the metadata is in place, and the provider is not offered on the login page
	// just because someone pressed fetch.
	p, ok := s.st.SSOProviderBySlug("saml")
	if !ok {
		t.Fatal("fetch did not create the provider row")
	}
	if p.Enabled {
		t.Error("fetching metadata enabled the provider; it must save a draft")
	}
	if strings.TrimSpace(p.IdPMetadataXML) == "" || p.IdPEntityID == "" {
		t.Errorf("metadata not stored: xml=%d entity=%q", len(p.IdPMetadataXML), p.IdPEntityID)
	}
	if p.IdPMetadataURL == "" {
		t.Error("the URL the admin typed was not kept, so the next fetch would have nothing to use")
	}
}

// An edited URL must be the one fetched. Reading it from the stored row meant retyping the URL and
// pressing fetch silently re-fetched the OLD address.
func TestFetchMetadataUsesTheURLTheAdminJustTyped(t *testing.T) {
	s := adminSSOServer(t)
	hits := make(chan string, 4)
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- r.URL.Path
		w.Write([]byte(testIdPMetadata))
	}))
	defer idp.Close()
	s.allowSSOFetchTo(idp.URL)
	saveProvider(t, s, `{"kind":"saml","slug":"saml","name":"Corp","idp_metadata_url":"`+idp.URL+`/old.xml"}`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sso/providers/saml/metadata",
		strings.NewReader(`{"idp_metadata_url":"`+idp.URL+`/new.xml"}`))
	req.SetPathValue("slug", "saml")
	s.apiAdminSSOFetchMetadata(rec, req, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("fetch → %d (%s)", rec.Code, rec.Body.String())
	}
	select {
	case got := <-hits:
		if got != "/new.xml" {
			t.Errorf("fetched %q, want /new.xml — the typed URL, not the stored one", got)
		}
	default:
		t.Fatal("the IdP was never contacted")
	}
	if p, _ := s.st.SSOProviderBySlug("saml"); p.IdPMetadataURL != idp.URL+"/new.xml" {
		t.Errorf("stored URL = %q, want the new one", p.IdPMetadataURL)
	}
}

// Every failure on this path is read by an admin mid-setup, so it must carry a code the client can
// translate. A bare English sentence is what "no such provider" looked like on a Chinese screen.
func TestFetchMetadataFailuresCarryACode(t *testing.T) {
	s := adminSSOServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/sso/providers/saml/metadata", strings.NewReader(`{"kind":"saml"}`))
	req.SetPathValue("slug", "saml")
	s.apiAdminSSOFetchMetadata(rec, req, "admin")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no URL at all → %d, want 400", rec.Code)
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["code"] != "sso_no_metadata_url" {
		t.Errorf("code = %v, want sso_no_metadata_url (got body %s)", out["code"], rec.Body.String())
	}
}

// TestSSOAdminNeverReturnsSecrets is the invariant that matters most on this surface: the client
// secret and the SP private key must never leave the server, in any field, on any read. Only
// booleans saying whether they are set.
func TestSSOAdminNeverReturnsSecrets(t *testing.T) {
	s := adminSSOServer(t)
	const secret = "super-secret-client-value"
	if rec := saveProvider(t, s, `{"kind":"oidc","slug":"acme","issuer":"https://idp.example",
		"client_id":"cid","client_secret":"`+secret+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("save → %d (%s)", rec.Code, rec.Body.String())
	}
	// And a SAML provider, which additionally holds a generated private key.
	if rec := saveProvider(t, s, `{"kind":"saml","slug":"corp"}`); rec.Code != http.StatusOK {
		t.Fatalf("saml save → %d (%s)", rec.Code, rec.Body.String())
	}

	raw, provs := listProviders(t, s)
	if strings.Contains(raw, secret) {
		t.Error("the client secret was returned by the admin API")
	}
	if strings.Contains(raw, "PRIVATE KEY") || strings.Contains(raw, "enc:v1:") {
		t.Error("key material (or its sealed form) was returned by the admin API")
	}
	byslug := map[string]map[string]any{}
	for _, p := range provs {
		byslug[p["slug"].(string)] = p
	}
	if byslug["acme"]["has_client_secret"] != true {
		t.Error("the admin UI must still be told that a secret IS set")
	}
	if byslug["corp"]["has_sp_key"] != true || byslug["corp"]["sp_cert_pem"] == "" {
		t.Error("a SAML provider must get an SP keypair on first save, with the CERTIFICATE readable")
	}
	// The derived URLs the admin pastes into the IdP come from public_url, not the request.
	if byslug["corp"]["sp_acs_url"] != "https://portal.example/api/auth/saml/corp/acs" {
		t.Errorf("sp_acs_url = %v, want it derived from public_url", byslug["corp"]["sp_acs_url"])
	}
}

// TestSSOAdminBlankSecretKeepsStored proves an admin can rename or retune a provider without
// re-entering credentials, and that an explicit empty string still clears them.
func TestSSOAdminBlankSecretKeepsStored(t *testing.T) {
	s := adminSSOServer(t)
	saveProvider(t, s, `{"kind":"oidc","slug":"acme","issuer":"https://idp.example","client_id":"cid","client_secret":"s1"}`)
	before, _ := s.st.SSOProviderBySlug("acme")

	// No client_secret key at all → keep.
	saveProvider(t, s, `{"kind":"oidc","slug":"acme","name":"Renamed","issuer":"https://idp.example","client_id":"cid"}`)
	after, _ := s.st.SSOProviderBySlug("acme")
	if after.ClientSecretEnc != before.ClientSecretEnc {
		t.Error("omitting the secret must keep the stored one")
	}
	if after.Name != "Renamed" {
		t.Error("the rename did not apply")
	}
	// Explicit "" → clear.
	saveProvider(t, s, `{"kind":"oidc","slug":"acme","issuer":"https://idp.example","client_id":"cid","client_secret":""}`)
	if cleared, _ := s.st.SSOProviderBySlug("acme"); cleared.ClientSecretEnc != "" {
		t.Error("an explicit empty secret must clear the stored one")
	}
}

// TestSSOAdminValidatesBeforeEnabling proves a half-configured provider can be saved as a draft but
// never put in front of users — failing at save time is far kinder than failing mid-login.
func TestSSOAdminValidatesBeforeEnabling(t *testing.T) {
	s := adminSSOServer(t)
	// Draft: incomplete but disabled → allowed.
	if rec := saveProvider(t, s, `{"kind":"oidc","slug":"acme"}`); rec.Code != http.StatusOK {
		t.Errorf("an incomplete DISABLED provider should save as a draft: %s", rec.Body.String())
	}
	// Enabling the same incomplete provider → refused with a reason.
	rec := saveProvider(t, s, `{"kind":"oidc","slug":"acme","enabled":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("enabling an incomplete provider → %d, want 400", rec.Code)
	}
	// JIT with no default group → refused: a new account must land somewhere known.
	rec = saveProvider(t, s, `{"kind":"oidc","slug":"acme","enabled":true,"provisioning":"jit",
		"issuer":"https://idp.example","client_id":"c","client_secret":"s"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "default group") {
		t.Errorf("JIT without a default group → %d %s, want a 400 naming the group", rec.Code, rec.Body.String())
	}
	// SAML over plain http → refused, because the ACS cookie must be Secure.
	s.st.SetSetting("public_url", "http://portal.example")
	rec = saveProvider(t, s, `{"kind":"saml","slug":"corp","enabled":true}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "https") {
		t.Errorf("SAML on plain http → %d %s, want a 400 about https", rec.Code, rec.Body.String())
	}
	// No public URL at all → refused, since every derived URL depends on it.
	s.st.SetSetting("public_url", "")
	rec = saveProvider(t, s, `{"kind":"oidc","slug":"acme","enabled":true,"issuer":"https://idp.example","client_id":"c","client_secret":"s"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("enabling without a public URL → %d, want 400", rec.Code)
	}
}

// OIDC must be configurable in ONE save. Everything its enable check needs — issuer, client id,
// client secret — is a field on the same form, so unlike SAML there is no round trip to the IdP in
// the middle and therefore no order an admin has to discover. This pins that: it is the reason the
// SAML deadlock had no OIDC twin, and the reason is a property of the code, not an accident.
func TestOIDCEnablesOnTheFirstSave(t *testing.T) {
	s := adminSSOServer(t)
	s.ssoInsecureForTest = true
	s.st.SetSetting("sso_allow_private", "1")
	op := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"issuer":"x"}`))
	}))
	defer op.Close()

	body := `{"kind":"oidc","slug":"oidc","name":"Corp","enabled":true,` +
		`"issuer":"` + op.URL + `","client_id":"c","client_secret":"s","scopes":"openid profile email"}`
	if rec := saveProvider(t, s, body); rec.Code != http.StatusOK {
		t.Fatalf("first save with enabled=true → %d (%s); OIDC must not need a draft round trip",
			rec.Code, rec.Body.String())
	}
	p, ok := s.st.SSOProviderBySlug("oidc")
	if !ok || !p.Enabled || p.Name != "Corp" || p.ClientSecretEnc == "" {
		t.Errorf("stored provider is wrong: ok=%v enabled=%v name=%q secret=%v", ok, p.Enabled, p.Name, p.ClientSecretEnc != "")
	}
}

// TestSSORulesRoundTripPreservesOrder proves order survives a save, because order IS the contract:
// the first matching rule decides the role and OU.
func TestSSORulesRoundTripPreservesOrder(t *testing.T) {
	s := adminSSOServer(t)
	body := `{"rules":[
		{"enabled":true,"attr":"","value":"admins","target_role":"operator","target_group":0,"note":"first"},
		{"enabled":true,"attr":"","value":"analysts","target_role":"user","target_group":0,"note":"second"},
		{"enabled":false,"attr":"dept","value":"x","note":"third"}]}`
	rec := httptest.NewRecorder()
	s.apiAdminSSORulesSave(rec, httptest.NewRequest(http.MethodPut, "/api/admin/sso/rules", strings.NewReader(body)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("save rules → %d (%s)", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.apiAdminSSORules(rec, httptest.NewRequest(http.MethodGet, "/api/admin/sso/rules", nil), "admin")
	var resp struct {
		Rules []map[string]any `json:"rules"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(resp.Rules))
	}
	if resp.Rules[0]["note"] != "first" || resp.Rules[1]["note"] != "second" || resp.Rules[2]["note"] != "third" {
		t.Errorf("rule order was not preserved: %v", resp.Rules)
	}
	if resp.Rules[2]["enabled"] != false {
		t.Error("the disabled flag did not round-trip")
	}
	// A save REPLACES the set, so a shorter list must not leave orphans behind.
	rec = httptest.NewRecorder()
	s.apiAdminSSORulesSave(rec, httptest.NewRequest(http.MethodPut, "/api/admin/sso/rules",
		strings.NewReader(`{"rules":[{"enabled":true,"value":"only","target_role":"user"}]}`)), "admin")
	rec = httptest.NewRecorder()
	s.apiAdminSSORules(rec, httptest.NewRequest(http.MethodGet, "/api/admin/sso/rules", nil), "admin")
	resp.Rules = nil
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Rules) != 1 {
		t.Errorf("after replacing, got %d rules, want 1", len(resp.Rules))
	}
}

// TestSSOLastSeenShowsRealClaimNames covers the probe that makes attribute mapping configurable
// from reality: IdPs disagree wildly on claim names, and a wrong guess fails as "not provisioned"
// with nothing to go on. It must return the NAMES and only a truncated preview of each value — an
// assertion carries personal data an admin has no reason to read in bulk.
func TestSSOLastSeenShowsRealClaimNames(t *testing.T) {
	s := adminSSOServer(t)
	saveProvider(t, s, `{"kind":"oidc","slug":"acme","issuer":"https://idp.example","client_id":"c","client_secret":"s"}`)

	probe := func() map[string]any {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/admin/sso/providers/acme/last-seen", nil)
		req.SetPathValue("slug", "acme")
		s.apiAdminSSOLastSeen(rec, req, "admin")
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if probe()["seen"] != false {
		t.Error("before anyone signs in there is nothing to show")
	}

	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	long := strings.Repeat("x", 200)
	s.st.LinkIdentity(Identity{
		Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-1", Username: "alice", ProviderSlug: "acme",
		Attrs: `{"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/upn":"alice@acme.example",
			"groups":["analysts","admins"],"bio":"` + long + `"}`,
	})
	got := probe()
	if got["seen"] != true {
		t.Fatalf("after a sign-in the claims must be shown: %v", got)
	}
	claims, _ := got["claims"].([]any)
	byName := map[string]string{}
	for _, c := range claims {
		m := c.(map[string]any)
		byName[m["name"].(string)] = m["preview"].(string)
	}
	// The long URN form is exactly what an admin cannot guess and most needs to see.
	if _, ok := byName["http://schemas.xmlsoap.org/ws/2005/05/identity/claims/upn"]; !ok {
		t.Errorf("the real claim names must be listed: %v", byName)
	}
	if byName["groups"] != "analysts, admins" {
		t.Errorf("a multi-valued claim should read naturally, got %q", byName["groups"])
	}
	if len([]rune(byName["bio"])) > 61 {
		t.Errorf("a long value must be truncated, got %d runes", len([]rune(byName["bio"])))
	}
}
