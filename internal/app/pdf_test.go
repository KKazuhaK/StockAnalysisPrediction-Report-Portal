package app

import (
	"strings"
	"testing"
)

// PDF export reads rep.HTML directly (see templates/pdf.html), but md-only reports no
// longer persist a stored HTML copy. renderPDFHTML must derive it from MD on the fly so
// export keeps working without wkhtmltopdf even being involved in this test.
func TestRenderPDFHTMLDerivesFromMD(t *testing.T) {
	s := &Server{}
	s.parseTemplates()

	out, err := s.renderPDFHTML(&Rep{Title: "t", MD: "# hi"})
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

	out, err := s.renderPDFHTML(&Rep{Title: "t", HTML: `<p>legacy body</p><img src="http://169.254.169.254/latest/meta-data"><script>alert(1)</script><iframe src="http://127.0.0.1:8790/api/admin/tokens"></iframe>`})
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
