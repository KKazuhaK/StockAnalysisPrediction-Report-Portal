package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The group-rules admin API. The rules decide role AND organizational unit, and in this portal the
// OU carries report visibility, the run allow-list and the daily quota — so this endpoint is where
// a federated user's actual permissions are set.

func rulesGET(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiAdminSSORules(rec, httptest.NewRequest(http.MethodGet, "/api/admin/sso/rules", nil), "admin")
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET rules: %v (%s)", err, rec.Body.String())
	}
	return out
}

func rulesPUT(t *testing.T, s *Server, body string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiAdminSSORulesSave(rec, httptest.NewRequest(http.MethodPut, "/api/admin/sso/rules",
		strings.NewReader(body)), "admin")
	return rec.Code
}

// TestSSORulesRoundTripThroughTheAPI proves the rules an admin saves are the rules the login path
// resolves against. Store-level tests could not catch a rule set that is saved but never consulted.
func TestSSORulesRoundTripThroughTheAPI(t *testing.T) {
	s := tenancyServer(t)
	ou, _ := s.st.CreateUserGroup("clients", "", 0)

	if code := rulesPUT(t, s, `{"rules":[
		{"provider_id":0,"enabled":true,"attr":"groups","value":"staff","target_role":"operator"},
		{"provider_id":0,"enabled":true,"attr":"groups","value":"clients","target_group":`+itoa(ou)+`}
	]}`); code != http.StatusOK {
		t.Fatalf("PUT rules → %d", code)
	}
	got := rulesGET(t, s)
	list, _ := got["rules"].([]any)
	if len(list) != 2 {
		t.Fatalf("GET returned %d rules, want 2", len(list))
	}
	// Order is the contract — first match wins — so it must survive the round trip.
	first, _ := list[0].(map[string]any)
	if first["value"] != "staff" {
		t.Errorf("rule order was not preserved: first rule is %v", first["value"])
	}
	// And the engine must actually see them, which is the half that was missing.
	if n := len(s.st.rulesFor(0)); n != 2 {
		t.Errorf("the login path resolves against %d rules, want 2", n)
	}
}

// TestSSORulesReportShadowedRules covers the warning the page shows. A rule that can never win looks
// to an admin exactly like a permission they granted.
func TestSSORulesReportShadowedRules(t *testing.T) {
	s := tenancyServer(t)
	if code := rulesPUT(t, s, `{"rules":[
		{"provider_id":0,"enabled":true,"attr":"groups","value":"staff","target_role":"operator"},
		{"provider_id":0,"enabled":true,"attr":"groups","value":"staff","target_role":"admin"}
	]}`); code != http.StatusOK {
		t.Fatalf("PUT rules → %d", code)
	}
	shadowed, _ := rulesGET(t, s)["shadowed"].([]any)
	if len(shadowed) != 1 {
		t.Fatalf("shadowed = %v, want the second rule", shadowed)
	}

	// A distinct value is not shadowed.
	rulesPUT(t, s, `{"rules":[
		{"provider_id":0,"enabled":true,"attr":"groups","value":"staff","target_role":"operator"},
		{"provider_id":0,"enabled":true,"attr":"groups","value":"clients","target_role":"user"}
	]}`)
	if sh, _ := rulesGET(t, s)["shadowed"].([]any); len(sh) != 0 {
		t.Errorf("shadowed = %v on a rule set where every rule is reachable", sh)
	}
}

// TestSSORulesSaveReplacesTheWholeSet pins the replace semantics: the array IS the order, and a save
// that merged instead of replacing would leave a deleted rule still granting access.
func TestSSORulesSaveReplacesTheWholeSet(t *testing.T) {
	s := tenancyServer(t)
	rulesPUT(t, s, `{"rules":[{"provider_id":0,"enabled":true,"attr":"groups","value":"staff","target_role":"admin"}]}`)
	if code := rulesPUT(t, s, `{"rules":[]}`); code != http.StatusOK {
		t.Fatalf("PUT empty → %d", code)
	}
	if list, _ := rulesGET(t, s)["rules"].([]any); len(list) != 0 {
		t.Errorf("%d rules survived a save that removed them all", len(list))
	}
	if n := len(s.st.rulesFor(0)); n != 0 {
		t.Errorf("the login path still resolves against %d deleted rules", n)
	}
}
