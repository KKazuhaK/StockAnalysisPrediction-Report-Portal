package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/legacy"
)

// TestLegacyImportE2E runs the full migration against a mock old-system HTTP server:
// it exercises the real OldClient (token auth + paginated list + per-id detail),
// the legacy adapters, and ImportLegacyReport — proving reports land in the unified
// table with body + source="legacy", and that symbol-less ones surface as research.
func TestLegacyImportE2E(t *testing.T) {
	reports := []OldRaw{
		{ID: 101, Title: "宏观周报", Category: "宏观", Author: "张三", Time: "2026-06-01 09:00:00", ReportDate: "2026-06-01", StockCode: ""},
		{ID: 102, Title: "贵州茅台深度", Category: "个股", Author: "李四", Time: "2026-06-02 10:00:00", ReportDate: "2026-06-02", StockCode: "600519"},
		{ID: 103, Title: "AI 泡沫评估", Category: "综合深度研究", Author: "王五", Time: "2026-06-03 11:00:00", ReportDate: "2026-06-03", StockCode: ""},
	}
	body := map[int64][2]string{
		101: {"# 宏观\n正文1", "<h1>宏观</h1>"},
		102: {"# 茅台\n正文2", "<h1>茅台</h1>"},
		103: {"# AI\n正文3", "<h1>AI</h1>"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/token":
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-123"})
		case r.URL.Path == "/api/reports":
			if r.URL.Query().Get("page") == "1" {
				json.NewEncoder(w).Encode(map[string]any{"reports": reports, "total": len(reports)})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"reports": []OldRaw{}, "total": len(reports)})
			}
		case strings.HasPrefix(r.URL.Path, "/api/reports/"):
			var id int64
			fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/api/reports/"), "%d", &id)
			var meta OldRaw
			for _, m := range reports {
				if m.ID == id {
					meta = m
				}
			}
			b := body[id]
			json.NewEncoder(w).Encode(OldDetail{
				ID: id, Title: meta.Title, Category: meta.Category, Author: meta.Author,
				Time: meta.Time, ReportDate: meta.ReportDate, StockCode: meta.StockCode,
				Content: b[0], ContentHTML: b[1],
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	st := newTestStore(t)
	oc := NewOldClient(srv.URL, "u", "p")
	res, err := (&legacy.Importer{Src: legacySource{c: oc}, Sink: legacySink{s: st}}).Run()
	if err != nil {
		t.Fatalf("import run: %v", err)
	}
	if res.Total != 3 || res.Imported != 3 || res.Failed != 0 {
		t.Fatalf("result = %+v, want total3 imported3 failed0", res)
	}
	if n := st.CountNew(); n != 3 {
		t.Fatalf("reports count = %d, want 3", n)
	}

	// A ticker report keeps its body + code + source=legacy.
	var title, src, md, sym string
	st.queryRow("SELECT title,source,body_md,symbol FROM reports WHERE uid=?", "legacy|102").
		Scan(&title, &src, &md, &sym)
	if title != "贵州茅台深度" || src != "legacy" || md != "# 茅台\n正文2" || sym != "600519" {
		t.Errorf("legacy|102 = title%q src%q md%q sym%q", title, src, md, sym)
	}

	// The two symbol-less reports surface as deep-research (reads the reports table now).
	if _, total := st.ResearchReports("", 10, 0); total != 2 {
		t.Errorf("ResearchReports total = %d, want 2 symbol-less", total)
	}

	// And a code report shows up under its symbol.
	syms := st.ListSymbols("600519", 10)
	if len(syms) != 1 || syms[0].Symbol != "600519" || syms[0].Count != 1 {
		t.Errorf("ListSymbols(600519) = %+v, want one symbol with count 1", syms)
	}
}
