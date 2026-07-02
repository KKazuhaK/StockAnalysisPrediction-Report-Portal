package app

// This file is part of the DISPOSABLE legacy-import path. Once the old Mail
// Research Report System is decommissioned, delete this file, the internal/legacy
// package, the import-legacy CLI wiring, and the old-portal client/sync/read-through.

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
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
	base := st.GetSetting("old_base", "")
	if base == "" {
		return 0, 0, 0, nil, fmt.Errorf("old_base not configured — set the legacy portal URL/credentials under Settings first")
	}
	oc := NewOldClient(base, st.GetSetting("old_user", ""), st.GetSetting("old_pass", ""))
	im := &legacy.Importer{
		Src: legacySource{c: oc}, Sink: legacySink{s: st}, Log: logf,
		MaxConsecutiveFailures: 15, // fail fast if the old system dies; a re-run resumes
	}
	if ms, _ := strconv.Atoi(os.Getenv("RP_IMPORT_DELAY_MS")); ms > 0 {
		im.Delay = time.Duration(ms) * time.Millisecond // optional throttle for a fragile backend
	}
	res, err := im.Run()
	return res.Imported, res.Skipped, res.Failed, res.FailedIDs, err
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
