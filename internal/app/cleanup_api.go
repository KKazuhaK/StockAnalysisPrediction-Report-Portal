package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// Storage-cleanup admin API (docs/adr/0017-storage-cleanup.md). All handlers are admin-only
// (requireAdminJSON = PermManage). Config + last-run summary live in meta; preview counts what a
// run WOULD remove for the selected targets (regardless of their scheduled-enable toggle, so the
// reports danger-zone confirm can show a live count before the target is enabled).

// ---------- config ----------

func (s *Server) apiCleanupConfigGet(w http.ResponseWriter, r *http.Request, user string) {
	c := s.cleanupConfigLoad()
	var last *cleanupResult
	if raw := s.st.GetSetting("cleanup_last_result", ""); raw != "" {
		var lr cleanupResult
		if json.Unmarshal([]byte(raw), &lr) == nil {
			last = &lr
		}
	}
	writeJSON(w, map[string]any{
		"freq":              c.Freq,
		"time":              c.Time,
		"weekday":           c.Weekday,
		"monthday":          c.Monthday,
		"batch_enabled":     c.BatchEnabled,
		"batch_days":        c.BatchDays,
		"tokens_enabled":    c.TokensEnabled,
		"tokens_grace_days": c.TokensGraceDays,
		"reports_enabled":   c.ReportsEnabled,
		"reports_days":      c.ReportsDays,
		"batch_floor":       minBatchRetentionDays,
		"reports_floor":     minReportsRetentionDays,
		"last_run_period":   s.st.GetSetting("cleanup_last_run_period", ""),
		"last_result":       last,
	})
}

func (s *Server) apiCleanupConfigSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Freq            *string `json:"freq"`
		Time            *string `json:"time"`
		Weekday         *int    `json:"weekday"`
		Monthday        *int    `json:"monthday"`
		BatchEnabled    *bool   `json:"batch_enabled"`
		BatchDays       *int    `json:"batch_days"`
		TokensEnabled   *bool   `json:"tokens_enabled"`
		TokensGraceDays *int    `json:"tokens_grace_days"`
		ReportsEnabled  *bool   `json:"reports_enabled"`
		ReportsDays     *int    `json:"reports_days"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	// Validate everything BEFORE persisting anything, so a rejected field leaves meta unchanged.
	if in.Freq != nil {
		switch *in.Freq {
		case "off", "daily", "weekly", "monthly":
		default:
			jsonError(w, http.StatusBadRequest, "bad freq")
			return
		}
	}
	if in.Time != nil {
		if _, _, ok := parseHHMM(*in.Time); !ok {
			jsonError(w, http.StatusBadRequest, "bad time")
			return
		}
	}
	if in.BatchDays != nil && *in.BatchDays < minBatchRetentionDays {
		jsonError(w, http.StatusBadRequest, "batch_days below floor")
		return
	}
	if in.ReportsDays != nil && *in.ReportsDays < minReportsRetentionDays {
		jsonError(w, http.StatusBadRequest, "reports_days below floor")
		return
	}

	if in.Freq != nil {
		s.st.SetSetting("cleanup_schedule_freq", *in.Freq)
	}
	if in.Time != nil {
		s.st.SetSetting("cleanup_schedule_time", *in.Time)
	}
	if in.Weekday != nil && *in.Weekday >= 0 && *in.Weekday <= 6 {
		s.st.SetSetting("cleanup_schedule_weekday", strconv.Itoa(*in.Weekday))
	}
	if in.Monthday != nil && *in.Monthday >= 1 && *in.Monthday <= 31 {
		s.st.SetSetting("cleanup_schedule_monthday", strconv.Itoa(*in.Monthday))
	}
	if in.BatchDays != nil {
		s.st.SetSetting("cleanup_batch_days", strconv.Itoa(*in.BatchDays))
	}
	if in.TokensGraceDays != nil && *in.TokensGraceDays >= 0 {
		s.st.SetSetting("cleanup_tokens_grace_days", strconv.Itoa(*in.TokensGraceDays))
	}
	if in.ReportsDays != nil {
		s.st.SetSetting("cleanup_reports_days", strconv.Itoa(*in.ReportsDays))
	}
	if in.BatchEnabled != nil {
		s.st.SetSetting("cleanup_batch_enabled", strconv.Itoa(boolInt(*in.BatchEnabled)))
	}
	if in.TokensEnabled != nil {
		s.st.SetSetting("cleanup_tokens_enabled", strconv.Itoa(boolInt(*in.TokensEnabled)))
	}
	if in.ReportsEnabled != nil {
		s.st.SetSetting("cleanup_reports_enabled", strconv.Itoa(boolInt(*in.ReportsEnabled)))
	}
	writeJSON(w, okJSON)
}

// ---------- usage (storage analysis) ----------

func (s *Server) apiCleanupUsage(w http.ResponseWriter, r *http.Request, user string) {
	c := s.cleanupConfigLoad()
	batchCut, tokenCut, reportsCut := c.cutoffs(time.Now())

	batchEligible, _ := s.st.CountFinishedJobsBefore(batchCut)
	tokEligible, _ := s.st.CountExpiredTokensBefore(tokenCut)
	repEligible, _ := s.st.CountReportsIngestedBefore(reportsCut)

	batchOld, batchNew := s.st.usageSpan("batch_jobs", "finished_at")
	tokOld, tokNew := s.st.usageSpan("api_tokens", "created_at")
	repOld, repNew := s.st.usageSpan("reports", "sent_at")

	cat := func(key string, rows, bytes, eligible int64, oldest, newest string) map[string]any {
		return map[string]any{"key": key, "rows": rows, "bytes": bytes, "eligible": eligible, "oldest": oldest, "newest": newest}
	}
	writeJSON(w, map[string]any{
		"db_bytes": s.st.DBSizeBytes(),
		"categories": []map[string]any{
			cat("batch",
				s.st.usageCount("batch_jobs"),
				s.st.usageBytes("batch_items", "LENGTH(COALESCE(inputs,''))+LENGTH(COALESCE(error,''))"),
				batchEligible, batchOld, batchNew),
			cat("tokens",
				s.st.usageCount("api_tokens"),
				s.st.usageBytes("api_tokens", "LENGTH(COALESCE(token,''))+LENGTH(COALESCE(name,''))"),
				tokEligible, tokOld, tokNew),
			cat("reports",
				s.st.usageCount("reports"),
				s.st.usageBytes("reports", "LENGTH(COALESCE(body_md,''))+LENGTH(COALESCE(body_html,''))"),
				repEligible, repOld, repNew),
			cat("chat",
				s.st.usageCount("chat_conversations"),
				s.st.usageBytes("chat_conversations", "LENGTH(COALESCE(title,''))"),
				0, "", ""), // read-only awareness: no retention target for the thin chat index
		},
	})
}

// ---------- preview / run ----------

// readTargets parses an optional {"targets":[...]} body; an empty/absent list means all targets.
func (s *Server) readTargets(r *http.Request) cleanupTargets {
	var in struct {
		Targets []string `json:"targets"`
	}
	_ = readJSON(r, &in)
	if len(in.Targets) == 0 {
		return cleanupTargets{Batch: true, Tokens: true, Reports: true}
	}
	var t cleanupTargets
	for _, x := range in.Targets {
		switch x {
		case "batch":
			t.Batch = true
		case "tokens":
			t.Tokens = true
		case "reports":
			t.Reports = true
		}
	}
	return t
}

// apiCleanupPreview counts what a run WOULD delete for the selected targets at the current
// retention — regardless of each target's scheduled-enable toggle — so the UI can show a live
// count (notably the reports danger-zone confirm) without deleting anything.
func (s *Server) apiCleanupPreview(w http.ResponseWriter, r *http.Request, user string) {
	sel := s.readTargets(r)
	c := s.cleanupConfigLoad()
	now := time.Now()
	batchCut, tokenCut, reportsCut := c.cutoffs(now)
	res := cleanupResult{Trigger: "preview", DryRun: true, OK: true, At: now.UTC().Format(time.RFC3339)}
	if sel.Batch {
		n, err := s.st.CountFinishedJobsBefore(batchCut)
		res.Batch = n
		res.note(err)
	}
	if sel.Tokens {
		n, err := s.st.CountExpiredTokensBefore(tokenCut)
		res.Tokens = n
		res.note(err)
	}
	if sel.Reports {
		n, err := s.st.CountReportsIngestedBefore(reportsCut)
		res.Reports = n
		res.note(err)
	}
	writeJSON(w, res)
}

// apiCleanupRunNow performs a manual cleanup of the selected targets now (records a cleanup_runs row).
func (s *Server) apiCleanupRunNow(w http.ResponseWriter, r *http.Request, user string) {
	res := s.runCleanup("manual", false, s.readTargets(r))
	writeJSON(w, res)
}

// ---------- history ----------

func (s *Server) apiCleanupHistory(w http.ResponseWriter, r *http.Request, user string) {
	runs, err := s.st.ListCleanupRuns(50)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if runs == nil {
		runs = []CleanupRun{}
	}
	writeJSON(w, map[string]any{"runs": runs})
}
