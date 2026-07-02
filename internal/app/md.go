package app

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// mdRenderer: GitHub-flavored Markdown (tables, strikethrough, autolinks, etc.).
var mdRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// mdToHTML renders Markdown to HTML (used when a new report only provides body_md). On failure it falls back to an escaped <pre>.
func mdToHTML(md string) string {
	var buf bytes.Buffer
	if err := mdRenderer.Convert([]byte(md), &buf); err != nil {
		return "<pre>" + template.HTMLEscapeString(md) + "</pre>"
	}
	return buf.String()
}
