package app

import (
	"net/http"
	"net/http/httptest"
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
