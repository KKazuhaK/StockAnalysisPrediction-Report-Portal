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

func TestRenderPDFHTMLUsesStoredHTMLForLegacyReports(t *testing.T) {
	s := &Server{}
	s.parseTemplates()

	out, err := s.renderPDFHTML(&Rep{Title: "t", HTML: "<p>legacy body</p>"})
	if err != nil {
		t.Fatalf("renderPDFHTML: %v", err)
	}
	if !strings.Contains(out, "<p>legacy body</p>") {
		t.Errorf("rendered PDF html = %q, want the stored legacy HTML untouched", out)
	}
}
