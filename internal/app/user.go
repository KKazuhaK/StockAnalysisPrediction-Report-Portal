package app

import "strings"

// normalizeUsername is the canonical form of an account name: trimmed and folded to lower case.
//
// It exists because userPrincipal folds and the storage does not. A read principal has to be stable,
// so it folds; `users.username` is a plain TEXT PRIMARY KEY, which is BINARY on SQLite and
// case-sensitive on Postgres. While the two disagreed, `Alice` and `alice` were two accounts sharing
// the one principal `u:alice` — each read what the other had been granted, and deleting either wiped
// both their viewer rows.
//
// The Passwall panel never had to write this down: its `upn` UNIQUE index is MySQL under a utf8mb4
// collation, so the database refuses the case variant for it. Neither of this project's drivers does,
// so the fold happens here, at every creation path, and the guards compare through it. Folding at
// write rather than comparing case-insensitively at read keeps every lookup an exact primary-key hit.
func normalizeUsername(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// User is an account. All fields are columns on the `users` table (the profile attributes
// were folded in from user_profiles, ADR 0013). The single primary group is carried
// out-of-band via PrimaryGroupOf/users.group_id, not on this struct.
type User struct {
	Username     string
	PasswordHash string
	Role         string // "admin" | "operator" | "user" (more roles can be added)
	DisplayName  string // human-friendly name shown in the UI (falls back to username)
	Email        string //
	Active       bool   // false = disabled; disabled accounts cannot log in
	LastLogin    string // timestamp of the last successful login ("" = never)
	// LastSeen is the last authenticated REQUEST, throttled (see lastSeenInterval). A separate fact
	// from LastLogin: "signed in Monday, still using it" and "signed in Monday, never came back"
	// are the two answers an admin consults this column to tell apart.
	LastSeen   string
	ExpiresAt  string // account validity cutoff as a panel-tz civil date "YYYY-MM-DD" ("" = never); see Server.accountExpired (ADR 0022 R4)
	SessionRev int64  // incremented on password changes; signed sessions carry this revision
	// CreatedAt identifies this INSTANCE of the account. session_rev cannot survive a deletion — a
	// recreated row is a fresh INSERT starting at zero again — so the signed session carries this
	// too, and a cookie issued to the previous holder of a reusable username stops resolving.
	// Empty on rows written before it was stamped; those keep their sessions (see verify).
	CreatedAt string
	Groups    []int64 // vestigial (group model B uses a single primary group_id); unused
	// Identity source (ADR 0023). Source is local | jit | scim and SourceRef names the provider that
	// owns the row, so a future sync only ever touches what it created. A row that predates SSO
	// reconciles to "local", never to federated.
	Source     string
	SourceRef  string
	ExternalID string // the IdP's immutable object id; the SSO<->SCIM join key
	// Restricted (ADR 0024) scopes this ACCOUNT's reads regardless of its OU, so a portal with no
	// OU tree can still have external users. ORs with the OU's own restricted flag.
	Restricted  bool
	TOTPEnabled bool // whether TOTP 2FA is confirmed and in force for this (local) account
}

// IsFederated reports whether the account is owned by an external identity provider, and therefore
// has no local password to check, reset, or attack.
func (u User) IsFederated() bool { return u.Source != "" && u.Source != "local" }

// Name returns the display name, falling back to the username.
func (u User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// EffRole returns the effective role (defaults to "user").
func (u User) EffRole() string {
	if u.Role != "" {
		return u.Role
	}
	return "user"
}

// IsAdmin reports whether the user is an administrator.
func (u User) IsAdmin() bool { return u.EffRole() == "admin" }
