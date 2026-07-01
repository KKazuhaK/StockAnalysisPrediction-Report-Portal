package main

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// mdRenderer：GitHub 风格 Markdown（表格/删除线/自动链接等）。
var mdRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// mdToHTML 把 Markdown 渲染成 HTML（新报告只给 body_md 时用）。失败则退化成转义 <pre>。
func mdToHTML(md string) string {
	var buf bytes.Buffer
	if err := mdRenderer.Convert([]byte(md), &buf); err != nil {
		return "<pre>" + template.HTMLEscapeString(md) + "</pre>"
	}
	return buf.String()
}
