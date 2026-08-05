package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"
)

// SAML 2.0 Service Provider (ADR 0023), built on crewjam's low-level ServiceProvider — never
// samlsp.Middleware, which ships its own JWT session and would fight ours.
//
// crewjam v0.5.1 validates more than it is usually credited for: the signature, the audience
// restriction, NotBefore/NotOnOrAfter with clock skew, and InResponseTo against the request ids we
// pass. Three things it does NOT do are ours, and each has a test:
//
//  1. Destination may be ABSENT when the response itself is unsigned (service_provider.go:1008) —
//     the check is skipped rather than failed. We require it to be present.
//  2. InResponseTo is not checked at all when AllowIDPInitiated is set. We default that off and
//     treat an absent InResponseTo as a rejection.
//  3. There is no assertion replay cache. Without one, a captured assertion can be posted again
//     inside its validity window.

const samlCookiePrefix = "rp_sso_saml_"

// samlSP builds the ServiceProvider for a provider row. Entity id and ACS URL come from the same
// publicBaseURL() the metadata endpoint publishes — crewjam compares an incoming Destination
// against the ACS URL we hand it, so two derivations that could diverge would be a bypass.
func (s *Server) samlSP(p SSOProvider) (*saml.ServiceProvider, error) {
	base := s.publicBaseURL()
	if base == "" {
		return nil, fmt.Errorf("set the public URL before enabling SAML")
	}
	key, cert, err := s.samlKeypair(p)
	if err != nil {
		return nil, err
	}
	acs, err := url.Parse(s.samlACSURL(p.Slug))
	if err != nil {
		return nil, err
	}
	meta, err := parseIdPMetadata(p.IdPMetadataXML)
	if err != nil {
		return nil, err
	}
	// crewjam exposes clock skew ONLY as a package-level variable, so it cannot be set per
	// provider without racing concurrent logins and leaking one provider's tolerance into
	// another's verification. It is therefore fixed once, at the library default, and the
	// per-provider column is not applied. Making it configurable would need an upstream change.
	return &saml.ServiceProvider{
		Key:         key,
		Certificate: cert,
		MetadataURL: *mustURL(s.samlEntityID(p.Slug)),
		AcsURL:      *acs,
		IDPMetadata: meta,
		// IdP-initiated login disables crewjam's InResponseTo check entirely, so it stays off
		// unless an operator explicitly accepts that trade.
		AllowIDPInitiated: p.AllowIdPInit,
		// MUST be set. crewjam reads an empty AuthnNameIDFormat as TRANSIENT ("to maintain library
		// back-compat", service_provider.go), so leaving it unset made every AuthnRequest demand a
		// NameID that changes on every login — which samlSubject then refuses, because a value like
		// that cannot key an account. The portal was asking for exactly what it would reject, and no
		// IdP configuration could satisfy it.
		//
		// Unspecified rather than persistent: crewjam maps it to an OMITTED NameIDPolicy, which
		// lets the IdP send whatever its admin configured as the unique identifier. That is the
		// interoperable choice — Entra sends the configured claim, usually the UPN; demanding
		// persistent would instead get an opaque pairwise id that no operator recognises.
		AuthnNameIDFormat: saml.UnspecifiedNameIDFormat,
	}, nil
}

func (s *Server) samlEntityID(slug string) string {
	return s.publicBaseURL() + "/api/auth/saml/" + url.PathEscape(slug) + "/metadata"
}

func (s *Server) samlACSURL(slug string) string {
	return s.publicBaseURL() + "/api/auth/saml/" + url.PathEscape(slug) + "/acs"
}

func mustURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	if u == nil {
		return &url.URL{}
	}
	return u
}

// samlKeypair unseals the SP signing key. The private key never leaves this function's callers and
// is unreachable from every API.
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

// parseIdPMetadata reads an IdP descriptor, unwrapping a federation <EntitiesDescriptor> so
// Shibboleth/InCommon-style metadata works too (crewjam models only the bare form).
func parseIdPMetadata(xmlDoc string) (*saml.EntityDescriptor, error) {
	if strings.TrimSpace(xmlDoc) == "" {
		return nil, fmt.Errorf("no IdP metadata configured")
	}
	var ed saml.EntityDescriptor
	if err := xml.Unmarshal([]byte(xmlDoc), &ed); err == nil && len(ed.IDPSSODescriptors) > 0 {
		return &ed, nil
	}
	var eds saml.EntitiesDescriptor
	if err := xml.Unmarshal([]byte(xmlDoc), &eds); err != nil {
		return nil, fmt.Errorf("IdP metadata is not valid XML: %w", err)
	}
	for _, e := range eds.EntityDescriptors {
		if len(e.IDPSSODescriptors) > 0 {
			cp := e
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("IdP metadata contains no IDPSSODescriptor")
}

// GET /api/auth/saml/{slug}/metadata — our SP metadata, for pasting into the IdP. Public by design.
func (s *Server) samlMetadata(w http.ResponseWriter, r *http.Request) {
	p, ok := s.enabledProvider(r.PathValue("slug"), "saml")
	if !ok {
		http.NotFound(w, r)
		return
	}
	sp, err := s.samlSP(p)
	if err != nil {
		http.Error(w, "saml is not configured", http.StatusServiceUnavailable)
		return
	}
	out, err := xml.MarshalIndent(sp.Metadata(), "", "  ")
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
	sp, err := s.samlSP(p)
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
	authReq, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		log.Printf("sso: saml %s authn request: %v", slug, err)
		s.ssoFail(w, r, "provider_unavailable")
		return
	}
	if purpose == authPurposeStepUp {
		// The whole point of a step-up round-trip. Without ForceAuthn the IdP answers from its own
		// session and returns instantly, which re-proves nothing against an attacker holding this
		// browser. An IdP that refuses ForceAuthn fails the round-trip, which is the correct
		// outcome: better a step-up that cannot complete than one that certifies nothing.
		force := true
		authReq.ForceAuthn = &force
	}
	// The request ID is stored so the ACS can require the response to answer THIS request.
	if err := s.st.CreateAuthRequest(AuthRequest{
		Token: token, ProviderID: p.ID, Kind: "saml", ReqID: authReq.ID,
		Target: safeReturnPath(r.URL.Query().Get("next")), Purpose: purpose, Username: forUser,
	}, time.Now().Add(ssoFlowTTL)); err != nil {
		s.ssoFail(w, r, "internal")
		return
	}
	redirect, err := authReq.Redirect(token, sp)
	if err != nil {
		log.Printf("sso: saml %s redirect: %v", slug, err)
		s.ssoFail(w, r, "provider_unavailable")
		return
	}
	http.SetCookie(w, s.samlFlowCookie(slug, token, int(ssoFlowTTL.Seconds())))
	http.Redirect(w, r, redirect.String(), http.StatusFound)
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
	// Bound the body before parsing any XML: an unbounded or deeply nested document is a denial
	// of service, and etree's writer recurses per level.
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

	raw, err := base64.StdEncoding.DecodeString(r.PostFormValue("SAMLResponse"))
	if err != nil {
		s.ssoFail(w, r, "bad_response")
		return
	}
	// crewjam skips the Destination check when the response is unsigned and the attribute is
	// absent (service_provider.go:1008). Require it ourselves so absence is a rejection.
	if err := requireDestination(raw, s.samlACSURL(slug)); err != nil {
		log.Printf("sso: saml %s destination: %v", slug, err)
		s.ssoFail(w, r, "saml_destination")
		return
	}
	// Algorithm policy and the encryption refusal both run BEFORE parsing: a signature must never be
	// accepted on the strength of SHA-1, and the SP private key must never be handed a ciphertext.
	if err := rejectWeakSignatureAlgs(raw); err != nil {
		log.Printf("sso: saml %s signature algorithm: %v", slug, err)
		s.ssoFail(w, r, "saml_weak_signature")
		return
	}
	if err := rejectEncryptedAssertion(raw); err != nil {
		log.Printf("sso: saml %s: %v", slug, err)
		s.ssoFail(w, r, "saml_encrypted")
		return
	}
	sp, err := s.samlSP(p)
	if err != nil {
		s.ssoFail(w, r, "provider_unavailable")
		return
	}
	assertion, err := sp.ParseXMLResponse(raw, []string{req.ReqID}, *mustURL(s.samlACSURL(slug)))
	if err != nil {
		log.Printf("sso: saml %s assertion rejected: %v", slug, err)
		s.ssoFail(w, r, "saml_assertion_rejected")
		return
	}
	// Replay: crewjam has no assertion cache. This must happen BEFORE a session is minted, and it
	// is insert-or-nothing so two concurrent posts of one assertion produce exactly one winner.
	if !s.st.MarkAssertionSeen(sp.IDPMetadata.EntityID, assertion.ID, assertionExpiry(assertion)) {
		log.Printf("sso: saml %s replayed assertion %s", slug, assertion.ID)
		s.ssoFail(w, r, "saml_replay")
		return
	}
	subject, format, err := samlSubject(assertion)
	if err != nil {
		log.Printf("sso: saml %s subject: %v", slug, err)
		s.ssoFail(w, r, subjectFailCode(err))
		return
	}
	claims, err := samlClaims(assertion)
	if err != nil {
		log.Printf("sso: saml %s attributes: %v", slug, err)
		s.ssoFail(w, r, "saml_attributes")
		return
	}
	claims["nameid"] = subject
	id := ssoIdentity{Provider: "saml", Issuer: sp.IDPMetadata.EntityID, Subject: subject, Claims: claims}
	if req.Purpose == authPurposeStepUp {
		s.completeSSOStepUp(w, r, p, id, req)
		return
	}
	s.completeSSOLogin(w, r, p, id, req.Target)
	_ = format
}

// requireDestination enforces that the Response carries a Destination equal to our ACS URL. It
// reads the attribute off the raw document deliberately: this is a PRE-condition, closing the case
// where crewjam would skip the check entirely, and it never feeds anything downstream.
func requireDestination(raw []byte, want string) error {
	var probe struct {
		Destination string `xml:"Destination,attr"`
	}
	if err := xml.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("response is not valid XML")
	}
	if probe.Destination == "" {
		return fmt.Errorf("response has no Destination")
	}
	if probe.Destination != want {
		return fmt.Errorf("response Destination %q is not this ACS URL", probe.Destination)
	}
	return nil
}

// The signature-algorithm policy. goxmldsig's default validation context maps rsa-sha1 straight to
// x509.SHA1WithRSA and applies no policy of its own, so without this the portal's entire
// authentication would rest on SHA-1 whenever the IdP is configured that way — and that is the ADFS
// and legacy-Keycloak default. SHA-1 collisions are practical.
//
// An ALLOWLIST, not a denylist: an algorithm nobody here has considered must be refused, not
// accepted by omission. The identifiers come from goxmldsig's own constants (also why it is a direct
// require) so a library rename cannot silently widen the policy. Deliberately not configurable —
// there is no deployment for which accepting SHA-1 is the right answer.
var (
	allowedSignatureMethods = map[string]bool{
		dsig.RSASHA256SignatureMethod:   true,
		dsig.RSASHA384SignatureMethod:   true,
		dsig.RSASHA512SignatureMethod:   true,
		dsig.ECDSASHA256SignatureMethod: true,
		dsig.ECDSASHA384SignatureMethod: true,
		dsig.ECDSASHA512SignatureMethod: true,
	}
	// The matching digests. goxmldsig does not export these, so they are the W3C identifiers.
	allowedDigestMethods = map[string]bool{
		"http://www.w3.org/2001/04/xmlenc#sha256":       true,
		"http://www.w3.org/2001/04/xmldsig-more#sha384": true,
		"http://www.w3.org/2001/04/xmlenc#sha512":       true,
	}
)

// rejectWeakSignatureAlgs refuses a response unless every signature and digest algorithm it declares
// is on the allowlist. It reads the raw XML because crewjam exposes no hook into the validation
// context, and it runs BEFORE parsing so no signature is ever accepted on the strength of SHA-1.
// Every SignatureMethod and DigestMethod in the document is examined — the response signature, the
// assertion signature, and each Reference digest — so a strong outer signature cannot vouch for a
// SHA-1 inner one.
func rejectWeakSignatureAlgs(raw []byte) error {
	// Scanned token by token rather than unmarshalled into a struct: these elements appear at
	// several depths and a fixed path would silently miss the inner ones.
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	for {
		tok, err := dec.Token()
		if err != nil {
			break // malformed XML is the parser's business to report, not ours
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var allowed map[string]bool
		switch el.Name.Local {
		case dsig.SignatureMethodTag:
			allowed = allowedSignatureMethods
		case dsig.DigestMethodTag:
			allowed = allowedDigestMethods
		default:
			continue
		}
		for _, a := range el.Attr {
			if a.Name.Local == dsig.AlgorithmAttr && !allowed[a.Value] {
				return fmt.Errorf("%s %q is not accepted; configure the IdP for SHA-256 or better",
					el.Name.Local, a.Value)
			}
		}
	}
	return nil
}

// rejectEncryptedAssertion refuses a response carrying an EncryptedAssertion. ADR 0023 lists
// encrypted assertions under what we deliberately do not build: on the Web SSO profile TLS already
// covers the transport, and decryption is a padding-oracle surface. crewjam looks for an encrypted
// assertion FIRST and would feed it to the SP private key, so declining it is an explicit refusal
// here rather than an absence of support.
func rejectEncryptedAssertion(raw []byte) error {
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "EncryptedAssertion" {
			return fmt.Errorf("encrypted assertions are not supported; disable assertion encryption for this SP")
		}
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
	if a.Subject == nil || a.Subject.NameID == nil {
		return "", "", errNoNameID
	}
	id := strings.TrimSpace(a.Subject.NameID.Value)
	format = a.Subject.NameID.Format
	if id == "" {
		return "", "", errNoNameID
	}
	if strings.Contains(format, "transient") {
		return "", "", errTransientNameID
	}
	return id, format, nil
}

// samlClaims flattens the attribute statements. A duplicate attribute Name is REJECTED rather than
// last-wins: with rules mapping an attribute to a role and an OU, attribute pollution would be
// aimed straight at the tenancy boundary. Both the URI-style Name and the FriendlyName are indexed,
// because ADFS/Entra send the long URN while admins type the short one.
func samlClaims(a *saml.Assertion) (map[string]any, error) {
	out := map[string]any{}
	seen := map[string]bool{}
	for _, st := range a.AttributeStatements {
		for _, attr := range st.Attributes {
			var vals []string
			for _, v := range attr.Values {
				vals = append(vals, v.Value)
			}
			// Name and FriendlyName are frequently the SAME string (Okta and Keycloak commonly send
			// e.g. both as "groups"). That is one attribute indexed twice, not a duplicate, so the
			// keys are deduplicated within the attribute before the collision check — otherwise a
			// perfectly ordinary assertion would be rejected.
			for _, key := range dedupe(attr.Name, attr.FriendlyName) {
				if seen[key] {
					return nil, fmt.Errorf("duplicate attribute %q in the assertion", key)
				}
				seen[key] = true
				if len(vals) == 1 {
					out[key] = vals[0]
				} else {
					out[key] = toAnySlice(vals)
				}
			}
		}
	}
	return out, nil
}

func toAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

// assertionExpiry is how long the replay entry must outlive the assertion, clamped so a hostile
// NotOnOrAfter cannot pin a row forever.
//
// The entry must outlive the window crewjam will ACCEPT the assertion in, which is NotOnOrAfter plus
// MaxClockSkew — not NotOnOrAfter alone. An entry that lapsed first would reopen the replay it
// exists to close, so the library's own skew is added to every branch, including the clamp.
func assertionExpiry(a *saml.Assertion) time.Time {
	margin := saml.MaxClockSkew + time.Minute
	latest := time.Now().Add(5 * time.Minute)
	// Conditions is optional in the schema, so it is a pointer and may legitimately be nil.
	if a.Conditions != nil && !a.Conditions.NotOnOrAfter.IsZero() && a.Conditions.NotOnOrAfter.After(latest) {
		latest = a.Conditions.NotOnOrAfter
	}
	// SubjectConfirmationData carries its own NotOnOrAfter, and crewjam checks that one too, so the
	// later of the two is what the acceptance window actually is.
	for _, sc := range subjectConfirmationExpiries(a) {
		if sc.After(latest) {
			latest = sc
		}
	}
	if max := time.Now().Add(30 * time.Minute); latest.After(max) {
		latest = max
	}
	return latest.Add(margin)
}

func subjectConfirmationExpiries(a *saml.Assertion) []time.Time {
	if a.Subject == nil {
		return nil
	}
	var out []time.Time
	for _, sc := range a.Subject.SubjectConfirmations {
		if sc.SubjectConfirmationData != nil && !sc.SubjectConfirmationData.NotOnOrAfter.IsZero() {
			out = append(out, sc.SubjectConfirmationData.NotOnOrAfter)
		}
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

// dedupe returns the distinct non-empty keys among its arguments, preserving order.
func dedupe(keys ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}
