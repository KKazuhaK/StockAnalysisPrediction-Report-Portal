package app

import "testing"

func TestReportFeedIndexesExist(t *testing.T) {
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
	for _, name := range []string{"idx_reports_date", "idx_reports_sym", "idx_reports_symbol_date_time", "idx_reports_date_time"} {
		if !seen[name] {
			t.Errorf("missing index %s", name)
		}
	}
}
