package app

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"

	"github.com/crewjam/saml"
)

// This file is the only place in package app that imports github.com/crewjam/saml directly. It
// exists for exactly two things authcore/saml's public API does not (yet) cover — see the package
// doc comment on saml.go for the fuller picture:
//
//  1. parseIdPMetadata: authcore/saml.Config.IDPMetadata takes an already-parsed
//     *crewjamsaml.EntityDescriptor and does no federation unwrapping of its own. Shibboleth/
//     InCommon-style metadata publishes a <EntitiesDescriptor> wrapper that crewjam's XML model
//     does not understand, so unwrapping it is still this project's job.
//  2. samlMaterial.forceAuthnRedirect: authcore/saml.Provider.NewAuthnRequest has no ForceAuthn
//     option, and Provider exposes no way to reach an unsent *crewjamsaml.AuthnRequest to set one
//     on. Step-up login (ADR 0023) is meaningless without ForceAuthn — see stepup_sso.go and
//     samlStart in saml.go — so this builds the AuthnRequest directly against crewjam, the same
//     library authcore/saml wraps, from the same key/cert/metadata/ACS/entity-id inputs the
//     authcore Provider is built from.
//
// samlMaterial is what both paths need, gathered once per samlStart/samlACS/samlMetadata call so
// neither this file nor saml.go has to unseal the SP key or re-parse the IdP metadata twice. Its
// meta field carries the crewjam type; saml.go reads and passes that field around by value without
// ever naming github.com/crewjam/saml.EntityDescriptor itself, which is what lets saml.go import
// github.com/KazuhaHub/authcore/saml as the bare, unaliased "saml" in the same package.
type samlMaterial struct {
	entityID string
	acsURL   string
	key      *rsa.PrivateKey
	cert     *x509.Certificate
	meta     *saml.EntityDescriptor
}

// samlLoadMaterial unseals the SP keypair and parses the configured IdP metadata — the two
// fallible, provider-specific inputs every SAML request needs, gathered in one place.
func (s *Server) samlLoadMaterial(p SSOProvider) (samlMaterial, error) {
	if s.publicBaseURL() == "" {
		return samlMaterial{}, fmt.Errorf("set the public URL before enabling SAML")
	}
	key, cert, err := s.samlKeypair(p)
	if err != nil {
		return samlMaterial{}, err
	}
	meta, err := parseIdPMetadata(p.IdPMetadataXML)
	if err != nil {
		return samlMaterial{}, err
	}
	return samlMaterial{
		entityID: s.samlEntityID(p.Slug),
		acsURL:   s.samlACSURL(p.Slug),
		key:      key,
		cert:     cert,
		meta:     meta,
	}, nil
}

// forceAuthnRedirect builds a fresh SP-initiated AuthnRequest with ForceAuthn set and returns its
// id and the URL to send the browser to. It is a deliberate, narrow bypass of
// authcore/saml.Provider.NewAuthnRequest for the one case that needs a knob that API does not
// expose: step-up authentication, where the whole point of the round-trip is that the IdP
// re-proves the user rather than answering from its existing session. An IdP that refuses
// ForceAuthn fails the round-trip, which is the correct outcome — better a step-up that cannot
// complete than one that certifies nothing.
func (m samlMaterial) forceAuthnRedirect(relayState string) (id, redirectURL string, err error) {
	acs, err := url.Parse(m.acsURL)
	if err != nil {
		return "", "", err
	}
	sp := &saml.ServiceProvider{
		Key:               m.key,
		Certificate:       m.cert,
		MetadataURL:       *mustURL(m.entityID),
		AcsURL:            *acs,
		IDPMetadata:       m.meta,
		AllowIDPInitiated: false,
		AuthnNameIDFormat: saml.UnspecifiedNameIDFormat,
	}
	req, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(saml.HTTPRedirectBinding),
		saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		return "", "", err
	}
	force := true
	req.ForceAuthn = &force
	redirect, err := req.Redirect(relayState, sp)
	if err != nil {
		return "", "", err
	}
	return req.ID, redirect.String(), nil
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

// mustURL parses raw as a URL, falling back to the zero value rather than panicking. Shared with
// saml.go (same package; no import of crewjam/saml needed there since url.URL is stdlib).
func mustURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	if u == nil {
		return &url.URL{}
	}
	return u
}
