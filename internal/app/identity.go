package app

import (
	"database/sql"
	"fmt"
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
	err := s.queryRow(`SELECT username FROM users
		WHERE sso_provider=? AND sso_issuer=? AND sso_subject=?`,
		provider, issuer, subject).Scan(&u)
	if err != nil || u.String == "" {
		return "", false
	}
	return u.String, true
}

// LinkIdentity binds an external identity to an account, or refreshes it on a repeat login. It is
// idempotent: the same identity signing in again updates the last-seen data rather than failing.
//
// ONE identity per account, enforced by the shape rather than by a rule someone has to remember —
// the columns live on the users row, so binding a new identity replaces the previous one. And no
// two accounts may hold the same identity: idx_users_sso_identity refuses that, which turns what
// would otherwise be a silent theft (the second sign-in quietly repointing the link and locking the
// first account out of SSO) into an error at the moment it is attempted.
func (s *Store) LinkIdentity(id Identity) error {
	if id.Username == "" || id.Provider == "" || id.Issuer == "" || id.Subject == "" {
		return fmt.Errorf("identity is incomplete")
	}
	res, err := s.exec(`UPDATE users SET sso_provider=?, sso_issuer=?, sso_subject=?, sso_slug=?,
			sso_nameid_format=?, sso_attrs=?, sso_linked_at=?
		WHERE username=?`,
		id.Provider, id.Issuer, id.Subject, id.ProviderSlug, id.NameIDFormat, id.Attrs,
		nowStr(), id.Username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no such account: %s", id.Username)
	}
	return nil
}

// IdentitiesOf returns the external identity bound to an account: at most one, so the slice is
// there only to keep the admin API shape stable for a UI that lists them.
func (s *Store) IdentitiesOf(username string) []Identity {
	var provider, issuer, subject, slug, format sql.NullString
	err := s.queryRow(`SELECT COALESCE(sso_provider,''),COALESCE(sso_issuer,''),COALESCE(sso_subject,''),
		COALESCE(sso_slug,''),COALESCE(sso_nameid_format,'') FROM users WHERE username=?`, username).
		Scan(&provider, &issuer, &subject, &slug, &format)
	if err != nil || subject.String == "" {
		return nil
	}
	return []Identity{{
		Provider: provider.String, Issuer: issuer.String, Subject: subject.String,
		Username: username, ProviderSlug: slug.String, NameIDFormat: format.String,
	}}
}

// UnlinkIdentity removes one external login binding.
func (s *Store) UnlinkIdentity(provider, issuer, subject string) error {
	_, err := s.exec(`UPDATE users SET sso_provider='', sso_issuer='', sso_subject='', sso_slug='',
			sso_nameid_format='', sso_attrs='', sso_linked_at=NULL
		WHERE sso_provider=? AND sso_issuer=? AND sso_subject=?`,
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
		// A group id that no longer exists (a deleted OU, a typo in a rule) must NOT be applied:
		// SetPrimaryGroup treats an unknown id as "clear", which would drop the user into the
		// unrestricted Default group — turning a misconfiguration into a silent privilege
		// escalation. Refuse instead, so the login fails visibly and the rule gets fixed.
		if !s.st.GroupExists(group) {
			return fmt.Errorf("rule targets group %d, which does not exist", group)
		}
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
	s.queryRow(`SELECT sso_attrs FROM users WHERE sso_slug=? AND COALESCE(sso_attrs,'')<>''
		ORDER BY sso_linked_at DESC LIMIT 1`, providerSlug).Scan(&v)
	return v.String
}

// GroupExists reports whether an OU id is real. Used before applying a rule's target, so a dangling
// reference fails the login rather than silently clearing the user's group.
func (s *Store) GroupExists(id int64) bool {
	if id == 0 {
		return false
	}
	var n int
	s.queryRow(`SELECT COUNT(*) FROM user_groups WHERE id=?`, id).Scan(&n)
	return n > 0
}
