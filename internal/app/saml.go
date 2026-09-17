package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KazuhaHub/authcore/saml"
)

// SAML 2.0 Service Provider (ADR 0023), built on github.com/KazuhaHub/authcore/saml, which in turn
// wraps crewjam/saml's low-level ServiceProvider — never samlsp.Middleware, which ships its own JWT
// session and would fight ours.
//
// authcore/saml closes the gaps crewjam/saml leaves open by design — see its package doc comment
// for the full list — and this file no longer implements any of them itself:
//
//   - Assertion replay: closed by authcore's ReplayCache, backed here by samlReplayCache
//     (saml_replay.go), which is the persistent, hashed sso_assertion_seen table this project has
//     always used. authcore's own in-memory default is never used — see samlReplayCache's doc
//     comment for why.
//   - Multiple assertions (GHSA-j2jp-wvqg-wc2g): closed unconditionally by authcore, in a
//     streaming pre-scan that runs before any signature is verified. Nothing here had this check
//     before; see the migration report for what that means.
//   - Weak signature algorithms, encrypted assertions, an absent Destination: authcore's
//     RejectWeakSignatures, AllowEncryptedAssertions and RequireDestination default to the same
//     policy this file used to hand-enforce (SHA-256-or-better; refuse; required), so leaving all
//     three at their zero value in samlProvider's Config reproduces the old behavior exactly.
//
// One authcore Config field does NOT default to the old behavior: StrictAttributes is false unless
// set, whereas this project's samlClaims has always rejected a duplicate attribute name
// unconditionally. samlProvider sets StrictAttributes: true explicitly — see its comment — and
// TestSAMLProviderConfigIsStrict pins that so a future edit cannot drop it silently.
//
// Two things authcore/saml has no API for at all stay in saml_crewjam.go, which is the only file
// in this package that imports github.com/crewjam/saml directly: parsing a federated
// <EntitiesDescriptor> IdP metadata document, and building an AuthnRequest with ForceAuthn set
// (authcore/saml.Provider.NewAuthnRequest has no such option). See that file's doc comment.
//
// crewjam exposes clock skew ONLY as a package-level variable, so it cannot be set per provider
// without racing concurrent logins and leaking one provider's tolerance into another's
// verification (authcore/saml inherits this limitation unchanged). It is therefore fixed once, at
// the library default, and the per-provider clock_skew_sec column is not applied — the same debt
// ADR 0023 already records.

const samlCookiePrefix = "rp_sso_saml_"

// samlProvider builds the authcore/saml Provider for a provider row, plus the ForceAuthn-capable
// crewjam material step-up needs. Entity id and ACS URL come from the same publicBaseURL() the
// metadata endpoint publishes — authcore compares an incoming Destination against the ACSURL we
// hand it, so two derivations that could diverge would be a bypass.
//
// A new Provider (and a new samlReplayCache) is built on every call, never cached on *Server or
// anywhere else. That is what keeps tenancy correct: authcore/saml.Provider has no notion of a
// tenant, and its ReplayCache sees only bare assertion ids with no issuer attached — the tenant
// separation lives entirely in samlReplayCache being freshly scoped, per call, to this provider's
// IdP entity id (see saml_replay.go). A cached, shared Provider (or a shared ReplayCache) would
// reopen the same "one shared type cannot express per-tenant state" failure mode that sank the
// audit-package migration.
func (s *Server) samlProvider(p SSOProvider) (*saml.Provider, samlMaterial, error) {
	m, err := s.samlLoadMaterial(p)
	if err != nil {
		return nil, samlMaterial{}, err
	}
	provider, err := saml.New(s.samlConfig(p, m))
	if err != nil {
		return nil, samlMaterial{}, err
	}
	return provider, m, nil
}

// samlConfig builds the authcore/saml Config for a provider row. Split out from samlProvider so
// TestSAMLProviderConfigIsStrict (saml_test.go) can inspect the Config value directly — every
// field on it is exported, so that test needs no reflection into authcore/saml's otherwise-private
// Provider internals.
func (s *Server) samlConfig(p SSOProvider, m samlMaterial) saml.Config {
	return saml.Config{
		EntityID:          m.entityID,
		ACSURL:            m.acsURL,
		Key:               m.key,
		Certificate:       m.cert,
		IDPMetadata:       m.meta,
		AllowIDPInitiated: p.AllowIdPInit,
		ReplayCache:       samlReplayCache{st: s.st, idpEntityID: m.meta.EntityID},
		// MUST be true. samlClaims has never tolerated a duplicate attribute Name/FriendlyName
		// (rules map one attribute to a role AND an OU, so a duplicate is aimed straight at the
		// tenancy boundary) — but authcore/saml defaults StrictAttributes to false, since not every
		// caller wants it. Leaving this unset would silently WEAKEN a protection this project has
		// always had, not merely fail to add one.
		StrictAttributes: true,
	}
}

func (s *Server) samlEntityID(slug string) string {
	return s.publicBaseURL() + "/api/auth/saml/" + url.PathEscape(slug) + "/metadata"
}

func (s *Server) samlACSURL(slug string) string {
	return s.publicBaseURL() + "/api/auth/saml/" + url.PathEscape(slug) + "/acs"
}

// samlKeypair unseals the SP signing key. The private key never leaves this function's callers and
// is unreachable from every API. Its lifetime is unchanged by the authcore migration: it is
// unsealed once per request here, handed by value to whichever caller needs it (samlProvider,
// which hands it to authcore's Config.Key; or samlMaterial.forceAuthnRedirect's crewjam
// ServiceProvider), and never cached on *Server, logged, or serialized — authcore/saml itself
// documents that it never writes to a logger, so nothing on that side of the call can reach it
// either.
func (s *Server) samlKeypair(p SSOProvider) (*rsa.PrivateKey, *x509.Certificate, error) {
	if p.SPKeyEnc == "" || p.SPCertPEM == "" {
		return nil, nil, fmt.Errorf("this provider has no SP certificate yet")
	}
	pemKey, err := s.openSecret(p.Slug, "saml_sp_key", p.SPKeyEnc)
	if err != nil {
		return nil, nil, fmt.Errorf("SP key unavailable (was secret_key rotated?): %w", err)
	}
	blk, _ := pem.Decode([]byte(pemKey))
	if blk == nil {
		return nil, nil, fmt.Errorf("SP key is not valid PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(blk.Bytes)
	if err != nil {
		return nil, nil, err
	}
	cblk, _ := pem.Decode([]byte(p.SPCertPEM))
	if cblk == nil {
		return nil, nil, fmt.Errorf("SP certificate is not valid PEM")
	}
	cert, err := x509.ParseCertificate(cblk.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return key, cert, nil
}

// generateSPKeypair mints the SP signing keypair. RSA-2048 rather than ECDSA because ADFS and
// several enterprise IdPs cannot consume EC keys in XML-DSig.
func generateSPKeypair(entityID string, validFor time.Duration) (keyPEM, certPEM string, notAfter time.Time, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", time.Time{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", time.Time{}, err
	}
	notAfter = time.Now().Add(validFor)
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: entityID},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", time.Time{}, err
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return keyPEM, certPEM, notAfter, nil
}

// GET /api/auth/saml/{slug}/metadata — our SP metadata, for pasting into the IdP. Public by design.
func (s *Server) samlMetadata(w http.ResponseWriter, r *http.Request) {
	p, ok := s.enabledProvider(r.PathValue("slug"), "saml")
	if !ok {
		http.NotFound(w, r)
		return
	}
	provider, _, err := s.samlProvider(p)
	if err != nil {
		http.Error(w, "saml is not configured", http.StatusServiceUnavailable)
		return
	}
	out, err := provider.MetadataXML()
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	w.Write(out)
}

// GET /api/auth/saml/{slug}/start — redirect to the IdP.
func (s *Server) samlStart(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	p, ok := s.enabledProvider(slug, "saml")
	if !ok {
		http.NotFound(w, r)
		return
	}
	provider, m, err := s.samlProvider(p)
	if err != nil {
		log.Printf("sso: saml %s start: %v", slug, err)
		s.ssoFail(w, r, "provider_unavailable")
		return
	}
	token, err := newAuthToken()
	if err != nil {
		s.ssoFail(w, r, "internal")
		return
	}
	purpose, forUser, allowed := s.stepUpIntent(w, r)
	if !allowed {
		return
	}

	var reqID, redirect string
	if purpose == authPurposeStepUp {
		// The whole point of a step-up round-trip. Without ForceAuthn the IdP answers from its own
		// session and returns instantly, which re-proves nothing against an attacker holding this
		// browser. authcore/saml.Provider.NewAuthnRequest has no ForceAuthn option, so this path
		// goes through samlMaterial.forceAuthnRedirect (saml_crewjam.go) instead — see that file's
		// doc comment for why that is the one place left that talks to crewjam/saml directly.
		reqID, redirect, err = m.forceAuthnRedirect(token)
	} else {
		var authReq *saml.AuthnRequest
		authReq, err = provider.NewAuthnRequest(token)
		if err == nil {
			reqID, redirect = authReq.ID, authReq.RedirectURL
		}
	}
	if err != nil {
		log.Printf("sso: saml %s authn request: %v", slug, err)
		s.ssoFail(w, r, "provider_unavailable")
		return
	}

	// The request ID is stored so the ACS can require the response to answer THIS request.
	if err := s.st.CreateAuthRequest(AuthRequest{
		Token: token, ProviderID: p.ID, Kind: "saml", ReqID: reqID,
		Target: safeReturnPath(r.URL.Query().Get("next")), Purpose: purpose, Username: forUser,
	}, time.Now().Add(ssoFlowTTL)); err != nil {
		s.ssoFail(w, r, "internal")
		return
	}
	http.SetCookie(w, s.samlFlowCookie(slug, token, int(ssoFlowTTL.Seconds())))
	http.Redirect(w, r, redirect, http.StatusFound)
}

// POST /api/auth/saml/{slug}/acs — consume the IdP's assertion.
func (s *Server) samlACS(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	http.SetCookie(w, s.samlFlowCookie(slug, "", -1)) // clear first, whatever happens next

	p, ok := s.enabledProvider(slug, "saml")
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Bound the body before parsing any XML: an unbounded or deeply nested document is a denial of
	// service. authcore/saml.Provider.ValidateResponse deliberately does not do this for us — its
	// own doc comment says so — so it stays here, exactly as before.
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	if err := r.ParseForm(); err != nil {
		s.ssoFail(w, r, "bad_response")
		return
	}
	relay := r.PostFormValue("RelayState")
	ck, err := r.Cookie(samlCookiePrefix + slug)
	if err != nil || ck.Value == "" || relay == "" || ck.Value != relay {
		s.ssoFail(w, r, "bad_state")
		return
	}
	req, ok := s.st.ConsumeAuthRequest(relay, time.Now())
	if !ok || req.Kind != "saml" || req.ProviderID != p.ID {
		s.ssoFail(w, r, "bad_state")
		return
	}

	provider, m, err := s.samlProvider(p)
	if err != nil {
		s.ssoFail(w, r, "provider_unavailable")
		return
	}
	// One call does everything the old five-step sequence
	// (requireDestination → rejectWeakSignatureAlgs → rejectEncryptedAssertion → ParseXMLResponse →
	// MarkAssertionSeen) did, in the same defense-in-depth order, PLUS the multi-Assertion check
	// (GHSA-j2jp-wvqg-wc2g) this project never had — see samlFailureReason below for how each
	// failure is classified for the login page and the log line.
	assertion, err := provider.ValidateResponse(r.Context(), r, []string{req.ReqID})
	if err != nil {
		log.Printf("sso: saml %s assertion rejected: %v", slug, err)
		s.ssoFail(w, r, samlFailureReason(err))
		return
	}
	subject, format, err := samlSubject(assertion)
	if err != nil {
		log.Printf("sso: saml %s subject: %v", slug, err)
		s.ssoFail(w, r, subjectFailCode(err))
		return
	}
	claims := samlClaims(assertion)
	claims["nameid"] = subject
	// The trust anchor is the configured IdP metadata's entity id, not whatever the assertion
	// itself claims as Issuer — the same choice the pre-authcore code made (sp.IDPMetadata.EntityID)
	// and for the same reason: it is config, not attacker-controlled document content.
	id := ssoIdentity{Provider: "saml", Issuer: m.meta.EntityID, Subject: subject, Claims: claims}
	if req.Purpose == authPurposeStepUp {
		s.completeSSOStepUp(w, r, p, id, req)
		return
	}
	s.completeSSOLogin(w, r, p, id, req.Target)
	_ = format
}

// samlFailureReason maps a authcore/saml validation error to the reason code the login page shows
// and the audit log records. Every branch here is a case this project already distinguished before
// the migration, except ErrTooManyAssertions (saml_multiple_assertions), which authcore/saml adds:
// see saml.go's package doc comment.
func samlFailureReason(err error) string {
	switch {
	case errors.Is(err, saml.ErrMissingDestination), errors.Is(err, saml.ErrDestinationMismatch):
		return "saml_destination"
	case errors.Is(err, saml.ErrWeakSignatureAlgorithm):
		return "saml_weak_signature"
	case errors.Is(err, saml.ErrEncryptedAssertionNotAllowed):
		return "saml_encrypted"
	case errors.Is(err, saml.ErrReplayed):
		return "saml_replay"
	case errors.Is(err, saml.ErrTooManyAssertions):
		return "saml_multiple_assertions"
	case errors.Is(err, saml.ErrDuplicateAttribute):
		return "saml_attributes"
	default:
		return "saml_assertion_rejected"
	}
}

// errTransientNameID and errNoNameID are distinguished because they are the two subject failures an
// admin can actually fix, and they are fixed in different places.
var (
	errTransientNameID = errors.New("transient NameID cannot identify an account; set the IdP's NameID format to persistent, emailAddress or unspecified")
	errNoNameID        = errors.New("the assertion carried no NameID; the IdP must send a unique user identifier")
)

// subjectFailCode maps a subject failure to the reason shown on the login page. Every code here
// describes the portal's own trust configuration, never anything about the person signing in, so
// none of them can be used to learn whether an account exists.
func subjectFailCode(err error) string {
	switch {
	case errors.Is(err, errTransientNameID):
		return "saml_transient_nameid"
	case errors.Is(err, errNoNameID):
		return "saml_no_nameid"
	}
	return "saml_bad_subject"
}

// samlSubject extracts the NameID, refusing formats that cannot serve as a stable account key. A
// transient NameID is a new value on every login, so linking to it would create an account per
// sign-in.
func samlSubject(a *saml.Assertion) (subject, format string, err error) {
	if a == nil {
		return "", "", errNoNameID
	}
	id := strings.TrimSpace(a.NameID)
	format = a.NameIDFormat
	if id == "" {
		return "", "", errNoNameID
	}
	if a.IsTransient() {
		return "", "", errTransientNameID
	}
	return id, format, nil
}

// samlClaims flattens the assertion's attribute bag into the claims map completeSSOLogin expects.
// Both the URI-style Name and the FriendlyName are already indexed by authcore/saml (ADFS/Entra
// send the long URN while admins type the short one), and a duplicate attribute Name is already
// refused before an *Assertion ever reaches here — samlProvider sets Config.StrictAttributes: true,
// so ValidateResponse itself returns ErrDuplicateAttribute in that case. This function only
// reshapes data; it does not itself validate anything.
func samlClaims(a *saml.Assertion) map[string]any {
	out := make(map[string]any, len(a.Attributes))
	for key, vals := range a.Attributes {
		if len(vals) == 1 {
			out[key] = vals[0]
		} else {
			out[key] = toAnySlice(vals)
		}
	}
	return out
}

func toAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

// samlFlowCookie binds an in-flight login to this browser. SameSite=None because the ACS is a
// cross-site POST from the IdP and a Lax cookie would not be sent — which is also why SAML
// requires an https public URL.
func (s *Server) samlFlowCookie(slug, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: samlCookiePrefix + slug, Value: value,
		Path:     "/api/auth/saml/" + url.PathEscape(slug) + "/acs",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode, MaxAge: maxAge,
	}
}
