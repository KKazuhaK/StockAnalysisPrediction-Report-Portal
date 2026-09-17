// Package captcha abstracts the public-form captcha behind one Service with two shapes of provider:
//
//   - "image": a self-hosted character captcha (base64Captcha, via authcore/captcha). The portal
//     issues the challenge image server-side and verifies the typed answer against an in-process
//     store. Zero external calls, which is the default here — a self-hosted portal is often behind
//     a network where Google and Cloudflare are unreachable, and a captcha that cannot load is a
//     locked door.
//   - token providers ("turnstile" / "recaptcha" / "hcaptcha"): rendered client-side by the
//     provider's widget using the public site key; the portal only verifies the returned token
//     server-side against the provider's siteverify endpoint using the secret key.
//
// The active provider and keys are passed in on every call, so an admin can switch providers
// without a restart. This is a deliberate mismatch with authcore/captcha's TokenVerifier, which
// fixes its provider/secret/allowed-hostnames at construction time: this package hides that by
// building a fresh TokenVerifier on every token-provider Verify call. NewTokenVerifier does no
// network I/O, so this costs nothing but an allocation — see verifyToken below.
//
// This is defence in depth on top of the login throttle, not a replacement for it. The throttle
// bounds how fast one source may guess; a captcha raises the per-attempt cost, which is what a
// distributed attempt spread across many addresses defeats the throttle with.
package captcha

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	captchacore "github.com/KazuhaHub/authcore/captcha"
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
//
// authcore/captcha has no concept of "image" as a token Provider — image and token verification
// are two unrelated mechanisms there (ImageGenerator vs TokenVerifier), so the four-way dispatch
// stays here, in the business layer that actually needs it.
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
//
// Delegated to authcore/captcha.HostOf: the implementation there is byte-for-byte what this
// package used to do on its own.
func HostOf(baseURL string) string {
	return captchacore.HostOf(baseURL)
}

// imageStoreCapacity matches base64Captcha.GCLimitNumber, the capacity the portal relied on before
// this package wrapped authcore/captcha. authcore's DefaultMemoryStore() uses a smaller default
// (10,000) tuned for authcore's own callers, so this is passed explicitly rather than left to the
// default — dropping it would be a quiet reduction in how many in-flight challenges the portal can
// hold under load.
const imageStoreCapacity = 10240

// imageStoreTTL is how long an issued image challenge remains answerable.
const imageStoreTTL = 5 * time.Minute

// tokenVerifyTimeout is the siteverify round-trip budget, matching the *http.Client timeout this
// package used before migrating to authcore/captcha.
const tokenVerifyTimeout = 10 * time.Second

// Service issues and verifies captchas.
type Service struct {
	// store backs the image generator below. Kept as its own field (rather than reached through
	// gen) because it is also exercised directly in tests, the same way it was before this package
	// wrapped authcore/captcha.
	store captchacore.Store
	gen   *captchacore.ImageGenerator
	http  *http.Client
	// endpoints holds the siteverify URL used for each token provider. Kept here — rather than left
	// to authcore/captcha's own built-in defaults — so a test can redirect a provider at an
	// httptest.Server, and so every call site always passes an explicit endpoint through
	// captchacore.WithEndpoint instead of depending on NewTokenVerifier's provider-name lookup.
	endpoints map[string]string
}

// New builds a Service with an in-process image-captcha store and the public siteverify endpoints.
//
// The store is in-process on purpose. A challenge lives for five minutes and is consumed on first
// correct answer, so the only cost of a restart or a second instance is that an in-flight challenge
// has to be re-issued — which is what the widget's refresh already does. Persisting it would add a
// table and a sweeper to protect nothing.
func New() *Service {
	store := captchacore.NewMemoryStore(imageStoreCapacity, imageStoreTTL)
	return &Service{
		store: store,
		gen:   captchacore.NewImageGenerator(store),
		// Shared across every TokenVerifier this Service builds (see verifyToken), so that
		// constructing a fresh TokenVerifier per call — required because authcore/captcha fixes a
		// verifier's config at construction time — does not also throw away TCP/TLS connection
		// reuse. authcore/captcha's own MIGRATION.md does not call this out; a TokenVerifier built
		// without WithHTTPClient gets a brand-new *http.Client (and Transport) on every call.
		http: &http.Client{Timeout: tokenVerifyTimeout},
		endpoints: map[string]string{
			ProviderTurnstile: captchacore.TurnstileEndpoint,
			ProviderRecaptcha: captchacore.RecaptchaEndpoint,
			ProviderHCaptcha:  captchacore.HCaptchaEndpoint,
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

// Issue creates a challenge. Only the image provider issues server-side; token providers return
// (nil, nil) because their widget is rendered on the client from the site key.
func (s *Service) Issue(set Settings) (*Challenge, error) {
	if providerOf(set) != ProviderImage {
		return nil, nil
	}
	ch, err := s.gen.Generate()
	if err != nil {
		return nil, fmt.Errorf("captcha: generate image: %w", err)
	}
	return &Challenge{ID: ch.ID, Image: ch.Image}, nil
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
		return s.gen.Verify(r.ID, r.Answer), nil
	case ProviderTurnstile, ProviderRecaptcha, ProviderHCaptcha:
		if r.Token == "" {
			return false, nil
		}
		secret := strings.TrimSpace(set.SecretKey)
		if secret == "" {
			return false, fmt.Errorf("captcha: %s secret key is not configured", provider)
		}
		return s.verifyToken(ctx, provider, secret, r.Token, r.RemoteIP, set.ExpectedHost)
	default:
		return false, fmt.Errorf("captcha: unknown provider %q", provider)
	}
}

// verifyToken builds a fresh authcore/captcha.TokenVerifier for THIS call's live configuration and
// runs the siteverify round trip through it.
//
// A TokenVerifier cannot be built once and reused: authcore/captcha fixes provider, secret and
// allowed hostnames at construction time (NewTokenVerifier), unlike this package's own contract of
// re-reading Settings on every call so an admin's config change takes effect without a restart.
// Building it here, per call, rather than caching one on Service, is what preserves that contract —
// see this package's doc comment. NewTokenVerifier does no network I/O, so this is cheap; the
// shared s.http client (see New) keeps the one thing that would otherwise be lost — connection
// reuse — intact across calls.
//
// authcore/captcha logs nothing on its own (by design — see its MIGRATION.md); the two log lines
// below reproduce what this package's own verifyToken used to log directly, now driven off the
// structured captchacore.Result instead of raw provider JSON.
func (s *Service) verifyToken(ctx context.Context, provider, secret, token, remoteIP, expectedHost string) (bool, error) {
	endpoint := s.endpoints[provider]
	if endpoint == "" {
		return false, fmt.Errorf("captcha: no siteverify endpoint for this provider")
	}
	opts := []captchacore.TokenOption{
		captchacore.WithEndpoint(endpoint),
		captchacore.WithHTTPClient(s.http),
	}
	if expectedHost != "" {
		opts = append(opts, captchacore.WithAllowedHostnames(expectedHost))
	}
	tv, err := captchacore.NewTokenVerifier(captchacore.Provider(provider), secret, opts...)
	if err != nil {
		return false, fmt.Errorf("captcha: %w", err)
	}
	res, err := tv.Verify(ctx, token, remoteIP)
	if err != nil {
		return false, fmt.Errorf("captcha: siteverify: %w", err)
	}
	if !res.Success {
		// error-codes / hostname mismatches are logged so a wrong secret or an unregistered domain
		// surfaces somewhere an operator will find it, rather than as an unexplained wall of
		// rejected logins.
		switch {
		case res.HostnameMismatch:
			log.Printf("captcha: hostname mismatch, token rejected: got %q want %q", res.Hostname, expectedHost)
		case len(res.ErrorCodes) > 0:
			log.Printf("captcha: siteverify rejected: %v", res.ErrorCodes)
		}
		return false, nil
	}
	return true, nil
}
