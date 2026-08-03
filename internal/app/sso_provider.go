package app

import "strings"

// SSO provider configuration (ADR 0023). One row per IdP, addressed by slug. v1's admin UI manages
// a single SAML row and a single OIDC row, but nothing here assumes that: adding more providers is
// a UI change with no schema movement.

// SSOProvider is one configured identity provider.
type SSOProvider struct {
	ID           int64
	Kind         string // "saml" | "oidc"
	Slug         string
	Name         string
	Enabled      bool
	Provisioning string // "off" | "jit"

	DefaultGroup      int64
	DefaultRole       string
	DefaultExpiryDays int
	AllowAdminRole    bool

	// OIDC
	Issuer           string
	ClientID         string
	ClientSecretEnc  string
	Scopes           string
	DiscoveryJSON    string
	DiscoveryFetched string

	// SAML
	IdPMetadataURL     string
	IdPMetadataXML     string
	IdPMetadataFetched string
	IdPMetadataError   string
	IdPEntityID        string
	IdPCertPEM         string
	SPCertPEM          string
	SPCertNotAfter     string
	SPKeyEnc           string
	AllowIdPInit       bool
	ClockSkewSec       int
	// Icon is what the login-page button shows beside its name: "" for none, "preset:<name>" for a
	// built-in, or a /site-assets/ path this portal serves. Never a remote URL — the login page is
	// unauthenticated, so a third-party fetch would announce every visitor before they sign in.
	Icon string
	// LinkBy is how an unlinked login finds an EXISTING account: "" (the default) means only the
	// identity link and external_id, refusing a name collision; "username" and "email" mean this
	// IdP is authoritative for those fields and may adopt the account they name.
	LinkBy string

	// Attribute mapping
	AttrUPN, AttrEmail, AttrDisplay, AttrGroups, AttrExternalID string

	SessionHours int
}

// EffectiveScopes returns the OIDC scopes to request, always including openid — without it the
// response is a plain OAuth2 grant and there is no ID token to verify.
func (p SSOProvider) EffectiveScopes() []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.FieldsFunc(p.Scopes, func(r rune) bool { return r == ',' || r == ' ' }) {
		if f = strings.TrimSpace(f); f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	if !seen["openid"] {
		out = append([]string{"openid"}, out...)
	}
	return out
}

const ssoProviderCols = `id,kind,slug,COALESCE(name,''),COALESCE(enabled,0),COALESCE(provisioning,'off'),
	COALESCE(default_group,0),COALESCE(default_role,'user'),COALESCE(default_expiry_days,0),COALESCE(allow_admin_role,0),
	COALESCE(issuer,''),COALESCE(client_id,''),COALESCE(client_secret_enc,''),COALESCE(scopes,''),
	COALESCE(discovery_json,''),COALESCE(discovery_fetched_at,''),
	COALESCE(idp_metadata_url,''),COALESCE(idp_metadata_xml,''),COALESCE(idp_metadata_fetched_at,''),COALESCE(idp_metadata_error,''),COALESCE(idp_entity_id,''),COALESCE(idp_cert_pem,''),
	COALESCE(sp_cert_pem,''),COALESCE(sp_cert_not_after,''),COALESCE(sp_key_enc,''),COALESCE(allow_idp_initiated,0),COALESCE(clock_skew_sec,60),
	COALESCE(attr_upn,''),COALESCE(attr_email,''),COALESCE(attr_display,''),COALESCE(attr_groups,''),COALESCE(attr_external_id,''),
	COALESCE(session_hours,0),COALESCE(icon,''),COALESCE(link_by,'')`

func scanProvider(scan func(...any) error) (SSOProvider, error) {
	var p SSOProvider
	var enabled, allowAdmin, allowIdPInit int
	err := scan(&p.ID, &p.Kind, &p.Slug, &p.Name, &enabled, &p.Provisioning,
		&p.DefaultGroup, &p.DefaultRole, &p.DefaultExpiryDays, &allowAdmin,
		&p.Issuer, &p.ClientID, &p.ClientSecretEnc, &p.Scopes, &p.DiscoveryJSON, &p.DiscoveryFetched,
		&p.IdPMetadataURL, &p.IdPMetadataXML, &p.IdPMetadataFetched, &p.IdPMetadataError, &p.IdPEntityID, &p.IdPCertPEM,
		&p.SPCertPEM, &p.SPCertNotAfter, &p.SPKeyEnc, &allowIdPInit, &p.ClockSkewSec,
		&p.AttrUPN, &p.AttrEmail, &p.AttrDisplay, &p.AttrGroups, &p.AttrExternalID, &p.SessionHours, &p.Icon, &p.LinkBy)
	if err != nil {
		return SSOProvider{}, err
	}
	p.Enabled, p.AllowAdminRole, p.AllowIdPInit = enabled != 0, allowAdmin != 0, allowIdPInit != 0
	return p, nil
}

// SSOProviderBySlug loads one provider. Callers on the login path additionally require Enabled —
// a disabled provider must behave as if it does not exist.
func (s *Store) SSOProviderBySlug(slug string) (SSOProvider, bool) {
	p, err := scanProvider(s.queryRow(`SELECT `+ssoProviderCols+` FROM sso_providers WHERE slug=?`, slug).Scan)
	if err != nil {
		return SSOProvider{}, false
	}
	return p, true
}

// ListSSOProviders returns every configured provider, newest last, for the admin UI.
func (s *Store) ListSSOProviders() []SSOProvider {
	rows, err := s.query(`SELECT ` + ssoProviderCols + ` FROM sso_providers ORDER BY kind, id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SSOProvider
	for rows.Next() {
		if p, err := scanProvider(rows.Scan); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// EnabledSSOProviders returns the providers the login page may offer. An empty result means SSO is
// entirely off, and the /api/auth/* routes then behave as if they do not exist.
func (s *Store) EnabledSSOProviders() []SSOProvider {
	var out []SSOProvider
	for _, p := range s.ListSSOProviders() {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

// SaveSSOProvider inserts or updates a provider by slug and returns its id.
func (s *Store) SaveSSOProvider(p SSOProvider) (int64, error) {
	if existing, ok := s.SSOProviderBySlug(p.Slug); ok {
		p.ID = existing.ID
		_, err := s.exec(`UPDATE sso_providers SET kind=?,name=?,enabled=?,provisioning=?,
			default_group=?,default_role=?,default_expiry_days=?,allow_admin_role=?,
			issuer=?,client_id=?,client_secret_enc=?,scopes=?,discovery_json=?,discovery_fetched_at=?,
			idp_metadata_url=?,idp_metadata_xml=?,idp_metadata_fetched_at=?,idp_metadata_error=?,idp_entity_id=?,idp_cert_pem=?,
			sp_cert_pem=?,sp_cert_not_after=?,sp_key_enc=?,allow_idp_initiated=?,clock_skew_sec=?,
			attr_upn=?,attr_email=?,attr_display=?,attr_groups=?,attr_external_id=?,session_hours=?,icon=?,link_by=?,updated_at=?
			WHERE id=?`,
			p.Kind, p.Name, boolInt(p.Enabled), p.Provisioning,
			nullZero(p.DefaultGroup), p.DefaultRole, p.DefaultExpiryDays, boolInt(p.AllowAdminRole),
			p.Issuer, p.ClientID, p.ClientSecretEnc, p.Scopes, p.DiscoveryJSON, p.DiscoveryFetched,
			p.IdPMetadataURL, p.IdPMetadataXML, p.IdPMetadataFetched, p.IdPMetadataError, p.IdPEntityID, p.IdPCertPEM,
			p.SPCertPEM, p.SPCertNotAfter, p.SPKeyEnc, boolInt(p.AllowIdPInit), p.ClockSkewSec,
			p.AttrUPN, p.AttrEmail, p.AttrDisplay, p.AttrGroups, p.AttrExternalID, p.SessionHours, p.Icon, p.LinkBy,
			nowStr(), p.ID)
		return p.ID, err
	}
	return s.insertID(`INSERT INTO sso_providers(kind,slug,name,enabled,provisioning,
		default_group,default_role,default_expiry_days,allow_admin_role,
		issuer,client_id,client_secret_enc,scopes,discovery_json,discovery_fetched_at,
		idp_metadata_url,idp_metadata_xml,idp_metadata_fetched_at,idp_metadata_error,idp_entity_id,idp_cert_pem,
		sp_cert_pem,sp_cert_not_after,sp_key_enc,allow_idp_initiated,clock_skew_sec,
		attr_upn,attr_email,attr_display,attr_groups,attr_external_id,session_hours,icon,link_by,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Kind, p.Slug, p.Name, boolInt(p.Enabled), p.Provisioning,
		nullZero(p.DefaultGroup), p.DefaultRole, p.DefaultExpiryDays, boolInt(p.AllowAdminRole),
		p.Issuer, p.ClientID, p.ClientSecretEnc, p.Scopes, p.DiscoveryJSON, p.DiscoveryFetched,
		p.IdPMetadataURL, p.IdPMetadataXML, p.IdPMetadataFetched, p.IdPMetadataError, p.IdPEntityID, p.IdPCertPEM,
		p.SPCertPEM, p.SPCertNotAfter, p.SPKeyEnc, boolInt(p.AllowIdPInit), p.ClockSkewSec,
		p.AttrUPN, p.AttrEmail, p.AttrDisplay, p.AttrGroups, p.AttrExternalID, p.SessionHours, p.Icon, p.LinkBy,
		nowStr(), nowStr())
}

// DeleteSSOProvider removes a provider and its rules. Existing identity links are left alone on
// purpose: they record who signed in as whom, and deleting a misconfigured provider should not
// silently orphan accounts that were adopted through it.
func (s *Store) DeleteSSOProvider(id int64) error {
	if err := s.DeleteRulesOfProvider(id); err != nil {
		return err
	}
	_, err := s.exec(`DELETE FROM sso_providers WHERE id=?`, id)
	return err
}

// nullZero stores a zero id as NULL, keeping "unset" distinct from "group 0" in the column.
func nullZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// NoteSSOMetadataError records why a metadata refresh failed WITHOUT touching the cached document.
// Failing closed to last-known-good is deliberate: blanking the trust anchor on a failed fetch
// would take every login down, and an attacker who can break the fetch should not be able to
// induce that.
func (s *Store) NoteSSOMetadataError(id int64, msg string) {
	s.exec(`UPDATE sso_providers SET idp_metadata_error=?, updated_at=? WHERE id=?`, msg, nowStr(), id)
}

// SaveOIDCDiscovery caches a fetched discovery document so later logins do not wait on the IdP's
// well-known endpoint, and a briefly-unreachable IdP does not take sign-in down.
func (s *Store) SaveOIDCDiscovery(id int64, doc string) {
	s.exec(`UPDATE sso_providers SET discovery_json=?, discovery_fetched_at=?, updated_at=? WHERE id=?`,
		doc, nowStr(), nowStr(), id)
}
