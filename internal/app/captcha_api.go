package app

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/captcha"
)

// Captcha configuration and the gate that public forms pass through.
//
// Settings live in `meta`, like every other feature setting, so this adds no schema. The secret key
// is sealed with the same envelope crypto the SSO client secrets use (ADR 0023) and is never
// returned by the admin GET — only whether one is stored.
//
// Three contexts are gated independently, because they have different exposure: login is attacked
// by credential stuffing, forgot-password is an outbound-email amplifier, and registration is an
// account-creation flood. An admin should be able to protect one without the others.

const (
	setCaptchaProvider  = "captcha_provider"
	setCaptchaSiteKey   = "captcha_site_key"
	setCaptchaSecret    = "captcha_secret_enc"
	setCaptchaLogin     = "captcha_login"
	setCaptchaForgot    = "captcha_forgot"
	setCaptchaRegister  = "captcha_register"
	setCaptchaTrigger   = "captcha_trigger"
	setCaptchaThreshold = "captcha_fail_threshold"
)

// Captcha trigger modes for the LOGIN form. The other two contexts are always-on when enabled:
// there is no per-account failure history before an account exists, and a forgot-password flood
// targets addresses rather than accounts.
const (
	triggerAlways       = "always"
	triggerAfterFailure = "after_failures"
)

// captchaContext names a gated form.
type captchaContext string

const (
	ctxLogin    captchaContext = "login"
	ctxForgot   captchaContext = "forgot"
	ctxRegister captchaContext = "register"
)

// captchaSettings resolves the live configuration. Read per request so an admin's change takes
// effect without a restart.
func (s *Server) captchaSettings() captcha.Settings {
	secret := ""
	if sealed := s.st.GetSetting(setCaptchaSecret, ""); sealed != "" {
		if v, err := s.openSecret("captcha", "secret_key", sealed); err == nil {
			secret = v
		}
	}
	return captcha.Settings{
		Provider:     s.st.GetSetting(setCaptchaProvider, captcha.ProviderImage),
		SiteKey:      s.st.GetSetting(setCaptchaSiteKey, ""),
		SecretKey:    secret,
		ExpectedHost: captcha.HostOf(s.publicBaseURL()),
	}
}

// captchaOn reports whether a context is gated at all.
func (s *Server) captchaOn(ctx captchaContext) bool {
	switch ctx {
	case ctxLogin:
		return s.st.GetSetting(setCaptchaLogin, "") == "1"
	case ctxForgot:
		return s.st.GetSetting(setCaptchaForgot, "") == "1"
	case ctxRegister:
		return s.st.GetSetting(setCaptchaRegister, "") == "1"
	}
	return false
}

// captchaRequired decides whether THIS request must present a captcha. Login can be configured to
// demand one only once a source has accumulated failures, so an ordinary user signing in on a quiet
// portal is never asked; the other contexts are always-on when enabled.
func (s *Server) captchaRequired(r *http.Request, ctx captchaContext, account string) bool {
	if !s.captchaOn(ctx) {
		return false
	}
	if ctx != ctxLogin || s.st.GetSetting(setCaptchaTrigger, triggerAlways) != triggerAfterFailure {
		return true
	}
	threshold, _ := strconv.Atoi(s.st.GetSetting(setCaptchaThreshold, "3"))
	if threshold <= 0 {
		threshold = 3
	}
	if s.loginThr == nil {
		return true // no counter to consult: ask for the captcha rather than skip it
	}
	// Either the address or the account having accumulated failures is enough. Keying on only one
	// leaves the other as a free lane: a distributed attempt spreads across addresses, and a
	// targeted one hammers a single account from one place.
	return s.loginThr.fails("ip:"+clientIP(r, s.trustedNets)) >= threshold ||
		(account != "" && s.loginThr.fails("u:"+strings.ToLower(account)) >= threshold)
}

// captchaProof is the client's answer, accepted on any gated form.
type captchaProof struct {
	CaptchaID     string `json:"captcha_id"`
	CaptchaAnswer string `json:"captcha_answer"`
	CaptchaToken  string `json:"captcha_token"`
}

// requireCaptcha verifies a gated form's captcha, writing the refusal itself. It returns true to
// proceed.
//
// The refusal carries captcha_required so the SPA knows to show — or refresh — the widget, which is
// what makes the after_failures mode usable: the form does not render a captcha until the server
// says one is needed, and the client learns that from the first refusal.
//
// A verification ERROR fails closed. A misconfigured provider or a siteverify outage must not
// silently open the gate.
func (s *Server) requireCaptcha(w http.ResponseWriter, r *http.Request, ctx captchaContext, account string, p captchaProof) bool {
	if !s.captchaRequired(r, ctx, account) {
		return true
	}
	ok, err := s.captchaSvc.Verify(r.Context(), s.captchaSettings(), captcha.Response{
		ID: p.CaptchaID, Answer: p.CaptchaAnswer, Token: p.CaptchaToken,
		RemoteIP: clientIP(r, s.trustedNets),
	})
	if err != nil {
		log.Printf("captcha: %s verify failed closed: %v", ctx, err)
	}
	if err != nil || !ok {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "captcha is required or incorrect", "captcha_required": true})
		return false
	}
	return true
}

// GET /api/captcha?ctx=login — what the public form needs to render, plus a challenge when the
// provider is the self-hosted image one. Public by necessity: it is read before authentication.
//
// It reports whether a captcha is required for THIS caller right now, so an after_failures portal
// shows nothing until the threshold is reached.
func (s *Server) apiCaptcha(w http.ResponseWriter, r *http.Request) {
	ctx := captchaContext(r.URL.Query().Get("ctx"))
	switch ctx {
	case ctxLogin, ctxForgot, ctxRegister:
	default:
		ctx = ctxLogin
	}
	set := s.captchaSettings()
	out := map[string]any{
		"required": s.captchaRequired(r, ctx, strings.TrimSpace(r.URL.Query().Get("account"))),
		"provider": set.Provider,
		// The site key is public by construction — it is embedded in the page for the provider's
		// widget. The secret never appears here.
		"site_key": set.SiteKey,
	}
	if out["required"].(bool) {
		if ch, err := s.captchaSvc.Issue(set); err == nil && ch != nil {
			out["captcha_id"], out["image"] = ch.ID, ch.Image
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, out)
}

// GET /api/admin/security — the captcha and registration settings, with the secret masked. The
// admin sees only WHETHER a secret is stored; the value never leaves the server, so a compromised
// admin session cannot exfiltrate the provider credential.
func (s *Server) apiAdminSecurity(w http.ResponseWriter, r *http.Request, user string) {
	groups := make([]map[string]any, 0)
	for _, g := range s.st.ListUserGroups() {
		groups = append(groups, map[string]any{"id": g.ID, "name": g.Name, "restricted": g.RestrictedEffective})
	}
	writeJSON(w, map[string]any{
		"captcha": map[string]any{
			"provider":       s.st.GetSetting(setCaptchaProvider, captcha.ProviderImage),
			"site_key":       s.st.GetSetting(setCaptchaSiteKey, ""),
			"has_secret":     s.st.GetSetting(setCaptchaSecret, "") != "",
			"login":          s.st.GetSetting(setCaptchaLogin, "") == "1",
			"forgot":         s.st.GetSetting(setCaptchaForgot, "") == "1",
			"register":       s.st.GetSetting(setCaptchaRegister, "") == "1",
			"trigger":        s.st.GetSetting(setCaptchaTrigger, triggerAlways),
			"fail_threshold": s.st.GetSetting(setCaptchaThreshold, "3"),
		},
		// Two axes (login_mode.go): what the page offers, and what the endpoint accepts. The
		// admin sees the STORED mode, not the resolved one, or a portal with no provider yet
		// would show "local only" and quietly discard the choice on save.
		"login": map[string]any{
			"mode":          s.st.GetSetting(setLoginMode, loginDual),
			"effective":     s.loginMode(),
			"sso_only":      s.st.GetSetting(setSSOOnly, "") == "1",
			"sso_available": s.ssoAvailable(),
		},
		"registration": map[string]any{
			"enabled":        s.registrationOpen(),
			"require_verify": s.registrationNeedsVerify(),
			"domains":        s.st.GetSetting(setRegDomains, ""),
			"default_group":  s.st.GetSetting(setRegGroup, ""),
			"expiry_days":    s.st.GetSetting(setRegExpiryDays, ""),
		},
		// Registration with verification on cannot work without SMTP, and an admin who cannot see
		// that will only learn it from a user who never got their email.
		"email_configured": s.emailEnabled(),
		"groups":           groups,
	})
}

// POST /api/admin/security — save both. A nil secret keeps the stored one; an explicit "" clears it,
// the same convention the SSO provider editor uses.
func (s *Server) apiAdminSecuritySave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Captcha struct {
			Provider      string  `json:"provider"`
			SiteKey       string  `json:"site_key"`
			SecretKey     *string `json:"secret_key"`
			Login         bool    `json:"login"`
			Forgot        bool    `json:"forgot"`
			Register      bool    `json:"register"`
			Trigger       string  `json:"trigger"`
			FailThreshold int     `json:"fail_threshold"`
		} `json:"captcha"`
		Login *struct {
			Mode    string `json:"mode"`
			SSOOnly bool   `json:"sso_only"`
		} `json:"login"`
		Registration struct {
			Enabled       bool   `json:"enabled"`
			RequireVerify bool   `json:"require_verify"`
			Domains       string `json:"domains"`
			DefaultGroup  string `json:"default_group"`
			ExpiryDays    string `json:"expiry_days"`
		} `json:"registration"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(in.Captcha.Provider))
	if provider == "" {
		provider = captcha.ProviderImage
	}
	// Validated here so the store can never hold a provider Verify would reject — which would
	// fail closed on every login and look like a portal-wide outage.
	if !captcha.ValidProvider(provider) {
		jsonError(w, http.StatusBadRequest, "unknown captcha provider")
		return
	}
	trigger := strings.ToLower(strings.TrimSpace(in.Captcha.Trigger))
	if trigger != triggerAfterFailure {
		trigger = triggerAlways
	}
	// An unrecognized mode is refused rather than coerced: silently storing something else would
	// leave an admin believing they configured a login policy they did not.
	// A pointer, so a body that omits `login` entirely leaves both settings alone. Every other
	// boolean on this endpoint is cleared by omission, which is fine for a captcha toggle and not
	// fine for the switch that decides whether passwords are accepted at all.
	if in.Login != nil {
		if !validLoginMode(in.Login.Mode) {
			jsonError(w, http.StatusBadRequest, "login mode must be dual | sso_first | sso_redirect | local_only")
			return
		}
		s.st.SetSetting(setLoginMode, in.Login.Mode)
		s.st.SetSetting(setSSOOnly, boolSetting(in.Login.SSOOnly))
	}

	s.st.SetSetting(setCaptchaProvider, provider)
	s.st.SetSetting(setCaptchaSiteKey, strings.TrimSpace(in.Captcha.SiteKey))
	s.st.SetSetting(setCaptchaTrigger, trigger)
	s.st.SetSetting(setCaptchaThreshold, strconv.Itoa(in.Captcha.FailThreshold))
	for k, on := range map[string]bool{
		setCaptchaLogin: in.Captcha.Login, setCaptchaForgot: in.Captcha.Forgot,
		setCaptchaRegister: in.Captcha.Register,
	} {
		s.st.SetSetting(k, boolSetting(on))
	}
	if in.Captcha.SecretKey != nil {
		if v := strings.TrimSpace(*in.Captcha.SecretKey); v == "" {
			s.st.SetSetting(setCaptchaSecret, "")
		} else {
			sealed, err := s.sealSecret("captcha", "secret_key", v)
			if err != nil {
				jsonError(w, http.StatusInternalServerError, "could not store the secret key")
				return
			}
			s.st.SetSetting(setCaptchaSecret, sealed)
		}
	}
	s.st.SetSetting(setRegEnabled, boolSetting(in.Registration.Enabled))
	s.st.SetSetting(setRegVerify, boolSetting(in.Registration.RequireVerify))
	s.st.SetSetting(setRegDomains, strings.TrimSpace(in.Registration.Domains))
	s.st.SetSetting(setRegGroup, strings.TrimSpace(in.Registration.DefaultGroup))
	s.st.SetSetting(setRegExpiryDays, strings.TrimSpace(in.Registration.ExpiryDays))
	writeJSON(w, okJSON)
}

func boolSetting(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
