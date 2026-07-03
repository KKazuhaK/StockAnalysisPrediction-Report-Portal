package app

// User is an account. Core fields (username/password/role) live in the `users`
// table; the extended attributes come from user_profiles (additive-only schema).
type User struct {
	Username     string
	PasswordHash string
	Role         string  // "admin" | "operator" | "user" (more roles can be added)
	DisplayName  string  // human-friendly name shown in the UI (falls back to username)
	Email        string  //
	Active       bool    // false = disabled; disabled accounts cannot log in
	LastLogin    string  // timestamp of the last successful login ("" = never)
	Groups       []int64 // organizational group ids this user belongs to
}

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
