package app

// User is an account. All fields are columns on the `users` table (the profile attributes
// were folded in from user_profiles, ADR 0013). The single primary group is carried
// out-of-band via PrimaryGroupOf/users.group_id, not on this struct.
type User struct {
	Username     string
	PasswordHash string
	Role         string  // "admin" | "operator" | "user" (more roles can be added)
	DisplayName  string  // human-friendly name shown in the UI (falls back to username)
	Email        string  //
	Active       bool    // false = disabled; disabled accounts cannot log in
	LastLogin    string  // timestamp of the last successful login ("" = never)
	ExpiresAt    string  // account validity cutoff as a panel-tz civil date "YYYY-MM-DD" ("" = never); see Server.accountExpired (ADR 0022 R4)
	SessionRev   int64   // incremented on password changes; signed sessions carry this revision
	Groups       []int64 // vestigial (group model B uses a single primary group_id); unused
	// Identity source (ADR 0023). Source is local | jit | scim and SourceRef names the provider that
	// owns the row, so a future sync only ever touches what it created. A row that predates SSO
	// reconciles to "local", never to federated.
	Source      string
	SourceRef   string
	ExternalID  string // the IdP's immutable object id; the SSO<->SCIM join key
	TOTPEnabled bool   // whether TOTP 2FA is confirmed and in force for this (local) account
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
