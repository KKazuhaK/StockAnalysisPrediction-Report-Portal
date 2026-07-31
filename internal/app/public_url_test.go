package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The public URL is a property of the DEPLOYMENT, not of email.
//
// It lived on the email page because password-reset links were the first thing to need an origin
// that a forged Host header cannot poison. Six more features have since come to depend on it — the
// SAML entity id and ACS URL, the OIDC redirect URL, the WebAuthn relying-party id, registration
// confirmation links and the captcha hostname check — and the SSO validator had to tell admins
// where to find it ("Manage → Email"), which is what a misfiled setting looks like.
//
// The meta key does not move, so nothing stored has to change and every reader keeps working.

func settingsRoundTrip(t *testing.T, s *Server, body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiSettingsSave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/settings",
		strings.NewReader(body)), "admin")
	return rec.Code
}

func TestPublicURLIsAGeneralSetting(t *testing.T) {
	s := tenancyServer(t)

	if code := settingsRoundTrip(t, s, `{"publicUrl":"https://portal.example.com/"}`); code != http.StatusOK {
		t.Fatalf("saving the public URL on the general settings → %d", code)
	}
	// Every consumer reads it through one accessor, and it is normalized there (no trailing slash),
	// so a URL typed with one still produces a valid redirect target.
	if got := s.publicBaseURL(); got != "https://portal.example.com" {
		t.Errorf("publicBaseURL() = %q after saving via general settings", got)
	}

	rec := httptest.NewRecorder()
	s.apiAdminSettings(rec, httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil), "admin")
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["publicUrl"] != "https://portal.example.com/" {
		t.Errorf("general settings reports publicUrl = %v", out["publicUrl"])
	}
}

// Saving the general settings without mentioning it must not wipe it — the field is a pointer for
// the same reason the SMTP password is: an omitted key means "leave alone", not "clear".
func TestPublicURLSurvivesAnUnrelatedSettingsSave(t *testing.T) {
	s := tenancyServer(t)
	s.st.SetSetting("public_url", "https://portal.example.com")
	if code := settingsRoundTrip(t, s, `{"siteTitle":"安禅AI投研"}`); code != http.StatusOK {
		t.Fatalf("saving unrelated settings → %d", code)
	}
	if got := s.publicBaseURL(); got != "https://portal.example.com" {
		t.Errorf("an unrelated save cleared the public URL: %q", got)
	}
}

// The email page no longer owns it, but a portal upgrading has the value already stored under the
// same key, so nothing needs re-entering.
func TestPublicURLReadsTheExistingStoredValue(t *testing.T) {
	s := tenancyServer(t)
	s.st.SetSetting("public_url", "https://old.example.org/")
	if got := s.publicBaseURL(); got != "https://old.example.org" {
		t.Errorf("a value stored before the move is not read: %q", got)
	}
}
