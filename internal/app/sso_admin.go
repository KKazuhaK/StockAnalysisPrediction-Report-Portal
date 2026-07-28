package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Admin API for SSO configuration (ADR 0023). Everything lives in the DB and is edited here;
// config.yaml stays infrastructure-only.
//
// One rule governs every response shape below: a secret is NEVER returned. Reads report only
// whether one is set, and a blank secret on write keeps the stored value — the same pattern the
// Dify api_key already uses, so admins do not have to re-enter credentials to rename something.

// ssoProviderJSON is the admin view of a provider. Note what is absent: client_secret_enc,
// sp_key_enc and any decrypted form of either.
func (s *Server) ssoProviderJSON(p SSOProvider) map[string]any {
	return map[string]any{
		"id": p.ID, "kind": p.Kind, "slug": p.Slug, "name": p.Name,
		"enabled": p.Enabled, "provisioning": p.Provisioning,
		"default_group": p.DefaultGroup, "default_role": p.DefaultRole,
		"default_expiry_days": p.DefaultExpiryDays, "allow_admin_role": p.AllowAdminRole,
		"session_hours": p.SessionHours,
		// OIDC
		"issuer": p.Issuer, "client_id": p.ClientID, "scopes": p.Scopes,
		"has_client_secret": p.ClientSecretEnc != "",
		"redirect_url":      s.oidcRedirectURL(p.Slug),
		// SAML
		"idp_metadata_url": p.IdPMetadataURL, "idp_entity_id": p.IdPEntityID,
		"has_idp_metadata":    strings.TrimSpace(p.IdPMetadataXML) != "",
		"allow_idp_initiated": p.AllowIdPInit, "clock_skew_sec": p.ClockSkewSec,
		"sp_entity_id": s.samlEntityID(p.Slug), "sp_acs_url": s.samlACSURL(p.Slug),
		"sp_cert_pem": p.SPCertPEM, "sp_cert_not_after": p.SPCertNotAfter, "has_sp_key": p.SPKeyEnc != "",
		// Attribute mapping
		"attr_upn": p.AttrUPN, "attr_email": p.AttrEmail, "attr_display": p.AttrDisplay,
		"attr_groups": p.AttrGroups, "attr_external_id": p.AttrExternalID,
	}
}

func (s *Server) apiAdminSSOProviders(w http.ResponseWriter, r *http.Request, user string) {
	out := make([]map[string]any, 0)
	for _, p := range s.st.ListSSOProviders() {
		out = append(out, s.ssoProviderJSON(p))
	}
	writeJSON(w, map[string]any{"providers": out, "public_url": s.publicBaseURL()})
}

// ssoProviderInput is the write shape. Secrets are pointers so "absent" (keep) is distinct from
// "" (clear) — the same reason the Dify target editor uses pointers.
type ssoProviderInput struct {
	Kind         string `json:"kind"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	Provisioning string `json:"provisioning"`

	DefaultGroup      int64  `json:"default_group"`
	DefaultRole       string `json:"default_role"`
	DefaultExpiryDays int    `json:"default_expiry_days"`
	AllowAdminRole    bool   `json:"allow_admin_role"`
	SessionHours      int    `json:"session_hours"`

	Issuer       string  `json:"issuer"`
	ClientID     string  `json:"client_id"`
	ClientSecret *string `json:"client_secret"`
	Scopes       string  `json:"scopes"`

	IdPMetadataURL string `json:"idp_metadata_url"`
	IdPMetadataXML string `json:"idp_metadata_xml"`
	AllowIdPInit   bool   `json:"allow_idp_initiated"`
	ClockSkewSec   int    `json:"clock_skew_sec"`

	AttrUPN        string `json:"attr_upn"`
	AttrEmail      string `json:"attr_email"`
	AttrDisplay    string `json:"attr_display"`
	AttrGroups     string `json:"attr_groups"`
	AttrExternalID string `json:"attr_external_id"`
}

func (s *Server) apiAdminSSOSave(w http.ResponseWriter, r *http.Request, user string) {
	var in ssoProviderInput
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if in.Slug == "" || (in.Kind != "oidc" && in.Kind != "saml") {
		jsonError(w, http.StatusBadRequest, "slug and a kind of oidc or saml are required")
		return
	}
	prev, existed := s.st.SSOProviderBySlug(in.Slug)

	p := SSOProvider{
		ID: prev.ID, Kind: in.Kind, Slug: in.Slug, Name: strings.TrimSpace(in.Name),
		Enabled: in.Enabled, Provisioning: firstNonEmpty(in.Provisioning, "off"),
		DefaultGroup: in.DefaultGroup, DefaultRole: validRole(firstNonEmpty(in.DefaultRole, "user")),
		DefaultExpiryDays: in.DefaultExpiryDays, AllowAdminRole: in.AllowAdminRole,
		SessionHours: in.SessionHours,
		Issuer:       strings.TrimRight(strings.TrimSpace(in.Issuer), "/"),
		ClientID:     strings.TrimSpace(in.ClientID), Scopes: in.Scopes,
		DiscoveryJSON: prev.DiscoveryJSON, DiscoveryFetched: prev.DiscoveryFetched,
		IdPMetadataURL: strings.TrimSpace(in.IdPMetadataURL),
		IdPMetadataXML: firstNonEmpty(strings.TrimSpace(in.IdPMetadataXML), prev.IdPMetadataXML),
		IdPEntityID:    prev.IdPEntityID, IdPCertPEM: prev.IdPCertPEM,
		AllowIdPInit: in.AllowIdPInit, ClockSkewSec: in.ClockSkewSec,
		SPCertPEM: prev.SPCertPEM, SPKeyEnc: prev.SPKeyEnc, SPCertNotAfter: prev.SPCertNotAfter,
		AttrUPN: in.AttrUPN, AttrEmail: in.AttrEmail, AttrDisplay: in.AttrDisplay,
		AttrGroups: in.AttrGroups, AttrExternalID: in.AttrExternalID,
		ClientSecretEnc: prev.ClientSecretEnc,
	}
	// A blank secret keeps the stored one; an explicit "" clears it.
	if in.ClientSecret != nil {
		if v := strings.TrimSpace(*in.ClientSecret); v == "" {
			p.ClientSecretEnc = ""
		} else {
			enc, err := s.sealSecret(p.Slug, "oidc_client_secret", v)
			if err != nil {
				jsonError(w, http.StatusInternalServerError, "could not store the client secret")
				return
			}
			p.ClientSecretEnc = enc
		}
	}
	// A SAML provider needs an SP keypair before it can sign anything; mint one on first save.
	if p.Kind == "saml" && p.SPKeyEnc == "" && s.publicBaseURL() != "" {
		keyPEM, certPEM, notAfter, err := generateSPKeypair(s.samlEntityID(p.Slug), 3*365*24*time.Hour)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "could not generate the SP certificate")
			return
		}
		enc, err := s.sealSecret(p.Slug, "saml_sp_key", keyPEM)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "could not store the SP key")
			return
		}
		p.SPKeyEnc, p.SPCertPEM, p.SPCertNotAfter = enc, certPEM, notAfter.UTC().Format(time.RFC3339)
	}
	// Enabling is where configuration is validated, so a half-configured provider can be saved as
	// a draft but never offered on the login page.
	if p.Enabled {
		if err := s.validateProviderForEnable(&p); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	id, err := s.st.SaveSSOProvider(p)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = existed
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// validateProviderForEnable refuses to put a provider in front of users until it can actually work.
// Failing here is much kinder than failing mid-login with an opaque error.
func (s *Server) validateProviderForEnable(p *SSOProvider) error {
	base := s.publicBaseURL()
	if base == "" {
		return ssoError("set the Public URL (Manage → Email) before enabling SSO — the redirect and ACS URLs derive from it")
	}
	if p.Provisioning == "jit" {
		if p.DefaultGroup == 0 {
			return ssoError("choose a default group: a just-in-time account must land in a known group")
		}
		if !s.st.GroupExists(p.DefaultGroup) {
			return ssoError("the default group no longer exists")
		}
		// The default group must be an EXTERNAL one. Landing a self-provisioned account in an
		// unrestricted group would give whoever the IdP admits the run of the portal — the exact
		// outcome the external-user model (ADR 0022) exists to prevent, reached by an easy
		// misconfiguration rather than an attack.
		if !s.st.GroupRestrictedEffective(p.DefaultGroup) {
			return ssoError("the default group must be an external (restricted) group — otherwise a self-created account could see everything")
		}
	}
	switch p.Kind {
	case "oidc":
		if p.Issuer == "" || p.ClientID == "" || p.ClientSecretEnc == "" {
			return ssoError("issuer, client id and client secret are required")
		}
		// Fetch discovery through the SSRF guard, so a bad or hostile issuer fails at save time.
		if err := s.ssoClient().checkURL(p.Issuer + "/.well-known/openid-configuration"); err != nil {
			return ssoError("issuer is not reachable: " + err.Error())
		}
	case "saml":
		if !strings.HasPrefix(base, "https://") {
			return ssoError("SAML requires an https Public URL: the assertion is posted cross-site, so its cookie must be Secure")
		}
		if strings.TrimSpace(p.IdPMetadataXML) == "" {
			return ssoError("fetch or paste the IdP metadata first")
		}
		if _, err := parseIdPMetadata(p.IdPMetadataXML); err != nil {
			return ssoError("IdP metadata is not usable: " + err.Error())
		}
		if p.SPKeyEnc == "" {
			return ssoError("the SP certificate has not been generated yet")
		}
	}
	return nil
}

// apiAdminSSOFetchMetadata pulls IdP metadata over the SSRF-guarded client. It FAILS CLOSED: a
// failed fetch leaves the previously-stored document in place, because blanking the trust anchor
// would take every login down and, worse, could be induced deliberately.
func (s *Server) apiAdminSSOFetchMetadata(w http.ResponseWriter, r *http.Request, user string) {
	p, ok := s.st.SSOProviderBySlug(r.PathValue("slug"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no such provider")
		return
	}
	if p.IdPMetadataURL == "" {
		jsonError(w, http.StatusBadRequest, "no metadata URL configured")
		return
	}
	body, err := s.ssoClient().fetch(p.IdPMetadataURL, 2<<20)
	if err != nil {
		s.st.NoteSSOMetadataError(p.ID, err.Error())
		jsonError(w, http.StatusBadGateway, "could not fetch the metadata: "+err.Error())
		return
	}
	meta, err := parseIdPMetadata(string(body))
	if err != nil {
		s.st.NoteSSOMetadataError(p.ID, err.Error())
		jsonError(w, http.StatusBadRequest, "the metadata is not usable: "+err.Error())
		return
	}
	p.IdPMetadataXML, p.IdPEntityID = string(body), meta.EntityID
	p.IdPMetadataFetched, p.IdPMetadataError = nowStr(), ""
	if _, err := s.st.SaveSSOProvider(p); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "entity_id": meta.EntityID, "fetched_at": p.IdPMetadataFetched})
}

// apiAdminSSOLastSeen returns the claims the IdP actually sent on the most recent sign-in through
// this provider. Attribute mapping is otherwise configured by guesswork — IdPs disagree wildly on
// claim names (ADFS and Entra send long URN forms, Okta and Keycloak short ones), and a wrong guess
// fails as "not provisioned" with nothing to go on. Showing the real keys turns that into a glance.
//
// Only the CLAIM NAMES and a short preview of each value are returned, never the whole payload:
// an assertion can carry personal data that an admin has no reason to read in bulk.
func (s *Server) apiAdminSSOLastSeen(w http.ResponseWriter, r *http.Request, user string) {
	p, ok := s.st.SSOProviderBySlug(r.PathValue("slug"))
	if !ok {
		jsonError(w, http.StatusNotFound, "no such provider")
		return
	}
	raw := s.st.LastSeenAttrs(p.Slug)
	if raw == "" {
		writeJSON(w, map[string]any{"seen": false})
		return
	}
	var claims map[string]any
	if err := json.Unmarshal([]byte(raw), &claims); err != nil {
		writeJSON(w, map[string]any{"seen": false})
		return
	}
	out := make([]map[string]any, 0, len(claims))
	for k, v := range claims {
		out = append(out, map[string]any{"name": k, "preview": previewClaim(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	writeJSON(w, map[string]any{"seen": true, "claims": out})
}

// previewClaim renders a claim value compactly and truncated: enough to recognise which field is
// which, not enough to be a data export.
func previewClaim(v any) string {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, fmt.Sprint(e))
		}
		s = strings.Join(parts, ", ")
	default:
		s = fmt.Sprint(t)
	}
	if len([]rune(s)) > 60 {
		s = string([]rune(s)[:60]) + "…"
	}
	return s
}

func (s *Server) apiAdminSSODelete(w http.ResponseWriter, r *http.Request, user string) {
	if err := s.st.DeleteSSOProvider(pathID(r, "id")); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// ---------- group rules ----------

func (s *Server) apiAdminSSORules(w http.ResponseWriter, r *http.Request, user string) {
	rows, err := s.st.query(`SELECT id,COALESCE(provider_id,0),COALESCE(ord,0),COALESCE(enabled,1),
		COALESCE(attr,''),COALESCE(value,''),COALESCE(target_role,''),COALESCE(target_group,0),
		COALESCE(keep_on_miss,0),COALESCE(ci,0),COALESCE(note,'') FROM sso_group_rules ORDER BY ord, id`)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, provID, group int64
		var ord, enabled, keep, ci int
		var attr, value, role, note string
		if rows.Scan(&id, &provID, &ord, &enabled, &attr, &value, &role, &group, &keep, &ci, &note) != nil {
			continue
		}
		out = append(out, map[string]any{"id": id, "provider_id": provID, "ord": ord,
			"enabled": enabled != 0, "attr": attr, "value": value, "target_role": role,
			"target_group": group, "keep_on_miss": keep != 0, "ci": ci != 0, "note": note})
	}
	writeJSON(w, map[string]any{"rules": out})
}

// apiAdminSSORulesSave replaces the whole ordered rule set in one transaction. Order is the
// contract (first match wins), so a partial write would silently change who gets what.
func (s *Server) apiAdminSSORulesSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Rules []struct {
			ProviderID  int64  `json:"provider_id"`
			Enabled     bool   `json:"enabled"`
			Attr        string `json:"attr"`
			Value       string `json:"value"`
			TargetRole  string `json:"target_role"`
			TargetGroup int64  `json:"target_group"`
			KeepOnMiss  bool   `json:"keep_on_miss"`
			CI          bool   `json:"ci"`
			Note        string `json:"note"`
		} `json:"rules"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	tx, err := s.st.db.Begin()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.st.bind(`DELETE FROM sso_group_rules`)); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i, r := range in.Rules {
		if _, err := tx.Exec(s.st.bind(`INSERT INTO sso_group_rules
			(provider_id,ord,enabled,attr,value,target_role,target_group,keep_on_miss,ci,note)
			VALUES(?,?,?,?,?,?,?,?,?,?)`),
			nullZero(r.ProviderID), i, boolInt(r.Enabled), strings.TrimSpace(r.Attr), r.Value,
			r.TargetRole, nullZero(r.TargetGroup), boolInt(r.KeepOnMiss), boolInt(r.CI), r.Note); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}
