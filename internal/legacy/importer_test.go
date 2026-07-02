package legacy

import (
	"errors"
	"testing"
)

type fakeSource struct {
	list    []OldReport
	content map[int64][2]string // id -> {md, html}
	failIDs map[int64]bool
}

func (f *fakeSource) ListAll() ([]OldReport, error) { return f.list, nil }
func (f *fakeSource) Content(id int64) (string, string, error) {
	if f.failIDs[id] {
		return "", "", errors.New("boom")
	}
	c := f.content[id]
	return c[0], c[1], nil
}

type fakeSink struct {
	got  []ImportedReport
	have map[int64]bool
}

func (s *fakeSink) ImportOne(r ImportedReport) error { s.got = append(s.got, r); return nil }
func (s *fakeSink) Has(id int64) (bool, error)       { return s.have[id], nil }

func TestImporterMigratesAllWithContent(t *testing.T) {
	src := &fakeSource{
		list: []OldReport{
			{ID: 1, Title: "A", Category: "宏观", ReportDate: "2024-01-01", Time: "2024-01-01 09:00:00", StockCode: "600000"},
			{ID: 2, Title: "B", Category: "个股", ReportDate: "2024-02-02", Time: "2024-02-02 10:00:00", StockCode: ""},
		},
		content: map[int64][2]string{1: {"md1", "<p>1</p>"}, 2: {"md2", "<p>2</p>"}},
	}
	sink := &fakeSink{}
	res, err := (&Importer{Src: src, Sink: sink}).Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Total != 2 || res.Imported != 2 || res.Failed != 0 {
		t.Fatalf("result = %+v, want total2 imported2 failed0", res)
	}
	if len(sink.got) != 2 || sink.got[0].BodyMD != "md1" || sink.got[0].Title != "A" || sink.got[0].StockCode != "600000" {
		t.Fatalf("first imported = %+v", sink.got[0])
	}
	if sink.got[1].BodyHTML != "<p>2</p>" {
		t.Errorf("second html = %q, want <p>2</p>", sink.got[1].BodyHTML)
	}
}

// A single report that fails to fetch must not abort the whole run — it is recorded
// and skipped so a multi-thousand backfill survives a few bad rows.
func TestImporterSkipsFailedFetch(t *testing.T) {
	src := &fakeSource{
		list:    []OldReport{{ID: 1}, {ID: 2}, {ID: 3}},
		content: map[int64][2]string{1: {"a", ""}, 3: {"c", ""}},
		failIDs: map[int64]bool{2: true},
	}
	sink := &fakeSink{}
	res, _ := (&Importer{Src: src, Sink: sink}).Run()
	if res.Imported != 2 || res.Failed != 1 || len(res.FailedIDs) != 1 || res.FailedIDs[0] != 2 {
		t.Fatalf("result = %+v, want imported2 failed1 failedIDs[2]", res)
	}
}

// Resume: reports already present in the sink are skipped without re-fetching their body.
func TestImporterResumeSkipsExisting(t *testing.T) {
	src := &fakeSource{
		list:    []OldReport{{ID: 1}, {ID: 2}, {ID: 3}},
		content: map[int64][2]string{1: {"a", ""}, 2: {"b", ""}, 3: {"c", ""}},
	}
	sink := &fakeSink{have: map[int64]bool{2: true}} // id 2 already imported
	res, _ := (&Importer{Src: src, Sink: sink}).Run()
	if res.Imported != 2 || res.Skipped != 1 {
		t.Fatalf("result = %+v, want imported2 skipped1", res)
	}
	for _, g := range sink.got {
		if g.OldID == 2 {
			t.Errorf("id=2 was re-imported despite already existing (resume should skip it)")
		}
	}
}

// Circuit breaker: after N consecutive failures the run aborts (old system likely
// down) instead of churning through the rest, so a re-run can resume.
func TestImporterCircuitBreaker(t *testing.T) {
	src := &fakeSource{
		list:    []OldReport{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5}},
		failIDs: map[int64]bool{1: true, 2: true, 3: true, 4: true, 5: true},
	}
	res, err := (&Importer{Src: src, Sink: &fakeSink{}, MaxConsecutiveFailures: 3}).Run()
	if !res.Aborted || err == nil {
		t.Fatalf("expected abort with error; res=%+v err=%v", res, err)
	}
	if res.Failed != 3 {
		t.Errorf("failed=%d, want 3 (stopped at the breaker, not all 5)", res.Failed)
	}
}
