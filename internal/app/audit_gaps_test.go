package app

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A read is the one action in the log that can be about someone reading something they should not
// have, and it was the one action recorded without an address: recordReportRead never took the
// request. Every other writer goes through recordChange/recordAuth, which stamp it.
func TestAReportReadRecordsWhereItCameFrom(t *testing.T) {
	s := tenancyServer(t)
	id, _, _ := s.st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-31", RType: "投资决策", Title: "内部", MD: "x"})
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})

	req := httptest.NewRequest("GET", "/report/1/md", nil)
	req.RemoteAddr = "203.0.113.9:41000"
	if rep := s.loadRep(req, "alice", id); rep == nil {
		t.Fatal("the fixture report should be readable")
	}

	rows, total := s.st.ListAudit(AuditFilter{Action: AuditReportRead})
	if total != 1 {
		t.Fatalf("a report read logged %d lines, want 1", total)
	}
	if rows[0].IP != "203.0.113.9" {
		t.Errorf("IP = %q, want the caller's address — a read with no address cannot be traced", rows[0].IP)
	}
}

// The console renders an action through t(`audit.a.${action}`) and falls back to the raw string,
// so a vocabulary entry with no label does not break the page — it just shows "auth.step_up" in a
// dropdown of Chinese phrases, and nothing fails until somebody looks. The vocabulary is the
// source of truth, so the check belongs on this side of the boundary.
func TestEveryAuditActionHasALabelInEveryLanguage(t *testing.T) {
	dir := filepath.Join("..", "..", "web", "src", "locales")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("locale bundles not present: %v", err)
	}
	var checked int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var labels map[string]string
		if err := json.Unmarshal(raw, &labels); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		checked++
		for _, action := range auditVocabulary {
			key := "audit.a." + action
			if strings.TrimSpace(labels[key]) == "" {
				t.Errorf("%s has no %q — the filter would offer the raw action string", e.Name(), key)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no locale bundles were checked")
	}
}

// "Somebody started a run" is not worth much on its own. The line has to say which workflow, with
// what inputs, and under which options — otherwise the log records that a decision happened and
// none of what was decided.
func TestARunSubmissionRecordsWhatWasSubmitted(t *testing.T) {
	detail := runSubmitDetail(runSubmitAudit{
		TargetID:   5,
		TargetName: "研报分析",
		Surface:    "run",
		Rows:       []map[string]string{{"symbol": "603587", "query": strings.Repeat("很长的提示词", 200)}},
		Priority:   "30",
		Retries:    1,
		Notify:     true,
		RunAt:      "",
		Preset:     "",
	})

	for _, want := range []string{"研报分析", "603587", `"rows":1`, `"retries":1`, `"notify":true`, `"surface":"run"`} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail is missing %s: %s", want, detail)
		}
	}
	// An agent run carries a whole prompt. The log records what was asked for, not a transcript:
	// unbounded, one submission could outweigh a month of ordinary lines.
	if len(detail) > 1200 {
		t.Errorf("detail is %d bytes; a single run must not be able to bloat the table", len(detail))
	}
	if !strings.Contains(detail, "…") {
		t.Errorf("a clamped value should say so: %s", detail)
	}
}
