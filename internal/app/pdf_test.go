package app

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// PDF export reads rep.HTML directly (see templates/pdf.html), but md-only reports no
// longer persist a stored HTML copy. renderPDFHTML must derive it from MD on the fly so
// export keeps working without wkhtmltopdf even being involved in this test.
func TestRenderPDFHTMLDerivesFromMD(t *testing.T) {
	s := &Server{}
	s.parseTemplates()

	out, err := s.renderPDFHTML(&Rep{Title: "t", MD: "# hi"}, "alice")
	if err != nil {
		t.Fatalf("renderPDFHTML: %v", err)
	}
	if !strings.Contains(out, "<h1>hi</h1>") {
		t.Errorf("rendered PDF html = %q, want it to contain the rendered heading", out)
	}
}

func TestRenderPDFHTMLSanitizesStoredHTMLForLegacyReports(t *testing.T) {
	s := &Server{}
	s.parseTemplates()

	out, err := s.renderPDFHTML(&Rep{Title: "t", HTML: `<p>legacy body</p><img src="http://169.254.169.254/latest/meta-data"><script>alert(1)</script><iframe src="http://127.0.0.1:8790/api/admin/tokens"></iframe>`}, "alice")
	if err != nil {
		t.Fatalf("renderPDFHTML: %v", err)
	}
	if !strings.Contains(out, "<p>legacy body</p>") {
		t.Errorf("rendered PDF html = %q, want safe legacy markup preserved", out)
	}
	for _, dangerous := range []string{"169.254.169.254", "127.0.0.1", "<script", "<iframe"} {
		if strings.Contains(out, dangerous) {
			t.Errorf("rendered PDF html contains unsafe content %q: %s", dangerous, out)
		}
	}
}

func TestRenderPDFHTMLSplicesUserScopedMermaidSVG(t *testing.T) {
	s := &Server{}
	s.parseTemplates()
	source := "flowchart LR\nA[开始] --> B[结束]"
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 80"><rect x="1" y="1" width="198" height="78" fill="#fff"/><text x="20" y="40" font-size="16">开始</text></svg>`
	if err := s.putMermaidChart("alice", source, "light", svg); err != nil {
		t.Fatalf("putMermaidChart: %v", err)
	}

	out, err := s.renderPDFHTML(&Rep{Title: "t", MD: "```mermaid\n" + source + "\n```"}, "alice")
	if err != nil {
		t.Fatalf("renderPDFHTML: %v", err)
	}
	if !strings.Contains(out, "<svg") || !strings.Contains(out, "开始") {
		t.Fatalf("rendered PDF html does not contain the cached chart: %s", out)
	}
	if strings.Contains(out, "flowchart LR") {
		t.Fatalf("rendered PDF html still contains Mermaid source: %s", out)
	}

	other, err := s.renderPDFHTML(&Rep{Title: "t", MD: "```mermaid\n" + source + "\n```"}, "bob")
	if err != nil {
		t.Fatalf("renderPDFHTML for another user: %v", err)
	}
	if strings.Contains(other, "<svg") || !strings.Contains(other, "flowchart LR") {
		t.Fatalf("chart cache was not user scoped: %s", other)
	}
}

func TestRenderPDFHTMLMatchesMermaidCacheForIndentedFence(t *testing.T) {
	s := &Server{}
	s.parseTemplates()
	source := "flowchart LR\nA[Start] --> B[End]"
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 80"><rect x="1" y="1" width="198" height="78" fill="#fff"/><text x="20" y="40" font-size="16">Start</text></svg>`
	if err := s.putMermaidChart("alice", source, "light", svg); err != nil {
		t.Fatalf("putMermaidChart: %v", err)
	}

	md := "  ```mermaid\n  flowchart LR\n  A[Start] --> B[End]\n  ```"
	out, err := s.renderPDFHTML(&Rep{Title: "t", MD: md}, "alice")
	if err != nil {
		t.Fatalf("renderPDFHTML: %v", err)
	}
	if !strings.Contains(out, "<svg") || strings.Contains(out, "Mermaid chart was not cached") {
		t.Fatalf("indented Mermaid fence did not match the browser cache key: %s", out)
	}
}

func TestSanitizeMermaidSVGRejectsFetchAndExecutableContent(t *testing.T) {
	bad := []string{
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="http://169.254.169.254/x"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><text>x</text></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><path marker-end="url(http://internal/marker)" d="M0 0L1 1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><defs><marker id="arrow"><path d="M0 0L1 1"/></marker></defs><path marker-end="url(#arrow)" d="M0 0L1 1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div>unsafe</div></foreignObject></svg>`,
	}
	for _, input := range bad {
		if _, err := sanitizeMermaidSVG(input); err == nil {
			t.Errorf("sanitizeMermaidSVG accepted unsafe input: %s", input)
		}
	}
}

// The browser inlines its locally computed font-family into the chart SVG, so a Windows client
// sends "Microsoft YaHei" — a font the render container does not have. Every chart must come out
// pinned to the same face the report body uses, or chart labels render in a fallback font.
func TestSanitizeMermaidSVGPinsChartFontToTheContainerFont(t *testing.T) {
	in := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">` +
		`<text font-family="Microsoft YaHei, sans-serif" font-size="12">图表</text>` +
		`<text font-family="&#34;Helvetica Neue&#34;, Arial">label</text></svg>`

	out, err := sanitizeMermaidSVG(in)
	if err != nil {
		t.Fatalf("sanitizeMermaidSVG: %v", err)
	}
	if strings.Contains(out, "Microsoft YaHei") || strings.Contains(out, "Helvetica Neue") {
		t.Errorf("sanitized SVG kept a client-side font: %s", out)
	}
	if got := strings.Count(out, pdfFontFamily); got != 2 {
		t.Errorf("sanitized SVG pinned %d of 2 font-family attributes to %q: %s", got, pdfFontFamily, out)
	}
	// The attribute must come out byte-identical to the constant. It does not if a family name is
	// quoted: encoding/xml escapes a double quote to &#34;, and the count above would then miss.
	if strings.Contains(pdfFontFamily, `"`) {
		t.Errorf("pdfFontFamily = %q; quoting a family name makes the emitted SVG attribute unreadable", pdfFontFamily)
	}
}

// fontFamilies splits a CSS font-family list into normalized family names.
func fontFamilies(list string) []string {
	out := []string{}
	for _, f := range strings.Split(list, ",") {
		if f = strings.Trim(strings.TrimSpace(f), `"'`); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// The "keep in sync" comments on pdfFontFamily and on the body rule in templates/pdf.html were the
// only thing holding the two stacks together, and a comment does not fail a build. A chart label and
// the paragraph beside it resolving to different faces is exactly the bug this pins down.
func TestPDFTemplateAndChartFontStacksAgree(t *testing.T) {
	tpl, err := tplFS.ReadFile("templates/pdf.html")
	if err != nil {
		t.Fatalf("read pdf.html: %v", err)
	}
	_, rest, ok := strings.Cut(string(tpl), "body { font-family:")
	if !ok {
		t.Fatal("pdf.html has no body font-family rule; this test needs updating alongside it")
	}
	declared, _, ok := strings.Cut(rest, ";")
	if !ok {
		t.Fatal("body font-family declaration is not terminated")
	}

	body, chart := fontFamilies(declared), fontFamilies(pdfFontFamily)
	if strings.Join(body, "|") != strings.Join(chart, "|") {
		t.Errorf("font stacks drifted apart:\n  pdf.html:       %v\n  pdfFontFamily:  %v", body, chart)
	}
	// Naming the symbol faces is what makes precedence deterministic — fontconfig would otherwise
	// pick among the installed fonts by its own sort, and a chart label could take a glyph from a
	// different face than the paragraph beside it. Coverage itself comes from Dockerfile.release;
	// scripts/probe-font-coverage.sh is what fails the build when that regresses.
	//
	// Named rather than counted: the agreement check above passes happily if a face is dropped from
	// both sides at once, which is exactly how this would regress in one careless edit.
	for _, want := range []string{"Noto Sans CJK SC", "Noto Emoji", "DejaVu Sans"} {
		if !slices.Contains(body, want) {
			t.Errorf("font stack %v no longer names %q; fallback precedence is unpinned for its glyphs", body, want)
		}
	}
	// The CJK face has to lead, or DejaVu wins Latin and digits it also covers and the report is set
	// in two faces.
	if len(body) > 0 && body[0] != "Noto Sans CJK SC" {
		t.Errorf("font stack %v does not lead with the body face", body)
	}
}

func TestMermaidCacheAPIRejectsTrailingJSON(t *testing.T) {
	body := `{"source":"flowchart LR\\nA --> B","svg":"<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 10 10\"><path d=\"M0 0L10 10\"/></svg>","theme":"light","version":"11.16.0"}{}`
	req := httptest.NewRequest(http.MethodPost, "/api/mermaid-cache", strings.NewReader(body))
	rec := httptest.NewRecorder()

	(&Server{}).apiMermaidCache(rec, req, "alice")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSanitizePDFBodyDropsAllFetchCapableAttributes(t *testing.T) {
	in := `<style>@import "http://internal/style"</style><a href="http://internal/a">link</a><div style="background:url(http://internal/b)">text</div><object data="http://internal/c"></object>`
	out := sanitizePDFBody(in)
	if strings.Contains(out, "http://") || strings.Contains(out, "style=") || strings.Contains(out, "href=") {
		t.Fatalf("sanitizePDFBody left a network-capable value: %s", out)
	}
	if !strings.Contains(out, "link") || !strings.Contains(out, "text") {
		t.Fatalf("sanitizePDFBody removed readable content: %s", out)
	}
}
