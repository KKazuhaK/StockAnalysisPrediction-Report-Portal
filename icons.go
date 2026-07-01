package main

import "html/template"

// 图标 = Tabler(MIT) 官方 webfont（整套 5800+，static/vendor/tabler-icons.min.css）。
// 任意图标名直接可用：{{icon "报告名"}} 或模板里直接写 <i class="ti ti-xxx"></i>。
// 名字见 https://tabler.io/icons（去掉 ti- 前缀）。.ti 样式在 style.css 里统一对齐。
func icon(name string) template.HTML {
	return template.HTML(`<i class="ti ti-` + template.HTMLEscapeString(name) + `" aria-hidden="true"></i>`)
}
