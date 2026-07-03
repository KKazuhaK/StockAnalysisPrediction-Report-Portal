package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

func userAdminServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "test-secret"}}
	// A baseline admin: the acting user + a floor for last-admin protection.
	s.st.UpsertUser(User{Username: "admin", PasswordHash: "x", Role: "admin"})
	return s
}

func call(t *testing.T, h handler, body, actor string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)), actor)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestUserAdminEnrichedFlow(t *testing.T) {
	s := userAdminServer(t)

	// Create a group.
	_, g := call(t, s.apiGroupAdd, `{"name":"Research","description":"The desk"}`, "admin")
	gid := int64(g["id"].(float64))

	// Create a user with display name / email / group.
	code, _ := call(t, s.apiUserAdd, fmt.Sprintf(`{"username":"alice","password":"pw12345678","role":"operator","display_name":"Alice A","email":"a@x.com","groups":[%d]}`, gid), "admin")
	if code != http.StatusOK {
		t.Fatalf("apiUserAdd → %d", code)
	}

	// The enriched list surfaces the profile + membership + group with its count.
	rec := httptest.NewRecorder()
	s.apiAdminUsers(rec, httptest.NewRequest("GET", "/x", nil), "admin")
	type userRow struct {
		Username    string  `json:"username"`
		DisplayName string  `json:"display_name"`
		Email       string  `json:"email"`
		Role        string  `json:"role"`
		Active      bool    `json:"active"`
		Groups      []int64 `json:"groups"`
	}
	var lst struct {
		Users  []userRow `json:"users"`
		Groups []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Members int    `json:"members"`
		} `json:"groups"`
	}
	json.Unmarshal(rec.Body.Bytes(), &lst)
	var alice *userRow
	for i := range lst.Users {
		if lst.Users[i].Username == "alice" {
			alice = &lst.Users[i]
		}
	}
	if alice == nil || alice.DisplayName != "Alice A" || alice.Email != "a@x.com" || !alice.Active || len(alice.Groups) != 1 || alice.Groups[0] != gid {
		t.Fatalf("alice enriched row = %+v", alice)
	}
	if len(lst.Groups) != 1 || lst.Groups[0].Members != 1 {
		t.Fatalf("group list = %+v, want 1 group with 1 member", lst.Groups)
	}
}

func TestLoginGatingAndLastLogin(t *testing.T) {
	s := userAdminServer(t)
	call(t, s.apiUserAdd, `{"username":"bob","password":"pw12345678","role":"user"}`, "admin")

	login := func() int {
		rec := httptest.NewRecorder()
		s.apiLogin(rec, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"bob","password":"pw12345678"}`)))
		return rec.Code
	}

	if code := login(); code != http.StatusOK {
		t.Fatalf("enabled login → %d, want 200", code)
	}
	if u := s.st.GetUser("bob"); u.LastLogin == "" {
		t.Fatal("last_login not stamped on login")
	}

	// Disable bob → login is refused with 403.
	s.st.SetUserActive("bob", false)
	if code := login(); code != http.StatusForbidden {
		t.Fatalf("disabled login → %d, want 403", code)
	}

	// A disabled user's still-valid session is rejected mid-flight.
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: s.sign("bob")})
	rec := httptest.NewRecorder()
	s.apiMe(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("apiMe for disabled user → %d, want 401", rec.Code)
	}
}

func TestBulkActionsAndGuards(t *testing.T) {
	s := userAdminServer(t)
	call(t, s.apiUserAdd, `{"username":"u1","password":"pw12345678","role":"user"}`, "admin")
	call(t, s.apiUserAdd, `{"username":"u2","password":"pw12345678","role":"user"}`, "admin")

	// Bulk disable u1 + u2.
	code, r := call(t, s.apiUsersBulk, `{"action":"disable","usernames":["u1","u2"]}`, "admin")
	if code != http.StatusOK || int(r["n"].(float64)) != 2 {
		t.Fatalf("bulk disable → %d n=%v", code, r["n"])
	}
	if s.st.GetUser("u1").Active || s.st.GetUser("u2").Active {
		t.Fatal("bulk disable did not stick")
	}

	// Bulk cannot disable the last admin or yourself.
	_, r2 := call(t, s.apiUsersBulk, `{"action":"disable","usernames":["admin"]}`, "admin")
	if int(r2["n"].(float64)) != 0 || !s.st.GetUser("admin").Active {
		t.Fatalf("last admin was disabled by bulk: n=%v active=%v", r2["n"], s.st.GetUser("admin").Active)
	}

	// Bulk add-to-group.
	_, g := call(t, s.apiGroupAdd, `{"name":"Ops"}`, "admin")
	gid := int64(g["id"].(float64))
	call(t, s.apiUsersBulk, fmt.Sprintf(`{"action":"add_group","usernames":["u1","u2"],"group_id":%d}`, gid), "admin")
	if len(s.st.GroupsOf("u1")) != 1 || len(s.st.GroupsOf("u2")) != 1 {
		t.Fatal("bulk add_group did not assign both users")
	}
}
