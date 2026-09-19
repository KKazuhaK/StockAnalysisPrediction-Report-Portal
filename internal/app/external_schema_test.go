package app

import (
	"testing"
)

// TestExternalUserSchemaBaseline locks the schema for external-user access (ADR 0022). Every
// column, table and index is declared once in baseSchemaStmts, which is now also the acceptance
// contract for an existing database (ADR 0034).
func TestExternalUserSchemaBaseline(t *testing.T) {
	st := newTestStore(t)

	cols := []struct{ table, col string }{
		{"user_groups", "parent_id"},       // OU tree
		{"user_groups", "restricted"},      // internal/external switch
		{"user_groups", "daily_run_quota"}, // R2 per-day run cap
		{"reports", "owner_group"},         // R1 attribution (OU that generated the report)
		{"users", "expires_at"},            // R4 account validity
		{"group_targets", "group_id"},      // R3 allow-list
		{"group_targets", "target_id"},
		{"group_targets", "surfaces"},
	}
	for _, c := range cols {
		if !st.columnExists(c.table, c.col) {
			t.Errorf("missing column %s.%s", c.table, c.col)
		}
	}
	if !st.tableExists("group_targets") {
		t.Error("missing table group_targets")
	}
	// owner_group deliberately has NO index (ADR 0024). It served the ADR 0022 read filter, which
	// version grants and report_viewers replaced; the column survives only as attribution, written
	// by a by-id UPDATE that uses the primary key. Asserted as an absence because an index nothing
	// reads is pure write amplification on every ingest — measured at 13% — and this table has
	// already lost two indexes for exactly that reason.
	var n int
	st.queryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, "idx_reports_owner").Scan(&n)
	if n != 0 {
		t.Error("idx_reports_owner is back: nothing reads owner_group, so it only costs writes")
	}
}
