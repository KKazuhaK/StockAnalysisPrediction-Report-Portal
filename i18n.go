package main

import "net/http"

// messages 多语言词条：lang → key → 文案。目前只填中文，英文以后批量补 "en"。
// 模板里用 {{t .Lang "key"}} 取；缺失自动回退中文、再回退 key，不会报错。
var messages = map[string]map[string]string{
	"zh": {
		"brand":        "研报门户",
		"nav.links":    "入口管理",
		"nav.types":    "类型管理",
		"nav.users":    "账号管理",
		"nav.settings": "系统设置",
		"nav.logout":   "退出",
		"theme.auto":   "跟随系统",
		"theme.light":  "浅色",
		"theme.dark":   "深色",
		"common.back":  "返回列表",
	},
	"en": {}, // 预留：以后批量补英文即可，缺失键自动回退中文
}

// 可选语言（顺序即下拉顺序）。
var langs = []struct{ Code, Name string }{
	{"zh", "中文"},
	{"en", "English"},
}

const langCookie = "rp_lang"

// T 取翻译；缺失回退中文、再回退 key。
func T(lang, key string) string {
	if m, ok := messages[lang]; ok {
		if v, ok := m[key]; ok && v != "" {
			return v
		}
	}
	if v, ok := messages["zh"][key]; ok {
		return v
	}
	return key
}

// langOf 从 cookie 取当前语言，默认中文。
func langOf(r *http.Request) string {
	if c, err := r.Cookie(langCookie); err == nil && messages[c.Value] != nil {
		return c.Value
	}
	return "zh"
}
