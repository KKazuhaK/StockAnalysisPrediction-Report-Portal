// Package legacy is a one-shot, DISPOSABLE importer that pulls the full history
// (metadata + body) out of the old "Mail Research Report System" and folds it into
// the portal's main reports store, so the old system can be decommissioned.
//
// Everything old-system-specific is confined here plus a couple of thin adapters.
// Once the old system is retired, delete: this package, internal/app/legacy_import.go,
// the import-legacy CLI wiring, and the old-portal client/sync/read-through.
package legacy

import "fmt"

// OldReport is one legacy report's metadata (as listed by the old system).
type OldReport struct {
	ID                                                   int64
	Title, Category, Author, Time, ReportDate, StockCode string
}

// Source is the read view of the old system the importer needs; implemented by an
// adapter over the old-portal client.
type Source interface {
	ListAll() ([]OldReport, error)                        // full metadata history
	Content(id int64) (md string, html string, err error) // one report's body
}

// ImportedReport is a fully-resolved legacy report handed to the Sink.
type ImportedReport struct {
	OldID                                        int64
	Title, StockCode, Category, ReportDate, Time string
	BodyMD, BodyHTML                             string
}

// Sink persists an imported report into the main store; implemented by an adapter
// over the store. Must be idempotent on the report's identity.
type Sink interface {
	ImportOne(r ImportedReport) error
}

// Result summarizes an import run.
type Result struct {
	Total, Imported, Failed int
	FailedIDs               []int64
}

// Importer copies every old report (with body) from Src into Sink. It is resilient:
// a single report that fails to fetch or store is recorded and skipped rather than
// aborting the whole run, so a multi-thousand backfill isn't lost to one bad row.
type Importer struct {
	Src  Source
	Sink Sink
	Log  func(format string, args ...any) // optional progress logger
}

func (im *Importer) logf(format string, args ...any) {
	if im.Log != nil {
		im.Log(format, args...)
	}
}

// Run performs the full import and returns a summary. The error is non-nil only on
// a fatal failure (e.g. cannot list); per-report failures surface via Result.
func (im *Importer) Run() (Result, error) {
	list, err := im.Src.ListAll()
	if err != nil {
		return Result{}, fmt.Errorf("list legacy reports: %w", err)
	}
	res := Result{Total: len(list)}
	im.logf("legacy import: %d reports to migrate", len(list))
	for i, r := range list {
		md, html, err := im.Src.Content(r.ID)
		if err != nil {
			res.Failed++
			res.FailedIDs = append(res.FailedIDs, r.ID)
			im.logf("  [%d/%d] id=%d fetch failed: %v", i+1, res.Total, r.ID, err)
			continue
		}
		if err := im.Sink.ImportOne(ImportedReport{
			OldID: r.ID, Title: r.Title, StockCode: r.StockCode, Category: r.Category,
			ReportDate: r.ReportDate, Time: r.Time, BodyMD: md, BodyHTML: html,
		}); err != nil {
			res.Failed++
			res.FailedIDs = append(res.FailedIDs, r.ID)
			im.logf("  [%d/%d] id=%d store failed: %v", i+1, res.Total, r.ID, err)
			continue
		}
		res.Imported++
		if res.Imported%100 == 0 {
			im.logf("  imported %d/%d", res.Imported, res.Total)
		}
	}
	im.logf("legacy import done: imported=%d failed=%d", res.Imported, res.Failed)
	return res, nil
}
