package app

// This file is part of the DISPOSABLE legacy-import path. Once the old Mail
// Research Report System is decommissioned, delete this file, the internal/legacy
// package, the import-legacy CLI wiring, and the old-portal client/sync/read-through.

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/legacy"
)

// RunLegacyImport runs the one-shot legacy import using the old-portal credentials
// stored in Settings (old_base/old_user/old_pass). It is the disposable entry point
// behind the `import-legacy` CLI subcommand. Tip: set sync_min=0 (or clear old_base)
// afterwards so nothing tries to re-read the retired system.
func RunLegacyImport(cfgPath string, logf func(string, ...any)) (imported, skipped, failed int, failedIDs []int64, err error) {
	cfg, err := config.EnsureConfig(cfgPath)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	st, err := OpenStore(cfg.DBDriver, cfg.DBSource())
	if err != nil {
		return 0, 0, 0, nil, err
	}
	im := st.legacyImporter(logf)
	if im == nil {
		return 0, 0, 0, nil, fmt.Errorf("old_base not configured — set the legacy portal URL/credentials under Settings first")
	}
	res, err := im.Run()
	return res.Imported, res.Skipped, res.Failed, res.FailedIDs, err
}

// legacyImporter builds a resumable importer from the store's saved legacy
// credentials, or returns nil if old_base is not configured. Shared by the CLI
// (RunLegacyImport) and the admin "run import" button.
func (s *Store) legacyImporter(logf func(string, ...any)) *legacy.Importer {
	base := s.GetSetting("old_base", "")
	if base == "" {
		return nil
	}
	oc := NewOldClient(base, s.GetSetting("old_user", ""), s.GetSetting("old_pass", ""))
	im := &legacy.Importer{
		Src: legacySource{c: oc}, Sink: legacySink{s: s}, Log: logf,
		MaxConsecutiveFailures: 15, // fail fast if the old system dies; a re-run resumes
	}
	if ms, _ := strconv.Atoi(os.Getenv("RP_IMPORT_DELAY_MS")); ms > 0 {
		im.Delay = time.Duration(ms) * time.Millisecond // optional throttle for a fragile backend
	}
	return im
}

// CountLegacy is the number of imported legacy reports (live progress for the UI).
func (s *Store) CountLegacy() (n int) {
	s.queryRow("SELECT COUNT(*) FROM reports WHERE source=?", "legacy").Scan(&n)
	return
}

// legacyImportJob tracks a single background import triggered from the admin UI
// (only one runs at a time). Package-level because there is one server instance.
type legacyImportJob struct {
	mu       sync.Mutex
	running  bool
	started  string
	finished string
	res      legacy.Result
	errMsg   string
}

var legacyJob legacyImportJob

func (j *legacyImportJob) tryStart() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return false
	}
	j.running, j.started, j.finished, j.res, j.errMsg = true, nowStr(), "", legacy.Result{}, ""
	return true
}

func (j *legacyImportJob) done(res legacy.Result, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running, j.finished, j.res = false, nowStr(), res
	if err != nil {
		j.errMsg = err.Error()
	}
}

func (j *legacyImportJob) snapshot() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	return map[string]any{
		"running": j.running, "started": j.started, "finished": j.finished,
		"imported": j.res.Imported, "skipped": j.res.Skipped, "failed": j.res.Failed,
		"aborted": j.res.Aborted, "error": j.errMsg,
	}
}

// apiLegacyImportStart triggers a resumable import in the background (fills gaps /
// transition-phase sync). Idempotent while running.
func (s *Server) apiLegacyImportStart(w http.ResponseWriter, r *http.Request, user string) {
	im := s.st.legacyImporter(func(f string, a ...any) { log.Printf(f, a...) })
	if im == nil {
		jsonError(w, http.StatusBadRequest, "旧门户未配置：请先填写旧门户地址/账号并保存")
		return
	}
	if !legacyJob.tryStart() {
		writeJSON(w, map[string]any{"ok": true, "alreadyRunning": true})
		return
	}
	go func() { legacyJob.done(im.Run()) }()
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

// apiLegacyImportStatus returns the background import's progress + live imported count.
func (s *Server) apiLegacyImportStatus(w http.ResponseWriter, r *http.Request, user string) {
	snap := legacyJob.snapshot()
	snap["count"] = s.st.CountLegacy()
	writeJSON(w, snap)
}

// ImportLegacyReport folds one legacy report (metadata + body) into the unified
// reports table as a first-class report (source="legacy", uid "legacy|<id>").
// Idempotent: a re-run updates the existing report in place.
func (s *Store) ImportLegacyReport(oldID int64, title, stockCode, category, reportDate, tm, bodyMD, bodyHTML string) error {
	uid := fmt.Sprintf("legacy|%d", oldID)
	kind := s.TypeKind(category)
	if kind == "" {
		kind = runKind([]string{category})
	}
	_, err := s.exec(`
		INSERT INTO reports(uid,title,symbol,name,rtype,rdate,kind,run_id,source,sent_at,body_md,body_html)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(uid) DO UPDATE SET title=excluded.title,symbol=excluded.symbol,name=excluded.name,
		  rtype=excluded.rtype,rdate=excluded.rdate,kind=excluded.kind,run_id=excluded.run_id,
		  source=excluded.source,sent_at=excluded.sent_at,body_md=excluded.body_md,body_html=excluded.body_html`,
		uid, title, stockCode, "", category, reportDate, kind, "", "legacy", tm, bodyMD, bodyHTML)
	return err
}

// HasLegacyReport reports whether the legacy report with this old id has already
// been imported (used by the resumable importer to skip re-fetching it).
func (s *Store) HasLegacyReport(oldID int64) (bool, error) {
	var x int
	err := s.queryRow("SELECT 1 FROM reports WHERE uid=?", fmt.Sprintf("legacy|%d", oldID)).Scan(&x)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// legacySource adapts *OldClient to legacy.Source.
type legacySource struct{ c *OldClient }

func (a legacySource) ListAll() ([]legacy.OldReport, error) {
	raws, err := a.c.ListAllMeta()
	if err != nil {
		return nil, err
	}
	out := make([]legacy.OldReport, len(raws))
	for i, r := range raws {
		out[i] = legacy.OldReport{
			ID: r.ID, Title: r.Title, Category: r.Category, Author: r.Author,
			Time: r.Time, ReportDate: r.ReportDate, StockCode: r.StockCode,
		}
	}
	return out, nil
}

func (a legacySource) Content(id int64) (string, string, error) {
	d, err := a.c.Detail(id)
	if err != nil {
		return "", "", err
	}
	return d.Content, d.ContentHTML, nil
}

// legacySink adapts *Store to legacy.Sink.
type legacySink struct{ s *Store }

func (a legacySink) ImportOne(r legacy.ImportedReport) error {
	return a.s.ImportLegacyReport(r.OldID, r.Title, r.StockCode, r.Category, r.ReportDate, r.Time, r.BodyMD, r.BodyHTML)
}

func (a legacySink) Has(oldID int64) (bool, error) { return a.s.HasLegacyReport(oldID) }
