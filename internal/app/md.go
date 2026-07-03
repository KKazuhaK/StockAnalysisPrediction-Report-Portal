package app

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// mdRenderer: GitHub-flavored Markdown (tables, strikethrough, autolinks, etc.).
var mdRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// mdToHTML renders Markdown to HTML. On failure it falls back to an escaped <pre>.
func mdToHTML(md string) string {
	var buf bytes.Buffer
	if err := mdRenderer.Convert([]byte(md), &buf); err != nil {
		return "<pre>" + template.HTMLEscapeString(md) + "</pre>"
	}
	return buf.String()
}

// htmlOf returns a report's HTML body. New reports only persist MD (storing a derived
// HTML copy alongside it would just be redundant bytes that can drift from the source),
// so HTML is rendered on demand here. Legacy-imported reports have no MD at all, so a
// stored HTML value is trusted as-is.
func htmlOf(rep Rep) string {
	if rep.HTML != "" {
		return rep.HTML
	}
	if rep.MD == "" {
		return ""
	}
	return mdToHTML(rep.MD)
}

// htmlToStore decides what HTML (if any) an ingest request should persist. MD is
// authoritative and HTML can always be re-derived from it (htmlOf), so once MD is
// present any caller-supplied HTML is dropped too — otherwise a caller could keep the
// redundant storage alive just by sending both fields. Only a true HTML-only submission
// (no MD at all — the legacy-import shape) keeps its HTML.
func htmlToStore(md, html string) string {
	if md != "" {
		return ""
	}
	return html
}
