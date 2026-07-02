package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The operator role runs batch jobs but has no admin access; admin holds both.
func TestOperatorPermissionMatrix(t *testing.T) {
	cases := []struct {
		role, perm string
		want       bool
	}{
		{"admin", PermManage, true},
		{"admin", PermRunBatch, true},
		{"operator", PermRunBatch, true},
		{"operator", PermManage, false},
		{"user", PermRunBatch, false},
		{"user", PermManage, false},
	}
	for _, c := range cases {
		if got := can(c.role, c.perm); got != c.want {
			t.Errorf("can(%q, %q) = %v, want %v", c.role, c.perm, got, c.want)
		}
	}
}

// requirePermJSON: 200 for holders, 403 for a logged-in non-holder, 401 for anon.
func TestRequirePermJSON(t *testing.T) {
	st := newTestStore(t)
	st.UpsertUser(User{Username: "adm", Role: "admin"})
	st.UpsertUser(User{Username: "op", Role: "operator"})
	st.UpsertUser(User{Username: "viewer", Role: "user"})
	srv := &Server{st: st, cfg: &config.Config{SecretKey: "test-secret"}}

	h := srv.requirePermJSON(PermRunBatch, func(w http.ResponseWriter, r *http.Request, user string) {
		writeJSON(w, map[string]any{"ok": true, "user": user})
	})
	call := func(user string) int {
		req := httptest.NewRequest("POST", "/x", nil)
		if user != "" {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: srv.sign(user)})
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}
	if c := call("op"); c != http.StatusOK {
		t.Errorf("operator → %d, want 200", c)
	}
	if c := call("adm"); c != http.StatusOK {
		t.Errorf("admin → %d, want 200", c)
	}
	if c := call("viewer"); c != http.StatusForbidden {
		t.Errorf("user → %d, want 403", c)
	}
	if c := call(""); c != http.StatusUnauthorized {
		t.Errorf("anon → %d, want 401", c)
	}
}
