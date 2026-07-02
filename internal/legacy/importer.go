// Package legacy is a one-shot, DISPOSABLE importer that pulls the full history
// (metadata + body) out of the old "Mail Research Report System" and folds it into
// the portal's main reports store, so the old system can be decommissioned.
//
// Everything old-system-specific is confined here plus a couple of thin adapters.
// Once the old system is retired, delete: this package, internal/app/legacy_import.go,
// the import-legacy CLI wiring, and the old-portal client/sync/read-through.
package legacy

import (
	"fmt"
	"time"
)

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
	// Has reports whether the report with this old id was already imported, so a
	// re-run can resume without re-fetching its (potentially large) body.
	Has(oldID int64) (bool, error)
}

// Result summarizes an import run.
type Result struct {
	Total, Imported, Skipped, Failed int
	FailedIDs                        []int64
	Aborted                          bool // true if the circuit breaker tripped (old system likely down)
}

// Importer copies every old report (with body) from Src into Sink. It is resilient:
// a single report that fails to fetch or store is recorded and skipped rather than
// aborting the whole run, so a multi-thousand backfill isn't lost to one bad row.
// It is also RESUMABLE (already-imported reports are skipped without re-fetching)
// and fails fast when the old system dies (MaxConsecutiveFailures), so a re-run
// picks up where it left off instead of churning through timeouts.
type Importer struct {
	Src                    Source
	Sink                   Sink
	Log                    func(format string, args ...any) // optional progress logger
	Delay                  time.Duration                    // optional pause between successful fetches (throttle a fragile backend)
	MaxConsecutiveFailures int                              // >0: abort after this many failures in a row (0 = never)
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
	consecFail := 0
	fail := func(i int, id int64, what string, err error) bool {
		res.Failed++
		res.FailedIDs = append(res.FailedIDs, id)
		consecFail++
		im.logf("  [%d/%d] id=%d %s failed: %v", i+1, res.Total, id, what, err)
		if im.MaxConsecutiveFailures > 0 && consecFail >= im.MaxConsecutiveFailures {
			res.Aborted = true
			return true
		}
		return false
	}
	for i, r := range list {
		// Resume: skip already-imported reports without re-fetching their body.
		if has, herr := im.Sink.Has(r.ID); herr == nil && has {
			res.Skipped++
			continue
		}
		md, html, err := im.Src.Content(r.ID)
		if err != nil {
			if fail(i, r.ID, "fetch", err) {
				break
			}
			continue
		}
		if err := im.Sink.ImportOne(ImportedReport{
			OldID: r.ID, Title: r.Title, StockCode: r.StockCode, Category: r.Category,
			ReportDate: r.ReportDate, Time: r.Time, BodyMD: md, BodyHTML: html,
		}); err != nil {
			if fail(i, r.ID, "store", err) {
				break
			}
			continue
		}
		consecFail = 0
		res.Imported++
		if res.Imported%100 == 0 {
			im.logf("  imported %d/%d (skipped %d)", res.Imported, res.Total, res.Skipped)
		}
		if im.Delay > 0 {
			time.Sleep(im.Delay)
		}
	}
	if res.Aborted {
		im.logf("legacy import ABORTED after %d consecutive failures (old system likely down) — imported=%d skipped=%d; re-run to resume", consecFail, res.Imported, res.Skipped)
		return res, fmt.Errorf("aborted after %d consecutive failures (old system likely down); re-run to resume", consecFail)
	}
	im.logf("legacy import done: imported=%d skipped=%d failed=%d", res.Imported, res.Skipped, res.Failed)
	return res, nil
}
