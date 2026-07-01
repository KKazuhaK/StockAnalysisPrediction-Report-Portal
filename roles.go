package main

// 权限点。加新权限在这里定义常量，再在下面的角色里授予；handler 用 requirePerm 门禁。
const (
	PermManage = "manage" // 访问所有管理页(入口/类型/账号/系统设置)
)

// Role 角色定义。加一个角色 = 往 roleRegistry 追加一项，UI 下拉与鉴权自动生效。
type Role struct {
	Code  string          // 存库的值
	Name  string          // 显示名
	Perms map[string]bool // 拥有的权限点
}

var roleRegistry = []Role{
	{Code: "admin", Name: "管理员", Perms: map[string]bool{PermManage: true}},
	{Code: "user", Name: "普通用户", Perms: map[string]bool{}}, // 只读浏览
	// 以后扩展示例：
	// {Code: "editor", Name: "编辑", Perms: map[string]bool{PermManageLinks: true}},
}

func roleByCode(code string) *Role {
	for i := range roleRegistry {
		if roleRegistry[i].Code == code {
			return &roleRegistry[i]
		}
	}
	return nil
}

// validRole 未知角色回退为 user。
func validRole(code string) string {
	if roleByCode(code) != nil {
		return code
	}
	return "user"
}

// can 判断某角色是否拥有某权限点。
func can(role, perm string) bool {
	if r := roleByCode(role); r != nil {
		return r.Perms[perm]
	}
	return false
}
