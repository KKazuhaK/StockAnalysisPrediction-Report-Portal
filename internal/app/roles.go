package app

// Permissions. To add a new permission, define a constant here, then grant it to roles below; handlers gate access via requirePermJSON.
const (
	PermManage   = "manage"    // Access to all admin pages (entries/types/accounts/system settings)
	PermRunBatch = "run_batch" // Execute already-configured batch jobs (see docs/adr/0001-batch-run-engine.md)
)

// Role defines a role. Adding a role = appending an entry to roleRegistry; the UI dropdown and authorization take effect automatically.
type Role struct {
	Code  string          // Value stored in the database
	Name  string          // Display name
	Perms map[string]bool // Permissions granted
}

var roleRegistry = []Role{
	{Code: "admin", Name: "管理员", Perms: map[string]bool{PermManage: true, PermRunBatch: true}},
	{Code: "operator", Name: "执行员", Perms: map[string]bool{PermRunBatch: true}}, // Runs configured batch jobs; no admin access
	{Code: "user", Name: "普通用户", Perms: map[string]bool{}},                      // Read-only browsing
}

func roleByCode(code string) *Role {
	for i := range roleRegistry {
		if roleRegistry[i].Code == code {
			return &roleRegistry[i]
		}
	}
	return nil
}

// validRole falls back to user for unknown roles.
func validRole(code string) string {
	if roleByCode(code) != nil {
		return code
	}
	return "user"
}

// can reports whether a given role holds a given permission.
func can(role, perm string) bool {
	if r := roleByCode(role); r != nil {
		return r.Perms[perm]
	}
	return false
}

// permsOf returns the permission set held by a role, for exposing to the UI so it
// can gate navigation (e.g. show the batch console only to holders of PermRunBatch).
func permsOf(role string) map[string]bool {
	out := map[string]bool{}
	if r := roleByCode(role); r != nil {
		for p, granted := range r.Perms {
			if granted {
				out[p] = true
			}
		}
	}
	return out
}
