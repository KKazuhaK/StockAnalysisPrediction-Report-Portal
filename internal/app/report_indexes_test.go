package app

import "testing"

// The report feed reads by code and by date, and both must stay index-backed. The composite
// pair below covers that. The single-column idx_reports_sym(symbol) / idx_reports_date(rdate)
// that once sat beside them are asserted ABSENT, not merely dropped: a B-tree already answers
// its own leftmost prefix, so each was shadowed by a wider index and only ever cost write
// amplification on ingest. Measured on 30k rows against Postgres 18 — the planner never chose
// idx_reports_sym at all (symbol lookups resolve on idx_reports_ident), and removing
// idx_reports_date shifted rdate lookups to idx_reports_date_time for +0.9% cost. Re-adding
// either buys nothing and slows every write, so this test fails if one comes back.
func TestReportFeedIndexes(t *testing.T) {
	st := newTestStore(t)
	rows, err := st.query(`SELECT name FROM sqlite_master WHERE type='index' AND name LIKE 'idx_reports_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		seen[name] = true
	}

	for _, name := range []string{"idx_reports_symbol_date_time", "idx_reports_date_time", "idx_reports_ident"} {
		if !seen[name] {
			t.Errorf("missing index %s — the report feed needs it", name)
		}
	}
	for _, name := range []string{"idx_reports_sym", "idx_reports_date"} {
		if seen[name] {
			t.Errorf("%s is back: it is shadowed by a wider index and only costs write amplification", name)
		}
	}
}
