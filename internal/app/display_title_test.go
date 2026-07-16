package app

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// displayTitle folds the (as-of) company name into the stored report title so the
// on-screen heading, the PDF <h1>, and the MD/PDF download filenames all read
// "001696 宗申动力 投资决策建议" instead of the bare "001696 投资决策建议" that ingest stores.
func TestDisplayTitle(t *testing.T) {
	cases := []struct {
		desc             string
		title, sym, comp string
		want             string
	}{
		{"folds name after the leading symbol", "001696 投资决策建议", "001696", "宗申动力", "001696 宗申动力 投资决策建议"},
		{"normalizes a missing space after the symbol", "001696投资决策建议", "001696", "宗申动力", "001696 宗申动力 投资决策建议"},
		{"idempotent when the name is already present", "001696 宗申动力 投资决策建议", "001696", "宗申动力", "001696 宗申动力 投资决策建议"},
		{"no company name leaves the title untouched", "新能源行业深度", "", "", "新能源行业深度"},
		{"symbol-only title just appends the name", "001696", "001696", "宗申动力", "001696 宗申动力"},
		{"title without a leading symbol only prefixes the name", "投资决策建议", "001696", "宗申动力", "宗申动力 投资决策建议"},
		{"collapses surrounding and inner whitespace", "  001696   投资决策建议 ", "001696", "宗申动力", "001696 宗申动力 投资决策建议"},
		{"empty title with a name becomes the name", "", "001696", "宗申动力", "宗申动力"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := displayTitle(c.title, c.sym, c.comp); got != c.want {
				t.Errorf("displayTitle(%q, %q, %q) = %q, want %q", c.title, c.sym, c.comp, got, c.want)
			}
		})
	}
}

// repDisplayTitle resolves the report's as-of company name (snapshot, falling back to
// the current name) before folding it in. It must be nil-safe on s.names, which unit
// tests leave unset.
func TestRepDisplayTitleNilSafeAndSnapshot(t *testing.T) {
	s := &Server{} // s.names == nil, as in a bare unit test

	if got := s.repDisplayTitle(&Rep{Title: "001696 投资决策建议", Symbol: "001696", Name: "宗申动力"}); got != "001696 宗申动力 投资决策建议" {
		t.Errorf("snapshot name: got %q", got)
	}
	// Pre-snapshot report (empty Name) with a nil resolver → no name to fold, title unchanged.
	if got := s.repDisplayTitle(&Rep{Title: "001696 投资决策建议", Symbol: "001696"}); got != "001696 投资决策建议" {
		t.Errorf("no name fallback: got %q", got)
	}
}

// The PDF <h1> is rendered from rep.Title via the template, so the company name must be
// folded in before the template runs.
func TestRenderPDFHTMLFoldsCompanyNameIntoHeading(t *testing.T) {
	s := &Server{}
	s.parseTemplates()

	out, err := s.renderPDFHTML(&Rep{Title: "001696 投资决策建议", Symbol: "001696", Name: "宗申动力", MD: "# body"})
	if err != nil {
		t.Fatalf("renderPDFHTML: %v", err)
	}
	if !strings.Contains(out, "<h1>001696 宗申动力 投资决策建议</h1>") {
		t.Errorf("rendered PDF html = %q, want the company name folded into the <h1>", out)
	}
}

// decodeFilenameStar pulls the UTF-8 filename back out of a Content-Disposition header.
func decodeFilenameStar(t *testing.T, cd string) string {
	t.Helper()
	const marker = "filename*=UTF-8''"
	i := strings.Index(cd, marker)
	if i < 0 {
		t.Fatalf("no filename* in Content-Disposition %q", cd)
	}
	name, err := url.QueryUnescape(cd[i+len(marker):])
	if err != nil {
		t.Fatalf("unescape %q: %v", cd, err)
	}
	return name
}

// End-to-end: a report ingested the way the Dify pipeline sends it (bare "symbol
// descriptor" title + as-of company name) must download as "001696 宗申动力 投资决策建议.md",
// with the company name folded into the actual Content-Disposition filename.
func TestReportMDDownloadFilenameFoldsCompanyName(t *testing.T) {
	s := newIngestTestServer(t)

	rec, _ := postIngest(t, s, map[string]any{
		"symbol": "001696", "name": "宗申动力", "date": "2026-07-16",
		"kind": "投资决策", "subtype": "投资决策建议",
		"title": "001696 投资决策建议", "body_md": "# body",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status=%d body=%s", rec.Code, rec.Body.String())
	}

	reps, err := s.st.NewBySymbol("001696")
	if err != nil || len(reps) == 0 {
		t.Fatalf("NewBySymbol: err=%v n=%d", err, len(reps))
	}
	ridStr := reps[0].RID

	req := httptest.NewRequest("GET", "/report/"+ridStr+"/md", nil)
	req.SetPathValue("rid", ridStr)
	w := httptest.NewRecorder()
	s.reportMD(w, req, "tester")

	if w.Code != http.StatusOK {
		t.Fatalf("reportMD status=%d body=%s", w.Code, w.Body.String())
	}
	if got := decodeFilenameStar(t, w.Header().Get("Content-Disposition")); got != "001696 宗申动力 投资决策建议.md" {
		t.Errorf("MD download filename = %q, want %q", got, "001696 宗申动力 投资决策建议.md")
	}
}

// repJSON must expose displayTitle (folded) for the SPA heading while leaving the raw
// title field untouched for machine consumers.
func TestRepJSONExposesDisplayTitle(t *testing.T) {
	s := newIngestTestServer(t)
	j := repJSON(&Rep{Title: "001696 投资决策建议", Symbol: "001696", Name: "宗申动力"}, s.names.Get)
	if j["displayTitle"] != "001696 宗申动力 投资决策建议" {
		t.Errorf("displayTitle = %v, want folded", j["displayTitle"])
	}
	if j["title"] != "001696 投资决策建议" {
		t.Errorf("raw title = %v, want it left untouched", j["title"])
	}
}
