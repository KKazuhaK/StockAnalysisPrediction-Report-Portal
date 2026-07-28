package app

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// signRS256 hand-rolls a compact JWS. RS256 is just PKCS#1 v1.5 over SHA-256, so this keeps the
// test harness free of a JWT dependency the production code does not use.
func signRS256(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(header) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// fakeOP is a minimal OpenID Provider: discovery, JWKS and a token endpoint that mints an ID token
// we control field by field, so each verification step can be tested by breaking exactly one thing.
type fakeOP struct {
	srv           *httptest.Server
	key           *rsa.PrivateKey
	claims        map[string]any // overrides applied to the next minted ID token
	discoveryDown bool           // simulate an IdP whose well-known endpoint stops answering
}

func newFakeOP(t *testing.T) *fakeOP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	op := &fakeOP{key: key, claims: map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if op.discoveryDown {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                op.srv.URL,
			"authorization_endpoint":                op.srv.URL + "/auth",
			"token_endpoint":                        op.srv.URL + "/token",
			"jwks_uri":                              op.srv.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{
			{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1", "n": n, "e": e}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "id_token": op.idToken(t)})
	})
	op.srv = httptest.NewServer(mux)
	t.Cleanup(op.srv.Close)
	return op
}

// idToken mints an ID token with sane defaults, overridden by whatever the test set in claims.
func (op *fakeOP) idToken(t *testing.T) string {
	t.Helper()
	c := map[string]any{
		"iss": op.srv.URL, "aud": "client-1", "sub": "subject-1",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"email": "alice@acme.example", "name": "Alice",
	}
	for k, v := range op.claims {
		if v == nil {
			delete(c, k) // the test wants the claim ABSENT, not empty
			continue
		}
		c[k] = v
	}
	return signRS256(t, op.key, map[string]any{"alg": "RS256", "typ": "JWT", "kid": "k1"}, c)
}

// oidcFixture wires a server with an enabled OIDC provider pointed at the fake OP.
func oidcFixture(t *testing.T) (*Server, *fakeOP) {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	s.ssoInsecureForTest = true // allow the plain-http loopback fake OP
	root := st.EnsureDefaultGroup()
	st.SetSetting("public_url", "https://portal.example")

	op := newFakeOP(t)
	enc, err := s.sealSecret("acme", "oidc_client_secret", "client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSSOProvider(SSOProvider{
		Kind: "oidc", Slug: "acme", Name: "Acme", Enabled: true, Provisioning: "jit",
		DefaultGroup: root, DefaultRole: "user",
		Issuer: op.srv.URL, ClientID: "client-1", ClientSecretEnc: enc,
		Scopes: "openid profile email", AttrUPN: "email", AttrEmail: "email", AttrDisplay: "name",
	}); err != nil {
		t.Fatal(err)
	}
	return s, op
}

// start runs the login-start handler and returns the redirect plus the flow cookie. It also feeds
// the requested nonce back to the fake OP, which never sees the authorization request — mirroring a
// real OP, which echoes the nonce it was given. A test that wants a bad nonce overrides it after.
func start(t *testing.T, s *Server, op *fakeOP, slug string) (*url.URL, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/"+slug+"/start", nil)
	req.SetPathValue("slug", slug)
	rec := httptest.NewRecorder()
	s.oidcStart(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("start → %d (%s), want a redirect", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("start must set the flow-binding cookie")
	}
	if op != nil {
		op.claims["nonce"] = loc.Query().Get("nonce")
	}
	return loc, rec.Result().Cookies()[0]
}

// refused reports whether a finished callback ended at the login page with an error rather than
// landing in the app. Both outcomes are 302s, so the Location is what distinguishes them.
func refused(rec *httptest.ResponseRecorder) bool {
	return rec.Code != http.StatusFound || strings.Contains(rec.Header().Get("Location"), "sso_error")
}

// callback replays the IdP's redirect back to us.
func callback(s *Server, slug, state, code string, ck *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/"+slug+"/callback?state="+
		url.QueryEscape(state)+"&code="+url.QueryEscape(code), nil)
	req.SetPathValue("slug", slug)
	if ck != nil {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	s.oidcCallback(rec, req)
	return rec
}

// TestOIDCStartIssuesBoundRequest locks the start leg: PKCE S256, a nonce, and a state that is
// bound to a browser cookie — so a callback cannot be forged into someone else's session.
func TestOIDCStartIssuesBoundRequest(t *testing.T) {
	s, op := oidcFixture(t)
	loc, ck := start(t, s, op, "acme")

	if !strings.HasPrefix(loc.String(), op.srv.URL+"/auth") {
		t.Errorf("redirect = %s, want the OP authorization endpoint", loc)
	}
	q := loc.Query()
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Error("PKCE S256 must be requested")
	}
	if q.Get("nonce") == "" {
		t.Error("a nonce must be requested")
	}
	if q.Get("response_mode") != "query" {
		t.Error("response_mode=query must be explicit — a form_post callback is a cross-site POST")
	}
	if q.Get("state") == "" || q.Get("state") != ck.Value {
		t.Error("state must match the flow cookie, binding the callback to this browser")
	}
	if !ck.HttpOnly || ck.SameSite != http.SameSiteLaxMode {
		t.Errorf("flow cookie = %+v, want HttpOnly and SameSite=Lax", ck)
	}
}

// TestOIDCCallbackHappyPath proves a well-formed callback provisions and signs the user in.
func TestOIDCCallbackHappyPath(t *testing.T) {
	s, op := oidcFixture(t)
	loc, ck := start(t, s, op, "acme")
	rec := callback(s, "acme", loc.Query().Get("state"), "code-1", ck)

	if refused(rec) {
		t.Fatalf("callback was refused: %d → %s", rec.Code, rec.Header().Get("Location"))
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			session = c
		}
	}
	if session == nil || session.Value == "" {
		t.Fatal("a portal session cookie must be issued")
	}
	// The account was JIT-created, linked, and marked as owned by this provider.
	u := s.st.GetUser("alice")
	if u == nil {
		t.Fatal("the user was not provisioned from the UPN mapping")
	}
	if u.Source != "jit" || u.SourceRef != "acme" {
		t.Errorf("provisioned user = source %q/%q, want jit/acme", u.Source, u.SourceRef)
	}
	if got, ok := s.st.FindIdentity("oidc", strings.TrimSuffix(u.Email, ""), "subject-1"); ok && got != "alice" {
		t.Errorf("identity link = %q", got)
	}
}

// TestOIDCCallbackRejections is the must-fail-closed matrix. Each case breaks exactly one thing.
func TestOIDCCallbackRejections(t *testing.T) {
	t.Run("state missing", func(t *testing.T) {
		s, op := oidcFixture(t)
		_, ck := start(t, s, op, "acme")
		if rec := callback(s, "acme", "", "c", ck); !refused(rec) {
			t.Error("a callback with no state must be refused")
		}
	})
	t.Run("state mismatched", func(t *testing.T) {
		s, op := oidcFixture(t)
		_, ck := start(t, s, op, "acme")
		if rec := callback(s, "acme", "not-the-state", "c", ck); !refused(rec) {
			t.Error("a callback whose state does not match the cookie must be refused")
		}
	})
	t.Run("cookie missing", func(t *testing.T) {
		s, op := oidcFixture(t)
		loc, _ := start(t, s, op, "acme")
		if rec := callback(s, "acme", loc.Query().Get("state"), "c", nil); !refused(rec) {
			t.Error("a callback without the browser-binding cookie must be refused")
		}
	})
	t.Run("replayed", func(t *testing.T) {
		s, op := oidcFixture(t)
		loc, ck := start(t, s, op, "acme")
		state := loc.Query().Get("state")
		if rec := callback(s, "acme", state, "c", ck); refused(rec) {
			t.Fatalf("setup: first callback should succeed, got %d %s", rec.Code, rec.Header().Get("Location"))
		}
		if rec := callback(s, "acme", state, "c", ck); !refused(rec) {
			t.Error("replaying a consumed callback must be refused")
		}
	})
	t.Run("nonce absent", func(t *testing.T) {
		s, op := oidcFixture(t)
		loc, ck := start(t, s, op, "acme")
		op.claims["nonce"] = nil // the OP omits it entirely
		if rec := callback(s, "acme", loc.Query().Get("state"), "c", ck); !refused(rec) {
			t.Error("an ID token with NO nonce must be refused (go-oidc does not check it)")
		}
	})
	t.Run("nonce mismatched", func(t *testing.T) {
		s, op := oidcFixture(t)
		loc, ck := start(t, s, op, "acme")
		op.claims["nonce"] = "someone-elses-nonce"
		if rec := callback(s, "acme", loc.Query().Get("state"), "c", ck); !refused(rec) {
			t.Error("an ID token with the wrong nonce must be refused")
		}
	})
	t.Run("wrong audience", func(t *testing.T) {
		s, op := oidcFixture(t)
		loc, ck := start(t, s, op, "acme")
		op.claims["aud"] = "another-client"
		if rec := callback(s, "acme", loc.Query().Get("state"), "c", ck); !refused(rec) {
			t.Error("an ID token minted for another client must be refused")
		}
	})
	t.Run("expired", func(t *testing.T) {
		s, op := oidcFixture(t)
		loc, ck := start(t, s, op, "acme")
		op.claims["exp"] = time.Now().Add(-time.Hour).Unix()
		if rec := callback(s, "acme", loc.Query().Get("state"), "c", ck); !refused(rec) {
			t.Error("an expired ID token must be refused")
		}
	})
	t.Run("idp error is not reflected", func(t *testing.T) {
		s, op := oidcFixture(t)
		_, ck := start(t, s, op, "acme")
		req := httptest.NewRequest(http.MethodGet,
			"/api/auth/oidc/acme/callback?error=access_denied&error_description=%3Cscript%3Ealert(1)%3C/script%3E", nil)
		req.SetPathValue("slug", "acme")
		req.AddCookie(ck)
		rec := httptest.NewRecorder()
		s.oidcCallback(rec, req)
		if strings.Contains(rec.Body.String(), "<script>") || strings.Contains(rec.Header().Get("Location"), "script") {
			t.Error("an IdP-supplied error_description must never be reflected back")
		}
	})
	t.Run("disabled provider is invisible", func(t *testing.T) {
		s, _ := oidcFixture(t)
		p, _ := s.st.SSOProviderBySlug("acme")
		p.Enabled = false
		s.st.SaveSSOProvider(p)
		req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/acme/start", nil)
		req.SetPathValue("slug", "acme")
		rec := httptest.NewRecorder()
		s.oidcStart(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("a disabled provider → %d, want 404", rec.Code)
		}
	})
}

// TestOIDCReturnPathMustBeRelative proves the post-login landing spot cannot be turned into an
// open redirect, including the protocol-relative and backslash forms browsers normalise oddly.
func TestOIDCReturnPathMustBeRelative(t *testing.T) {
	for _, bad := range []string{"https://evil.example/x", "//evil.example/x", "/\\evil.example", "http:/evil"} {
		if got := safeReturnPath(bad); got != "/" {
			t.Errorf("safeReturnPath(%q) = %q, want / (open-redirect guard)", bad, got)
		}
	}
	for _, ok := range []string{"/stock/600000", "/run/x%7C2026-01-01", "/"} {
		if got := safeReturnPath(ok); got != ok {
			t.Errorf("safeReturnPath(%q) = %q, want it preserved", ok, got)
		}
	}
}

// TestOIDCDiscoveryIsCached proves a login does not depend on the IdP's well-known endpoint being
// reachable every time: the document is cached on first use, and a later sign-in still works when
// discovery has gone away. Without this, a slow or briefly-down IdP takes sign-in with it.
func TestOIDCDiscoveryIsCached(t *testing.T) {
	s, op := oidcFixture(t)
	loc, ck := start(t, s, op, "acme")
	if rec := callback(s, "acme", loc.Query().Get("state"), "c", ck); refused(rec) {
		t.Fatalf("first login was refused: %s", rec.Header().Get("Location"))
	}
	p, _ := s.st.SSOProviderBySlug("acme")
	if p.DiscoveryJSON == "" {
		t.Fatal("the discovery document must be cached after the first use")
	}

	// Take the OP's discovery endpoint away entirely; everything else keeps working.
	op.discoveryDown = true
	loc, ck = start(t, s, op, "acme")
	if rec := callback(s, "acme", loc.Query().Get("state"), "c", ck); refused(rec) {
		t.Errorf("a login must survive discovery being unreachable once cached: %s", rec.Header().Get("Location"))
	}
}
