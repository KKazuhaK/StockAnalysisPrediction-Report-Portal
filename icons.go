package main

import (
	"embed"
	"html/template"
	"regexp"
	"sync"
)

// 图标 = icons/ 下的 Tabler(MIT) SVG 文件。加图标只需丢一个 <name>.svg 进去，零改代码。
// 模板里用 {{icon "name"}} 调用；随字号缩放、随文字颜色变。
//
//go:embed icons/*.svg
var iconFS embed.FS

var (
	iconMu    sync.RWMutex
	iconCache = map[string]template.HTML{}
	reSvgBody = regexp.MustCompile(`(?s)<svg[^>]*>(.*)</svg>`)
	rePlace   = regexp.MustCompile(`<path stroke="none"[^/]*/>`)
)

func icon(name string) template.HTML {
	iconMu.RLock()
	c, ok := iconCache[name]
	iconMu.RUnlock()
	if ok {
		return c
	}
	out := template.HTML("")
	if b, err := iconFS.ReadFile("icons/" + name + ".svg"); err == nil {
		if m := reSvgBody.FindSubmatch(b); m != nil {
			inner := rePlace.ReplaceAll(m[1], nil) // 去掉占位的透明底 path
			out = template.HTML(`<svg class="ti" width="1em" height="1em" viewBox="0 0 24 24" ` +
				`fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" ` +
				`stroke-linejoin="round" aria-hidden="true">` + string(inner) + `</svg>`)
		}
	}
	iconMu.Lock()
	iconCache[name] = out
	iconMu.Unlock()
	return out
}
