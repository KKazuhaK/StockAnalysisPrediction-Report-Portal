package app

import (
	"archive/zip"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// This file implements the "export every report a stock has on one date" bundle
// (the ⬇ 导出本日全部 button on the stock detail page). It gathers all reports for
// symbol+date, then streams a ZIP holding, per report, a Markdown source and a
// wkhtmltopdf-rendered PDF. It reuses the same rendering path as the single-report
// /report/{rid}/pdf download (renderPDFHTML → htmlToPDF).

// reBadFilename matches characters that are illegal or awkward in file names across
// OSes (path separators, Windows-reserved, control chars); Unicode (incl. CJK) is kept.
var reBadFilename = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]+`)

// sanitizeFilename makes s safe as a ZIP entry / download name: illegal characters
// become "_", surrounding dots/spaces are trimmed, and the empty result becomes "_".
func sanitizeFilename(s string) string {
	s = reBadFilename.ReplaceAllString(strings.TrimSpace(s), "_")
	s = strings.Trim(s, ". ")
	if s == "" {
		return "_"
	}
	return s
}

// dayEntry is one report in the bundle, with the unique base name (no extension)
// used for both its .md and .pdf files.
type dayEntry struct {
	base string
	rep  Rep
}

// dayExportEntries orders a day's reports the way the on-screen tabs read (top-level
// category order, summary tabs first, then by time) and assigns each a numbered,
// filesystem-safe base name so the ZIP lists them in that same order and every entry
// is unique even when two reports share a label. Pure, so it is unit-tested directly.
func dayExportEntries(reps []Rep) []dayEntry {
	ordered := make([]Rep, len(reps))
	copy(ordered, reps)
	kindRank := func(k string) int {
		for i, x := range kindOrder {
			if x == k {
				return i
			}
		}
		return len(kindOrder)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ki, kj := kindRank(repKind(ordered[i])), kindRank(repKind(ordered[j])); ki != kj {
			return ki < kj
		}
		if si, sj := isSummary(ordered[i]), isSummary(ordered[j]); si != sj {
			return si // summary / decision tabs first, matching the page
		}
		return ordered[i].Time < ordered[j].Time
	})
	out := make([]dayEntry, 0, len(ordered))
	for i, r := range ordered {
		// A running "NN_" prefix keeps the ZIP in reading order and guarantees a unique
		// name, so duplicate labels (e.g. two "重组交易分析") never collide.
		base := fmt.Sprintf("%02d_%s", i+1, sanitizeFilename(label(r)))
		out = append(out, dayEntry{base: base, rep: r})
	}
	return out
}

// reportDayZip streams a ZIP of every report a stock has on one date, each as a .md
// (always) and a .pdf (when wkhtmltopdf is available). GET /report/day.zip?symbol=&date=
// Cookie session auth (same as the single-report MD/PDF downloads).
func (s *Server) reportDayZip(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	symbol := strings.TrimSpace(q.Get("symbol"))
	date := strings.TrimSpace(q.Get("date"))
	if symbol == "" || date == "" {
		http.Error(w, "symbol and date required", http.StatusBadRequest)
		return
	}
	meta, err := s.st.NewBySymbol(symbol)
	if err != nil {
		log.Printf("day-export db error: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	// NewBySymbol returns metadata only; load each matching row's body for rendering.
	var reps []Rep
	for _, m := range meta {
		if m.Date != date {
			continue
		}
		if full := s.loadRep(m.ID); full != nil {
			reps = append(reps, *full)
		}
	}
	if len(reps) == 0 {
		http.Error(w, "该日暂无报告", http.StatusNotFound)
		return
	}

	entries := dayExportEntries(reps)
	pdfOK := wkhtmltopdfBin() != "" // render PDFs only when the tool exists; MD always works
	name := s.names.Get(symbol)
	if name == "" {
		name = symbol
	}
	fn := sanitizeFilename(name+"_"+symbol+"_"+date) + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.QueryEscape(fn))
	zw := zip.NewWriter(w)
	for _, e := range entries {
		if fw, err := zw.Create(e.base + ".md"); err == nil {
			fw.Write([]byte(e.rep.MD))
		}
		if !pdfOK {
			continue
		}
		rep := e.rep
		html, err := s.renderPDFHTML(&rep, user)
		if err != nil {
			log.Printf("day-export render %d: %v", e.rep.ID, err)
			continue
		}
		pdf, err := htmlToPDFContext(r.Context(), html)
		if err != nil {
			log.Printf("day-export pdf %d: %v", e.rep.ID, err)
			continue
		}
		if fw, err := zw.Create(e.base + ".pdf"); err == nil {
			fw.Write(pdf)
		}
	}
	if !pdfOK {
		// Be explicit rather than silently shipping an MD-only ZIP (mirrors the single
		// PDF endpoint's "PDF 暂不可用" behavior; Docker deploys bundle wkhtmltopdf).
		if fw, err := zw.Create("PDF_未生成.txt"); err == nil {
			fw.Write([]byte("本机未安装 wkhtmltopdf，未生成 PDF；已导出全部 Markdown。\nDocker 部署已内置 wkhtmltopdf，线上会包含 PDF。\n"))
		}
	}
	if err := zw.Close(); err != nil {
		log.Printf("day-export zip error: %v", err)
		return
	}
	log.Printf("day-export %s %s -> %d reports (pdf=%v)", symbol, date, len(entries), pdfOK)
}
