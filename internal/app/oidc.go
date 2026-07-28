package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDC relying-party login (ADR 0023).
//
// go-oidc verifies the ID token's signature, issuer, audience and expiry. It does NOT check the
// nonce (it merely populates the field) and it does not know about our state, PKCE or return path.
// Everything it leaves to us is done here, explicitly, and each one has a test that breaks exactly
// that check.

const (
	oidcCookiePrefix = "rp_sso_oidc_"
	ssoFlowTTL       = 10 * time.Minute
	oidcMaxSkew      = 2 * time.Minute
)

// ssoClient builds the SSRF-guarded HTTP client used for every outbound call in a flow, including
// the ones go-oidc makes for us (discovery, JWKS, token) via oidc.ClientContext.
func (s *Server) ssoClient() *safeClient {
	c := newSafeClient(s.st.GetSetting("sso_allow_private", "") == "1")
	c.allowInsecureForTest = s.ssoInsecureForTest
	return c
}

func (s *Server) ssoContext(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, s.ssoClient().http)
}

// enabledProvider loads a provider that may actually be used right now. A missing, disabled or
// wrong-kind slug is indistinguishable from "no such route": SSO being off must not be detectable.
func (s *Server) enabledProvider(slug, kind string) (SSOProvider, bool) {
	p, ok := s.st.SSOProviderBySlug(slug)
	if !ok || !p.Enabled || p.Kind != kind {
		return SSOProvider{}, false
	}
	return p, true
}

// oidcRuntime is a provider plus its oauth2 config, rebuilt per request. Discovery is cached in the
// provider row, so this does not hit the network on every login.
type oidcRuntime struct {
	prov     *oidc.Provider
	verifier *oidc.IDTokenVerifier
	conf     oauth2.Config
}

func (s *Server) oidcRuntimeFor(ctx context.Context, p SSOProvider) (*oidcRuntime, error) {
	secret, err := s.openSecret(p.Slug, "oidc_client_secret", p.ClientSecretEnc)
	if err != nil {
		return nil, fmt.Errorf("client secret unavailable: %w", err)
	}
	prov, err := oidc.NewProvider(s.ssoContext(ctx), p.Issuer)
	if err != nil {
		return nil, err
	}
	return &oidcRuntime{
		prov: prov,
		// SkipClientIDCheck / SkipExpiryCheck / SkipIssuerCheck are deliberately left false and are
		// never exposed as configuration: they turn verification off.
		verifier: prov.Verifier(&oidc.Config{ClientID: p.ClientID}),
		conf: oauth2.Config{
			ClientID: p.ClientID, ClientSecret: secret, Endpoint: prov.Endpoint(),
			RedirectURL: s.oidcRedirectURL(p.Slug), Scopes: p.EffectiveScopes(),
		},
	}, nil
}

// oidcRedirectURL derives the callback from the configured public URL — never from the request
// host, which an attacker controls via the Host header. It is the same value the admin pastes into
// the IdP, and the same one the token exchange must present.
func (s *Server) oidcRedirectURL(slug string) string {
	return s.publicBaseURL() + "/api/auth/oidc/" + url.PathEscape(slug) + "/callback"
}

// GET /api/sso/providers — what the login page may offer. Public by necessity (it is read before
// login) and deliberately minimal: no issuer, no client id, nothing about configuration.
func (s *Server) apiSSOProviders(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	for _, p := range s.st.EnabledSSOProviders() {
		out = append(out, map[string]any{"slug": p.Slug, "kind": p.Kind, "name": firstNonEmpty(p.Name, p.Slug)})
	}
	writeJSON(w, map[string]any{"providers": out})
}

// GET /api/auth/oidc/{slug}/start — begin a login.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p, ok := s.enabledProvider(slug, "oidc")
	if !ok {
		http.NotFound(w, r)
		return
	}
	rt, err := s.oidcRuntimeFor(r.Context(), p)
	if err != nil {
		log.Printf("sso: oidc %s start: %v", slug, err)
		s.ssoFail(w, r, "provider_unavailable")
		return
	}
	token, err := newAuthToken()
	if err != nil {
		s.ssoFail(w, r, "internal")
		return
	}
	nonce, err := newAuthToken()
	if err != nil {
		s.ssoFail(w, r, "internal")
		return
	}
	verifier := oauth2.GenerateVerifier()
	// The pending row is the single-use record; the cookie below binds it to this browser. Both are
	// required: the row proves the callback answers a request we issued, the cookie proves it came
	// back to the browser that issued it (login CSRF).
	if err := s.st.CreateAuthRequest(AuthRequest{
		Token: token, ProviderID: p.ID, Kind: "oidc", Nonce: nonce, Verifier: verifier,
		Target: safeReturnPath(r.URL.Query().Get("next")),
	}, time.Now().Add(ssoFlowTTL)); err != nil {
		s.ssoFail(w, r, "internal")
		return
	}
	http.SetCookie(w, s.ssoFlowCookie(slug, token, int(ssoFlowTTL.Seconds())))
	http.Redirect(w, r, rt.conf.AuthCodeURL(token,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
		// Explicit: form_post would make the callback a cross-site POST, which a Lax cookie is
		// not sent on.
		oauth2.SetAuthURLParam("response_mode", "query"),
	), http.StatusFound)
}

// GET /api/auth/oidc/{slug}/callback — finish a login.
//
// The ORDER here is the security property: nothing attacker-supplied reaches the token endpoint or
// the JWKS fetcher until the state matches and the single-use row has been claimed.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	// 1. Clear the flow cookie unconditionally and first, so no failure path can leave it usable.
	http.SetCookie(w, s.ssoFlowCookie(slug, "", -1))

	p, ok := s.enabledProvider(slug, "oidc")
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	// 2. An IdP-reported error ends here. error_description is attacker-influenced text and is
	// never echoed — only a code we chose.
	if q.Get("error") != "" {
		log.Printf("sso: oidc %s returned error %q", slug, q.Get("error"))
		s.ssoFail(w, r, "idp_error")
		return
	}
	// 3. Constant-time state compare against the browser-bound cookie.
	ck, err := r.Cookie(oidcCookiePrefix + slug)
	state := q.Get("state")
	if err != nil || ck.Value == "" || state == "" ||
		subtle.ConstantTimeCompare([]byte(ck.Value), []byte(state)) != 1 {
		s.ssoFail(w, r, "bad_state")
		return
	}
	// 4. Claim the pending row. This is the single-use gate: a replayed callback loses here.
	req, ok := s.st.ConsumeAuthRequest(state, time.Now())
	if !ok || req.Kind != "oidc" || req.ProviderID != p.ID {
		s.ssoFail(w, r, "bad_state")
		return
	}
	// 5. RFC 9207: if the OP told us which issuer answered, it must be the one we asked.
	if iss := q.Get("iss"); iss != "" && iss != p.Issuer {
		s.ssoFail(w, r, "bad_issuer")
		return
	}
	code := q.Get("code")
	if code == "" {
		s.ssoFail(w, r, "bad_state")
		return
	}

	ctx := s.ssoContext(r.Context())
	rt, err := s.oidcRuntimeFor(ctx, p)
	if err != nil {
		log.Printf("sso: oidc %s callback: %v", slug, err)
		s.ssoFail(w, r, "provider_unavailable")
		return
	}
	// 6. Only now is the code exchanged, with the PKCE verifier we stored server-side.
	tok, err := rt.conf.Exchange(ctx, code, oauth2.VerifierOption(req.Verifier))
	if err != nil {
		log.Printf("sso: oidc %s token exchange failed: %v", slug, err)
		s.ssoFail(w, r, "exchange_failed")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		s.ssoFail(w, r, "no_id_token")
		return
	}
	idt, err := rt.verifier.Verify(ctx, rawID)
	if err != nil {
		log.Printf("sso: oidc %s id token rejected: %v", slug, err)
		s.ssoFail(w, r, "bad_token")
		return
	}
	// 7. The checks go-oidc does NOT make.
	if err := verifyOIDCExtras(idt, req.Nonce, p.ClientID); err != nil {
		log.Printf("sso: oidc %s id token rejected: %v", slug, err)
		s.ssoFail(w, r, "bad_token")
		return
	}

	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		s.ssoFail(w, r, "bad_token")
		return
	}
	s.completeSSOLogin(w, r, p, ssoIdentity{
		Provider: "oidc", Issuer: idt.Issuer, Subject: idt.Subject, Claims: claims,
	}, req.Target)
}

// verifyOIDCExtras performs the validations go-oidc leaves to the caller.
func verifyOIDCExtras(idt *oidc.IDToken, wantNonce, clientID string) error {
	// The nonce MUST be present and MUST match. go-oidc populates IDToken.Nonce and never compares
	// it; writing this as `if n != "" && n != want` is the classic bug that silently passes when
	// the OP omits the claim entirely, so presence is checked first and separately.
	if idt.Nonce == "" {
		return fmt.Errorf("id token carries no nonce")
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(wantNonce)) != 1 {
		return fmt.Errorf("id token nonce does not match this login")
	}
	// azp: when the token has several audiences, the authorized party must be us.
	var extra struct {
		AZP string `json:"azp"`
	}
	if err := idt.Claims(&extra); err == nil && extra.AZP != "" && extra.AZP != clientID {
		return fmt.Errorf("id token azp is another client")
	}
	// iat sanity: a token issued far in the future indicates a broken or hostile OP clock.
	if !idt.IssuedAt.IsZero() && time.Until(idt.IssuedAt) > oidcMaxSkew {
		return fmt.Errorf("id token was issued in the future")
	}
	return nil
}

// ssoFlowCookie is the short-lived browser binding for a login in flight. Scoped to the one
// callback path so it is never sent anywhere else, and SameSite=Lax because the OIDC callback is a
// top-level GET. It never touches rp_session.
func (s *Server) ssoFlowCookie(slug, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: oidcCookiePrefix + slug, Value: value,
		Path:     "/api/auth/oidc/" + url.PathEscape(slug) + "/callback",
		HttpOnly: true, Secure: strings.HasPrefix(s.publicBaseURL(), "https://"),
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	}
}

// ssoFail ends a failed login at a fixed SPA route with a code WE chose. Nothing from the IdP is
// reflected, and the codes are deliberately coarse: unknown user, inactive, expired and no-rule-
// match must be indistinguishable, or the page becomes a user-enumeration oracle against the IdP.
func (s *Server) ssoFail(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?sso_error="+url.QueryEscape(code), http.StatusFound)
}

// safeReturnPath keeps a post-login landing spot to a relative path inside this app. Anything that
// could leave the origin — absolute, protocol-relative, or the backslash form browsers normalise
// into one — collapses to the home page.
func safeReturnPath(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") {
		return "/"
	}
	if strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return next
}

// ssoIdentity is what a finished protocol handshake produced, before any mapping.
type ssoIdentity struct {
	Provider string
	Issuer   string
	Subject  string
	Claims   map[string]any
}

// claim reads a mapped attribute as a string.
func (id ssoIdentity) claim(name string) string {
	if name == "" {
		return ""
	}
	switch v := id.Claims[name].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	}
	return ""
}

// groups reads the mapped groups attribute, accepting the several shapes IdPs use.
func (id ssoIdentity) groups(name string) []string {
	if name == "" {
		return nil
	}
	switch v := id.Claims[name].(type) {
	case []any:
		var out []string
		for _, g := range v {
			if s, ok := g.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		return strings.Split(v, ",")
	}
	return nil
}

// attrs serializes the claims for admin diagnostics ("what did the IdP actually send?").
func (id ssoIdentity) attrs() string {
	b, err := json.Marshal(id.Claims)
	if err != nil {
		return ""
	}
	return string(b)
}
