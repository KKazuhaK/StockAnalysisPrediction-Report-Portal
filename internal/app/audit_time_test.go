package app

import (
	"strings"
	"testing"
	"time"
)

// audit_log.at is a UTC instant, in the RFC3339 form the rest of the portal already uses for
// instants (reports' sent_at, /api/v1/now). It used to be the host's local wall clock, which meant
// correlating an audit row with a Dify run — the sandbox runs UTC — required knowing the server's
// timezone, and nothing recorded what that was.
//
// The two formats are distinguishable on sight and to a comparison: an RFC3339 stamp carries T and
// Z, a legacy one carries a space. That is what lets rows written by v0.4.9–v0.4.14 stay readable
// instead of being silently reinterpreted as UTC and shifted by the host's offset.

func TestAuditStampsAreUTCInstants(t *testing.T) {
	s := auditServer(t)
	s.recordAuth(nil, AuditLogin, "kazuha", "kazuha", nil)

	rows, _ := s.st.ListAudit(AuditFilter{Action: AuditLogin})
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	at, err := time.Parse(time.RFC3339, rows[0].At)
	if err != nil {
		t.Fatalf("at = %q, which is not an RFC3339 instant: %v", rows[0].At, err)
	}
	if _, off := at.Zone(); off != 0 {
		t.Errorf("at = %q carries a %d-second offset; instants are UTC on the wire", rows[0].At, off)
	}
	if d := time.Since(at); d < 0 || d > time.Minute {
		t.Errorf("at = %q is %v away from now — the stamp is not the moment it recorded", rows[0].At, d)
	}
}

// Retention compares lexically, which is only chronological if the format is consistent. A legacy
// row must still be deletable, and must not be deleted EARLY because a space sorts below a T.
func TestRetentionHandlesBothStampFormats(t *testing.T) {
	s := auditServer(t)
	old := time.Now().Add(-100 * 24 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)

	// One row in each format, on each side of the cutoff.
	s.st.WriteAudit(AuditEntry{Action: "legacy.old", At: old.Format("2006-01-02 15:04:05")})
	s.st.WriteAudit(AuditEntry{Action: "legacy.recent", At: recent.Format("2006-01-02 15:04:05")})
	s.st.WriteAudit(AuditEntry{Action: "utc.old", At: old.UTC().Format(time.RFC3339)})
	s.st.WriteAudit(AuditEntry{Action: "utc.recent", At: recent.UTC().Format(time.RFC3339)})

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	if n, err := s.st.CountAuditBefore(cutoff); err != nil || n != 2 {
		t.Fatalf("CountAuditBefore = %d,%v; want the two old rows in either format", n, err)
	}
	if _, err := s.st.DeleteAuditBefore(cutoff); err != nil {
		t.Fatalf("delete: %v", err)
	}
	left := map[string]bool{}
	rows, _ := s.st.ListAudit(AuditFilter{})
	for _, r := range rows {
		left[r.Action] = true
	}
	if left["legacy.old"] || left["utc.old"] {
		t.Error("an old row survived the cutoff")
	}
	if !left["legacy.recent"] || !left["utc.recent"] {
		t.Errorf("a recent row was deleted early: %v", left)
	}
}

// Newest-first has to mean newest-first across the format change, or the console's first page shows
// the wrong rows for as long as both formats are present.
func TestListingOrdersAcrossTheFormatChange(t *testing.T) {
	s := auditServer(t)
	base := time.Now().Add(-2 * time.Hour)
	s.st.WriteAudit(AuditEntry{Action: "older.legacy", At: base.Format("2006-01-02 15:04:05")})
	s.st.WriteAudit(AuditEntry{Action: "newer.utc", At: base.Add(time.Hour).UTC().Format(time.RFC3339)})

	rows, _ := s.st.ListAudit(AuditFilter{})
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Action != "newer.utc" {
		t.Errorf("first row is %q; newest-first broke across the format change (%q then %q)",
			rows[0].Action, rows[0].At, rows[1].At)
	}
}

// The Since filter is a civil date from the console. It has to select the same day in both formats.
func TestSinceFilterSpansBothFormats(t *testing.T) {
	s := auditServer(t)
	today := time.Now().UTC()
	s.st.WriteAudit(AuditEntry{Action: "a.legacy", At: today.Format("2006-01-02 15:04:05")})
	s.st.WriteAudit(AuditEntry{Action: "b.utc", At: today.Format(time.RFC3339)})
	s.st.WriteAudit(AuditEntry{Action: "c.ancient", At: today.AddDate(0, 0, -9).Format(time.RFC3339)})

	_, total := s.st.ListAudit(AuditFilter{Since: today.Format("2006-01-02")})
	if total != 2 {
		t.Errorf("Since today matched %d rows, want the two written today in either format", total)
	}
	if strings.Contains(today.Format(time.RFC3339), " ") {
		t.Fatal("fixture assumption broken")
	}
}
