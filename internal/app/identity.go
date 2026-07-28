package app

import (
	"database/sql"
	"strings"
	"unicode"
)

// Identity linking and account provisioning for SSO (ADR 0023).
//
// The one rule everything here exists to enforce: an external identity is linked to a local account
// by (provider, issuer, subject) and by NOTHING else. Never by email — an email claim is mutable and,
// on some IdPs, settable by an administrator of an unrelated tenant, which is the nOAuth
// account-takeover class.

// Identity is one external login bound to a local account.
type Identity struct {
	Provider     string // "saml" | "oidc"
	Issuer       string // OIDC iss / SAML IdP entity id, verbatim
	Subject      string // OIDC sub / SAML NameID, verbatim
	Username     string
	ProviderSlug string
	NameIDFormat string
	Attrs        string // last-seen claims, for admin diagnostics
}

// FindIdentity resolves an external identity to its local account. The comparison is a plain `=` on
// all three key columns — deliberately not likeOp(), which is ILIKE on Postgres and would make the
// authentication boundary depend on the database driver.
func (s *Store) FindIdentity(provider, issuer, subject string) (string, bool) {
	if provider == "" || issuer == "" || subject == "" {
		return "", false
	}
	var u sql.NullString
	err := s.queryRow(`SELECT username FROM user_identities WHERE provider=? AND issuer=? AND subject=?`,
		provider, issuer, subject).Scan(&u)
	if err != nil || u.String == "" {
		return "", false
	}
	return u.String, true
}

// LinkIdentity binds an external identity to an account, or refreshes it on a repeat login. It is
// idempotent: the same identity logging in again updates the last-seen data rather than failing.
func (s *Store) LinkIdentity(id Identity) error {
	_, err := s.exec(`INSERT INTO user_identities(provider,issuer,subject,username,provider_slug,nameid_format,attrs,created_at,last_login_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(provider,issuer,subject) DO UPDATE SET
			username=excluded.username, provider_slug=excluded.provider_slug,
			nameid_format=excluded.nameid_format, attrs=excluded.attrs, last_login_at=excluded.last_login_at`,
		id.Provider, id.Issuer, id.Subject, id.Username, id.ProviderSlug, id.NameIDFormat, id.Attrs,
		nowStr(), nowStr())
	return err
}

// IdentitiesOf lists the external identities bound to an account, so an admin can see and unlink
// them. Ordered for a stable UI.
func (s *Store) IdentitiesOf(username string) []Identity {
	rows, err := s.query(`SELECT provider,issuer,subject,username,COALESCE(provider_slug,''),COALESCE(nameid_format,'')
		FROM user_identities WHERE username=? ORDER BY provider, issuer`, username)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Identity
	for rows.Next() {
		var id Identity
		if rows.Scan(&id.Provider, &id.Issuer, &id.Subject, &id.Username, &id.ProviderSlug, &id.NameIDFormat) == nil {
			out = append(out, id)
		}
	}
	return out
}

// UnlinkIdentity removes one external login binding.
func (s *Store) UnlinkIdentity(provider, issuer, subject string) error {
	_, err := s.exec(`DELETE FROM user_identities WHERE provider=? AND issuer=? AND subject=?`,
		provider, issuer, subject)
	return err
}

// FindUserByExternalID finds an account pre-provisioned for this IdP object id — an admin who filled
// it in ahead of time, or (later) SCIM. It is what lets a first SSO login ADOPT the intended account
// instead of creating a duplicate. Scoped by provider so two IdPs cannot collide on the same id.
func (s *Store) FindUserByExternalID(sourceRef, externalID string) (string, bool) {
	if externalID == "" {
		return "", false
	}
	var u sql.NullString
	err := s.queryRow(`SELECT username FROM users WHERE source_ref=? AND external_id=?`,
		sourceRef, externalID).Scan(&u)
	if err != nil || u.String == "" {
		return "", false
	}
	return u.String, true
}

// SetUserExternalID records the IdP's immutable object id for an account.
func (s *Store) SetUserExternalID(username, sourceRef, externalID string) error {
	_, err := s.exec(`UPDATE users SET source_ref=?, external_id=?, updated_at=? WHERE username=?`,
		sourceRef, externalID, nowStr(), username)
	return err
}

// SetUserSource records where an account came from. Only a sync that owns a row (matching source and
// source_ref) may later modify it, which is what keeps a future SCIM run from touching local or
// manually-created accounts.
func (s *Store) SetUserSource(username, source, sourceRef string) error {
	_, err := s.exec(`UPDATE users SET source=?, source_ref=?, updated_at=? WHERE username=?`,
		source, sourceRef, nowStr(), username)
	return err
}

// ssoUsernameMax bounds a generated username. The mapped UPN is attacker-influenced (it comes from
// the IdP), and username is a bare-string foreign key all over this schema.
const ssoUsernameMax = 64

// sanitizeSSOUsername turns a mapped UPN into a username we are willing to create: the local part of
// an address, lowercased, reduced to a conservative charset, collapsed and bounded. ok=false when
// nothing usable remains — the caller must then refuse rather than invent a name.
func sanitizeSSOUsername(upn string) (string, bool) {
	v := strings.TrimSpace(upn)
	if at := strings.IndexByte(v, '@'); at > 0 { // keep the local part; "@acme.com" alone is not a name
		v = v[:at]
	}
	v = strings.ToLower(v)
	var b strings.Builder
	lastDash := false
	for _, r := range v {
		switch {
		case unicode.IsLetter(r) && r < unicode.MaxASCII, unicode.IsDigit(r), r == '.', r == '_', r == '-':
			b.WriteRune(r)
			lastDash = false
		default:
			// Anything else (including a domain separator or a path character) collapses to a
			// single dash, so "ACME\jdoe" and "weird//name" stay readable and predictable.
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-.")
	if len(out) > ssoUsernameMax {
		out = strings.Trim(out[:ssoUsernameMax], "-.")
	}
	if out == "" {
		return "", false
	}
	return out, true
}

// BumpSessionRev invalidates every session already issued for an account. Sessions carry the
// revision they were signed with, so incrementing it is how a privilege change takes effect
// immediately rather than at cookie expiry.
func (s *Store) BumpSessionRev(username string) error {
	_, err := s.exec(`UPDATE users SET session_rev=COALESCE(session_rev,0)+1, updated_at=? WHERE username=?`,
		nowStr(), username)
	return err
}

// applyAssignment writes a resolved role and OU onto an account, and invalidates existing sessions
// when either actually changed — a downgrade must take effect now, not whenever the 7-day cookie
// happens to expire. An unchanged assignment is a no-op, so an ordinary repeat login does not log
// the user out of their other devices.
func (s *Server) applyAssignment(username, role string, group int64) error {
	u := s.st.GetUser(username)
	if u == nil {
		return sql.ErrNoRows
	}
	changed := false
	if role != "" && role != u.EffRole() {
		if err := s.st.SetUserRole(username, validRole(role)); err != nil {
			return err
		}
		changed = true
	}
	if group != 0 && group != s.st.PrimaryGroupOf(username) {
		if err := s.st.SetPrimaryGroup(username, group); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return s.st.BumpSessionRev(username)
}

// LastSeenAttrs returns the claims recorded on the most recent sign-in through a provider. It backs
// the admin "what did the IdP actually send?" view, which is what makes attribute mapping a glance
// rather than trial and error.
func (s *Store) LastSeenAttrs(providerSlug string) string {
	var v sql.NullString
	s.queryRow(`SELECT attrs FROM user_identities WHERE provider_slug=? AND attrs<>''
		ORDER BY last_login_at DESC LIMIT 1`, providerSlug).Scan(&v)
	return v.String
}
