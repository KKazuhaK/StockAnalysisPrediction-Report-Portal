package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewjam/saml"
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
	skew := time.Duration(p.ClockSkewSec) * time.Second
	if skew <= 0 {
		skew = time.Minute
	}
	saml.MaxClockSkew = skew
	return &saml.ServiceProvider{
		Key:         key,
		Certificate: cert,
		MetadataURL: *mustURL(s.samlEntityID(p.Slug)),
		AcsURL:      *acs,
		IDPMetadata: meta,
		// IdP-initiated login disables crewjam's InResponseTo check entirely, so it stays off
		// unless an operator explicitly accepts that trade.
		AllowIDPInitiated: p.AllowIdPInit,
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
	authReq, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		log.Printf("sso: saml %s authn request: %v", slug, err)
		s.ssoFail(w, r, "provider_unavailable")
		return
	}
	// The request ID is stored so the ACS can require the response to answer THIS request.
	if err := s.st.CreateAuthRequest(AuthRequest{
		Token: token, ProviderID: p.ID, Kind: "saml", ReqID: authReq.ID,
		Target: safeReturnPath(r.URL.Query().Get("next")),
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
		s.ssoFail(w, r, "bad_response")
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
		s.ssoFail(w, r, "bad_response")
		return
	}
	// Replay: crewjam has no assertion cache. This must happen BEFORE a session is minted, and it
	// is insert-or-nothing so two concurrent posts of one assertion produce exactly one winner.
	if !s.st.MarkAssertionSeen(sp.IDPMetadata.EntityID, assertion.ID, assertionExpiry(assertion)) {
		log.Printf("sso: saml %s replayed assertion %s", slug, assertion.ID)
		s.ssoFail(w, r, "bad_response")
		return
	}
	subject, format, err := samlSubject(assertion)
	if err != nil {
		log.Printf("sso: saml %s subject: %v", slug, err)
		s.ssoFail(w, r, "bad_response")
		return
	}
	claims, err := samlClaims(assertion)
	if err != nil {
		log.Printf("sso: saml %s attributes: %v", slug, err)
		s.ssoFail(w, r, "bad_response")
		return
	}
	claims["nameid"] = subject
	s.completeSSOLogin(w, r, p, ssoIdentity{
		Provider: "saml", Issuer: sp.IDPMetadata.EntityID, Subject: subject, Claims: claims,
	}, req.Target)
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

// samlSubject extracts the NameID, refusing formats that cannot serve as a stable account key. A
// transient NameID is a new value on every login, so linking to it would create an account per
// sign-in.
func samlSubject(a *saml.Assertion) (subject, format string, err error) {
	if a.Subject == nil || a.Subject.NameID == nil {
		return "", "", fmt.Errorf("assertion has no NameID")
	}
	id := strings.TrimSpace(a.Subject.NameID.Value)
	format = a.Subject.NameID.Format
	if id == "" {
		return "", "", fmt.Errorf("assertion has an empty NameID")
	}
	if strings.Contains(format, "transient") {
		return "", "", fmt.Errorf("transient NameID cannot identify an account; configure a persistent format")
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
func assertionExpiry(a *saml.Assertion) time.Time {
	latest := time.Now().Add(5 * time.Minute)
	// Conditions is optional in the schema, so it is a pointer and may legitimately be nil.
	if a.Conditions != nil && !a.Conditions.NotOnOrAfter.IsZero() && a.Conditions.NotOnOrAfter.After(latest) {
		latest = a.Conditions.NotOnOrAfter
	}
	if max := time.Now().Add(30 * time.Minute); latest.After(max) {
		latest = max
	}
	return latest.Add(time.Minute)
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
