package app

import (
	"bytes"
	"html/template"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// mdRenderer: GitHub-flavored Markdown (tables, strikethrough, autolinks, etc.).
var mdRenderer = goldmark.New(goldmark.WithExtensions(extension.GFM))

// pdfBodyPolicy preserves report typography while removing every element and attribute that can
// fetch a URL, execute code, or carry CSS. wkhtmltopdf runs server-side, so browser-oriented XSS
// sanitization alone is insufficient: an image, stylesheet, iframe, or CSS url() would become SSRF.
var pdfBodyPolicy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(
		"a", "abbr", "b", "blockquote", "br", "code", "dd", "del", "details", "div", "dl", "dt",
		"em", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "i", "kbd", "li", "ol", "p", "pre",
		"s", "small", "span", "strong", "sub", "summary", "sup", "table", "tbody", "td", "tfoot",
		"th", "thead", "tr", "u", "ul",
	)
	p.AllowAttrs("colspan", "rowspan").OnElements("td", "th")
	p.SkipElementsContent("script", "style", "iframe", "object", "embed", "svg", "math", "template")
	return p
}()

func sanitizePDFBody(body string) string { return pdfBodyPolicy.Sanitize(body) }

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
