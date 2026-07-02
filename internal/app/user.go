package app

// User is an account (stored in the DB; this type is also used by the store).
type User struct {
	Username     string
	PasswordHash string
	Role         string // "admin" | "user" (more roles can be added)
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
