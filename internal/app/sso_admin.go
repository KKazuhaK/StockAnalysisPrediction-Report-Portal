package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/ssorules"
)

// Admin API for SSO configuration (ADR 0023). Everything lives in the DB and is edited here;
// config.yaml stays infrastructure-only.
//
// One rule governs every response shape below: a secret is NEVER returned. Reads report only
// whether one is set, and a blank secret on write keeps the stored value — the same pattern the
// Dify api_key already uses, so admins do not have to re-enter credentials to rename something.

// The slugs a provider gets when an admin saves the SAML or OIDC tab without renaming anything.
// The admin UI offers one of each, so these are what the derived addresses are built from before
// a row exists — and they must be the same strings the client seeds an unsaved provider with.
const (
	defaultSAMLSlug = "saml"
	defaultOIDCSlug = "oidc"
)

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
		"allow_idp_initiated": p.AllowIdPInit, "clock_skew_sec": p.ClockSkewSec, "icon": p.Icon, "link_by": p.LinkBy,
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
	// The SP addresses for a provider that has NOT been saved yet. They depend only on the public
	// URL and the default slug, and the setup guide needs them first: configuring the IdP is step
	// one, saving the portal side is step four. Sending them only per stored row left a portal that
	// had never touched SSO showing the guide with two empty boxes.
	writeJSON(w, map[string]any{"providers": out, "public_url": s.publicBaseURL(),
		"sp_defaults": map[string]any{
			"saml": map[string]any{"sp_entity_id": s.samlEntityID(defaultSAMLSlug), "sp_acs_url": s.samlACSURL(defaultSAMLSlug)},
			"oidc": map[string]any{"redirect_url": s.oidcRedirectURL(defaultOIDCSlug)},
		}})
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
	Icon           string `json:"icon"`
	LinkBy         string `json:"link_by"`

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
	in.LinkBy = strings.ToLower(strings.TrimSpace(in.LinkBy))
	if !validLinkBy(in.LinkBy) {
		jsonErrorCode(w, http.StatusBadRequest, "sso_bad_link_by",
			"账号匹配方式只能是「仅身份绑定」、「用户名」或「邮箱」")
		return
	}
	in.Icon = strings.TrimSpace(in.Icon)
	if !validSSOIcon(in.Icon) {
		jsonErrorCode(w, http.StatusBadRequest, "sso_bad_icon",
			"图标只能是内置图标，或上传到本门户的图片")
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
		// A cached discovery document belongs to the issuer it came from. Repointing the provider
		// drops it so the next login refetches, rather than relying on the read-side guard alone.
		DiscoveryJSON: prev.DiscoveryJSON, DiscoveryFetched: prev.DiscoveryFetched,
		IdPMetadataURL: strings.TrimSpace(in.IdPMetadataURL),
		IdPMetadataXML: firstNonEmpty(strings.TrimSpace(in.IdPMetadataXML), prev.IdPMetadataXML),
		IdPEntityID:    prev.IdPEntityID, IdPCertPEM: prev.IdPCertPEM,
		AllowIdPInit: in.AllowIdPInit, ClockSkewSec: in.ClockSkewSec, Icon: in.Icon, LinkBy: in.LinkBy,
		SPCertPEM: prev.SPCertPEM, SPKeyEnc: prev.SPKeyEnc, SPCertNotAfter: prev.SPCertNotAfter,
		AttrUPN: in.AttrUPN, AttrEmail: in.AttrEmail, AttrDisplay: in.AttrDisplay,
		AttrGroups: in.AttrGroups, AttrExternalID: in.AttrExternalID,
		ClientSecretEnc: prev.ClientSecretEnc,
	}
	if existed && !sameIssuer(p.Issuer, prev.Issuer) {
		p.DiscoveryJSON, p.DiscoveryFetched = "", ""
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
			// The code, not just the sentence: every one of these is read by an admin mid-setup, on
			// a screen that may not be in English.
			var c ssoCoded
			if errors.As(err, &c) {
				jsonErrorCode(w, http.StatusBadRequest, c.code, c.msg)
			} else {
				jsonError(w, http.StatusBadRequest, err.Error())
			}
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

// The built-in login-button icons. Server-side only as an allow-list — the drawing lives in the
// SPA — because this value is rendered on the unauthenticated login page and an unknown name would
// render nothing, which looks like a broken deployment rather than a rejected setting.
var ssoIconPresets = map[string]bool{
	"entra": true, "google": true, "okta": true, "keycloak": true,
	"github": true, "gitlab": true, "auth0": true, "key": true, "shield": true,
}

// validSSOIcon accepts exactly two shapes and nothing that could cause a fetch.
//
// The login page is unauthenticated, so anything it loads from another host announces every
// visitor to that host before they have signed in. A path this portal serves itself does not, and
// a preset is not a resource at all. Refusing a data: URI is a separate reason: an inline SVG on a
// public page is a script vector.
func validSSOIcon(icon string) bool {
	switch {
	case icon == "":
		return true
	case strings.HasPrefix(icon, "preset:"):
		return ssoIconPresets[strings.TrimPrefix(icon, "preset:")]
	case strings.HasPrefix(icon, "/site-assets/"):
		name := strings.TrimPrefix(icon, "/site-assets/")
		// One path segment, no traversal, no query. The asset handler does its own base-name check;
		// this refuses at the door so a nonsense value never reaches the login page at all.
		return name != "" && name == path.Base(name) && !strings.ContainsAny(name, "?#\\")
	}
	return false
}

// ssoCoded is a refusal the client can translate. The message is the fallback for a client with no
// string for the code — never the only thing the admin gets.
type ssoCoded struct{ code, msg string }

func (e ssoCoded) Error() string { return e.msg }

// validateProviderForEnable refuses to put a provider in front of users until it can actually work.
// Failing here is much kinder than failing mid-login with an opaque error.
func (s *Server) validateProviderForEnable(p *SSOProvider) error {
	base := s.publicBaseURL()
	if base == "" {
		return ssoCoded{"sso_need_public_url", "set the Public URL (Manage \u2192 General) before enabling SSO \u2014 the redirect and ACS URLs derive from it"}
	}
	if p.Provisioning == "jit" {
		if p.DefaultGroup == 0 {
			return ssoCoded{"sso_need_default_group", "choose a default group: a just-in-time account must land in a known group"}
		}
		if !s.st.GroupExists(p.DefaultGroup) {
			return ssoCoded{"sso_group_gone", "the default group no longer exists"}
		}
		// The default group must be an EXTERNAL one. Landing a self-provisioned account in an
		// unrestricted group would give whoever the IdP admits the run of the portal — the exact
		// outcome the external-user model (ADR 0022) exists to prevent, reached by an easy
		// misconfiguration rather than an attack.
		if !s.st.GroupRestrictedEffective(p.DefaultGroup) {
			return ssoCoded{"sso_group_not_restricted", "the default group must be an external (restricted) group \u2014 otherwise a self-created account could see everything"}
		}
	}
	switch p.Kind {
	case "oidc":
		if p.Issuer == "" || p.ClientID == "" || p.ClientSecretEnc == "" {
			return ssoCoded{"sso_oidc_incomplete", "issuer, client id and client secret are required"}
		}
		// Fetch discovery through the SSRF guard, so a bad or hostile issuer fails at save time.
		if err := s.ssoClient().checkURL(p.Issuer + "/.well-known/openid-configuration"); err != nil {
			return ssoCoded{"sso_issuer_unreachable", "issuer is not reachable: " + err.Error()}
		}
	case "saml":
		if !strings.HasPrefix(base, "https://") {
			return ssoCoded{"sso_need_https", "SAML requires an https Public URL: the assertion is posted cross-site, so its cookie must be Secure"}
		}
		if strings.TrimSpace(p.IdPMetadataXML) == "" {
			return ssoCoded{"sso_need_metadata", "fetch or paste the IdP metadata first"}
		}
		if _, err := parseIdPMetadata(p.IdPMetadataXML); err != nil {
			return ssoCoded{"sso_metadata_unusable", "IdP metadata is not usable: " + err.Error()}
		}
		if p.SPKeyEnc == "" {
			return ssoCoded{"sso_no_sp_cert", "the SP certificate has not been generated yet"}
		}
	}
	return nil
}

// apiAdminSSOFetchMetadata pulls IdP metadata over the SSRF-guarded client. It FAILS CLOSED: a
// failed fetch leaves the previously-stored document in place, because blanking the trust anchor
// would take every login down and, worse, could be induced deliberately.
//
// It takes the URL from the REQUEST, falling back to the stored one, and creates a disabled draft
// when no provider row exists yet. Both are needed to break a deadlock in first-time SAML setup:
// enabling SAML requires stored metadata (validateProviderForEnable), so a provider with the enable
// switch already on cannot be saved — and this endpoint used to require the very row that could not
// be saved. An admin filling the form top to bottom got "no such provider" here and "fetch or paste
// the IdP metadata first" from save, with nothing saying which order to do them in.
//
// The draft is DISABLED. Pressing fetch is not consent to put a provider in front of users.
func (s *Server) apiAdminSSOFetchMetadata(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Kind           string `json:"kind"`
		IdPMetadataURL string `json:"idp_metadata_url"`
	}
	_ = readJSON(r, &in) // an empty body is fine: it means "use what is stored"

	slug := r.PathValue("slug")
	p, existed := s.st.SSOProviderBySlug(slug)
	if !existed {
		kind := strings.TrimSpace(in.Kind)
		if kind != "saml" {
			jsonErrorCode(w, http.StatusBadRequest, "sso_bad_kind", "只有 SAML 提供商需要拉取元数据")
			return
		}
		// Only the fields this endpoint owns. Everything else the admin typed is still in the form
		// and lands on the next save — this draft exists so the metadata has somewhere to go.
		p = SSOProvider{Slug: slug, Kind: kind, Enabled: false}
	}
	if u := strings.TrimSpace(in.IdPMetadataURL); u != "" {
		p.IdPMetadataURL = u // what the admin just typed wins over what was stored
	}
	if p.IdPMetadataURL == "" {
		jsonErrorCode(w, http.StatusBadRequest, "sso_no_metadata_url", "请先填写 IdP 元数据地址")
		return
	}
	body, err := s.ssoClient().fetch(p.IdPMetadataURL, 2<<20)
	if err != nil {
		if p.ID != 0 {
			s.st.NoteSSOMetadataError(p.ID, err.Error())
		}
		jsonErrorCode(w, http.StatusBadGateway, "sso_metadata_unreachable", "无法拉取元数据："+err.Error())
		return
	}
	meta, err := parseIdPMetadata(string(body))
	if err != nil {
		if p.ID != 0 {
			s.st.NoteSSOMetadataError(p.ID, err.Error())
		}
		jsonErrorCode(w, http.StatusBadRequest, "sso_metadata_unusable", "元数据无法使用："+err.Error())
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
	stored := s.st.SSORules()
	out := make([]map[string]any, 0, len(stored))
	engine := make([]ssorules.Rule, 0, len(stored))
	for _, r := range stored {
		out = append(out, map[string]any{"id": r.ID, "provider_id": r.ProviderID, "ord": r.Ord,
			"enabled": r.Enabled, "attr": r.Attr, "value": r.Value, "target_role": r.TargetRole,
			"target_group": r.TargetGroup, "keep_on_miss": r.KeepOnMiss, "ci": r.CI, "note": r.Note})
		engine = append(engine, ssorules.Rule{ID: r.ID, Ord: r.Ord, Enabled: r.Enabled,
			Attr: r.Attr, Value: r.Value})
	}
	// Which rules can never win, so the page can say so. First match wins, so a rule sitting behind
	// an earlier one on the same attribute and value is unreachable — and an unreachable rule reads
	// to an admin exactly like a granted permission, which is the whole reason to compute this.
	shadowed := ssorules.Shadowed(engine)
	if shadowed == nil {
		shadowed = []int64{}
	}
	writeJSON(w, map[string]any{"rules": out, "shadowed": shadowed})
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
	rules := make([]storedRule, 0, len(in.Rules))
	for _, r := range in.Rules {
		rules = append(rules, storedRule{
			ProviderID: r.ProviderID, Enabled: r.Enabled, Attr: r.Attr, Value: r.Value,
			TargetRole: r.TargetRole, TargetGroup: r.TargetGroup,
			KeepOnMiss: r.KeepOnMiss, CI: r.CI, Note: r.Note,
		})
	}
	// One statement, so a save can never land half-applied — which the previous delete-then-reinsert
	// transaction could only avoid by being a transaction.
	if err := s.st.SaveSSORules(rules); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// ---------- an account's identity-provider binding ----------

// GET /api/admin/users/{name}/identity — which IdP account this person signs in as.
//
// The store could answer this from the day SSO shipped and nothing asked it, so an admin had no way
// to see the binding, and no way to cut one except by deleting the account. The subject and issuer
// are shown because they are the join key an operator has to match against the IdP's own console;
// the stored attribute blob is not, since it is whatever the IdP last sent and may hold anything.
func (s *Server) apiAdminUserIdentity(w http.ResponseWriter, r *http.Request, user string) {
	name := r.PathValue("name")
	if s.st.GetUser(name) == nil {
		jsonError(w, http.StatusNotFound, "no such user")
		return
	}
	ids := s.st.IdentitiesOf(name)
	if len(ids) == 0 {
		// Not an error: most accounts are local. The page needs to say "none", not fail.
		writeJSON(w, map[string]any{"identity": nil})
		return
	}
	id := ids[0]
	writeJSON(w, map[string]any{"identity": map[string]any{
		"provider": id.Provider, "issuer": id.Issuer, "subject": id.Subject, "slug": id.ProviderSlug,
	}})
}

// DELETE /api/admin/users/{name}/identity — cut the binding, keep the account.
//
// Worth having separately from deleting the account: a link outlives the person's IdP account, and
// while it stands, whoever the IdP later issues that same subject to would sign in as this account.
// Unlinking also returns the row to local — a federated account has no usable password path — so an
// admin should set a password afterwards if the person is to keep signing in.
func (s *Server) apiAdminUserUnlink(w http.ResponseWriter, r *http.Request, user string) {
	name := r.PathValue("name")
	u := s.st.GetUser(name)
	if u == nil {
		jsonError(w, http.StatusNotFound, "no such user")
		return
	}
	ids := s.st.IdentitiesOf(name)
	if len(ids) == 0 {
		writeJSON(w, okJSON) // already unlinked; a second click must not 500
		return
	}
	id := ids[0]
	if err := s.st.UnlinkIdentity(id.Provider, id.Issuer, id.Subject); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The account is local again, and its sessions end: the person who held it via the IdP must not
	// keep a live session on an account they can no longer prove they own.
	s.st.SetUserSource(name, "local", "")
	s.st.BumpSessionRev(name)
	// An administrator acting on somebody else's account, so the actor and the target differ — and
	// the target is the account, so it lands on that account's timeline next to its sign-ins.
	s.recordChange(r, user, AuditIdentityUnlink, "user", name,
		map[string]any{"provider": id.Provider, "slug": id.ProviderSlug, "subject": id.Subject})
	log.Printf("sso: admin %s revoked the %s/%s binding on %s", user, id.Provider, id.ProviderSlug, name)
	writeJSON(w, okJSON)
}
