// Package captcha abstracts the public-form captcha behind one Service with two shapes of provider:
//
//   - "image": a self-hosted character captcha (base64Captcha). The portal issues the challenge
//     image server-side and verifies the typed answer against an in-process store. Zero external
//     calls, which is the default here — a self-hosted portal is often behind a network where
//     Google and Cloudflare are unreachable, and a captcha that cannot load is a locked door.
//   - token providers ("turnstile" / "recaptcha" / "hcaptcha"): rendered client-side by the
//     provider's widget using the public site key; the portal only verifies the returned token
//     server-side against the provider's siteverify endpoint using the secret key.
//
// The active provider and keys are passed in on every call, so an admin can switch providers
// without a restart.
//
// This is defence in depth on top of the login throttle, not a replacement for it. The throttle
// bounds how fast one source may guess; a captcha raises the per-attempt cost, which is what a
// distributed attempt spread across many addresses defeats the throttle with.
package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	base64Captcha "github.com/mojocn/base64Captcha"
)

// Provider identifiers. One source of truth, shared with the settings validation so the admin API
// cannot store a provider Verify would then reject.
const (
	ProviderImage     = "image"
	ProviderTurnstile = "turnstile"
	ProviderRecaptcha = "recaptcha"
	ProviderHCaptcha  = "hcaptcha"
)

// ValidProvider reports whether p names a provider this service can actually verify.
func ValidProvider(p string) bool {
	switch p {
	case ProviderImage, ProviderTurnstile, ProviderRecaptcha, ProviderHCaptcha:
		return true
	default:
		return false
	}
}

// Settings is the live configuration a caller passes on every call. It is deliberately a plain
// struct rather than a reference to the store: the package does no I/O of its own beyond the
// provider's siteverify endpoint, which keeps it testable without a database.
type Settings struct {
	Provider  string
	SiteKey   string
	SecretKey string
	// ExpectedHost is the hostname the portal is served from, derived from the configured public
	// URL. Token providers report the hostname a challenge was solved on; a mismatch is rejected so
	// a token solved on another site sharing this site key cannot be replayed here. Empty skips the
	// check, so a portal with no public URL configured is not locked out.
	ExpectedHost string
}

// Challenge is an issued image captcha (image provider only).
type Challenge struct {
	ID    string `json:"captcha_id"`
	Image string `json:"image"` // a data:image/...;base64 URL, ready for an <img src>
}

// Response carries the client's answer. Image providers fill ID+Answer; token providers fill Token.
type Response struct {
	ID       string
	Answer   string
	Token    string
	RemoteIP string
}

// HostOf extracts the bare hostname from a base URL like "https://portal.example.com:8443/".
// Returns "" for an empty or unparseable URL, which callers pass through to mean "do not check".
func HostOf(baseURL string) string {
	b := strings.TrimSpace(baseURL)
	if b == "" {
		return ""
	}
	u, err := url.Parse(b)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Service issues and verifies captchas.
type Service struct {
	store     base64Captcha.Store
	http      *http.Client
	endpoints map[string]string // provider → siteverify URL (overridden in tests)
}

// New builds a Service with an in-process image-captcha store and the public siteverify endpoints.
//
// The store is in-process on purpose. A challenge lives for five minutes and is consumed on first
// correct answer, so the only cost of a restart or a second instance is that an in-flight challenge
// has to be re-issued — which is what the widget's refresh already does. Persisting it would add a
// table and a sweeper to protect nothing.
func New() *Service {
	return &Service{
		store: base64Captcha.NewMemoryStore(base64Captcha.GCLimitNumber, 5*time.Minute),
		http:  &http.Client{Timeout: 10 * time.Second},
		endpoints: map[string]string{
			ProviderTurnstile: "https://challenges.cloudflare.com/turnstile/v0/siteverify",
			ProviderRecaptcha: "https://www.google.com/recaptcha/api/siteverify",
			ProviderHCaptcha:  "https://hcaptcha.com/siteverify",
		},
	}
}

func providerOf(s Settings) string {
	p := strings.ToLower(strings.TrimSpace(s.Provider))
	if p == "" {
		return ProviderImage
	}
	return p
}

// imageCaptcha builds a digit captcha over the shared store. Digits rather than alphanumerics
// because O/0 and l/1 are indistinguishable in a distorted glyph, and a user who mistypes a
// captcha they read correctly learns only that the portal is broken.
func (s *Service) imageCaptcha() *base64Captcha.Captcha {
	return base64Captcha.NewCaptcha(base64Captcha.NewDriverDigit(80, 240, 5, 0.7, 80), s.store)
}

// Issue creates a challenge. Only the image provider issues server-side; token providers return
// (nil, nil) because their widget is rendered on the client from the site key.
func (s *Service) Issue(set Settings) (*Challenge, error) {
	if providerOf(set) != ProviderImage {
		return nil, nil
	}
	id, b64s, _, err := s.imageCaptcha().Generate()
	if err != nil {
		return nil, fmt.Errorf("captcha: generate image: %w", err)
	}
	return &Challenge{ID: id, Image: b64s}, nil
}

// Verify checks a response against the configured provider. A (false, nil) result is an ordinary
// wrong or empty answer; a non-nil error signals a misconfiguration — an unknown provider, a
// missing secret, a siteverify outage — which the caller must treat as fail-closed. A gate that
// opens when its verifier is broken is not a gate.
func (s *Service) Verify(ctx context.Context, set Settings, r Response) (bool, error) {
	switch provider := providerOf(set); provider {
	case ProviderImage:
		if r.ID == "" || r.Answer == "" {
			return false, nil
		}
		// clear=true: a challenge is single-use, so a correct answer is consumed rather than left
		// available for replay across a burst of parallel attempts.
		return s.store.Verify(r.ID, r.Answer, true), nil
	case ProviderTurnstile, ProviderRecaptcha, ProviderHCaptcha:
		if r.Token == "" {
			return false, nil
		}
		secret := strings.TrimSpace(set.SecretKey)
		if secret == "" {
			return false, fmt.Errorf("captcha: %s secret key is not configured", provider)
		}
		return s.verifyToken(ctx, s.endpoints[provider], secret, r.Token, r.RemoteIP, set.ExpectedHost)
	default:
		return false, fmt.Errorf("captcha: unknown provider %q", provider)
	}
}

// verifyToken POSTs the siteverify form shared by Turnstile, reCAPTCHA and hCaptcha, and validates
// the reply. error-codes are logged so a wrong secret or an unregistered domain surfaces somewhere
// an operator will find it, rather than as an unexplained wall of rejected logins.
func (s *Service) verifyToken(ctx context.Context, endpoint, secret, token, remoteIP, expectedHost string) (bool, error) {
	if endpoint == "" {
		return false, fmt.Errorf("captcha: no siteverify endpoint for this provider")
	}
	form := url.Values{"secret": {secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("captcha: siteverify: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Success    bool     `json:"success"`
		Hostname   string   `json:"hostname"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, fmt.Errorf("captcha: decode siteverify: %w", err)
	}
	if !out.Success {
		if len(out.ErrorCodes) > 0 {
			log.Printf("captcha: siteverify rejected: %v", out.ErrorCodes)
		}
		return false, nil
	}
	// Hostname pinning, deliberately lenient in both directions it can be wrong: skipped when no
	// public URL is configured, and skipped when the provider omits a hostname (reCAPTCHA does with
	// domain verification off). It must not be able to lock out a correctly-configured admin.
	if expectedHost != "" && out.Hostname != "" && !strings.EqualFold(out.Hostname, expectedHost) {
		log.Printf("captcha: hostname mismatch, token rejected: got %q want %q", out.Hostname, expectedHost)
		return false, nil
	}
	return true, nil
}
