package app

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTokenCreateReturnsSecretOnceAndListReturnsPrefix(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	created := httptest.NewRecorder()
	s.apiTokenAdd(created, httptest.NewRequest("POST", "/api/admin/tokens", strings.NewReader(`{"name":"ci","scope":"query"}`)), "admin")
	var createBody struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createBody); err != nil {
		t.Fatal(err)
	}
	if len(createBody.Token) != 48 {
		t.Fatalf("created token length = %d, want 48", len(createBody.Token))
	}
	if got := created.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("token create Cache-Control = %q", got)
	}

	listed := httptest.NewRecorder()
	s.apiAdminTokens(listed, httptest.NewRequest("GET", "/api/admin/tokens", nil), "admin")
	var listBody struct {
		Tokens []struct {
			Prefix string `json:"prefix"`
			Token  string `json:"token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listBody); err != nil {
		t.Fatal(err)
	}
	if len(listBody.Tokens) != 1 || listBody.Tokens[0].Prefix != createBody.Token[:8] || listBody.Tokens[0].Token != "" {
		t.Fatalf("listed token = %+v", listBody.Tokens)
	}
}

func TestTokenCreateRejectsUnknownScope(t *testing.T) {
	s := &Server{st: newTestStore(t)}
	w := httptest.NewRecorder()
	s.apiTokenAdd(w, httptest.NewRequest("POST", "/api/admin/tokens", strings.NewReader(`{"scope":"admin"}`)), "admin")
	if w.Code != 400 || s.st.CountTokens() != 0 {
		t.Fatalf("unknown scope response = %d, tokens=%d", w.Code, s.st.CountTokens())
	}
}
