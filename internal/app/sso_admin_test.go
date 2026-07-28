package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

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
