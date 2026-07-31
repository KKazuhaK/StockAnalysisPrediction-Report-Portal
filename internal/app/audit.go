package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The audit log (retention is the storage-cleanup subsystem's Target D, ADR 0017).
//
// Two audiences, one table. "Who read this report" is what a client asks, and it is the only
// question the portal can answer with evidence rather than assurance — which is what makes it worth
// keeping once the portal serves people outside the company. "Who changed this grant" is what an
// operator asks when something is visible that should not be. Both are actor / action / target /
// when, so they share one table and one query.

// AuditEntry is one recorded action.
type AuditEntry struct {
	ID         int64  `json:"id"`
	At         string `json:"at"`
	Actor      string `json:"actor"`    // "" for a machine caller holding a token
	ActorOU    int64  `json:"actor_ou"` // the actor's OU AT THE TIME; see the schema comment
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Detail     string `json:"detail"` // JSON
}

// AuditFilter narrows the log. The zero value is everything.
type AuditFilter struct {
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	Q          string // substring of the detail
	Since      string // "YYYY-MM-DD"; inclusive
	Limit      int
	Offset     int
}

// The action vocabulary the portal itself writes. Kept as constants so a rename is a compile error
// rather than a filter that silently stops matching.
const (
	AuditReportRead   = "report.read"
	AuditGrantChange  = "grant.change"
	AuditUserChange   = "user.change"
	AuditGroupChange  = "group.change"
	AuditPolicyChange = "policy.change"
)

// WriteAudit records one action. Deliberately best-effort and never returns an error to its caller:
// an audit write must not be able to fail the operation it is describing. A portal that refuses to
// serve a report because it could not log the read has turned an observability feature into an
// outage.
func (s *Store) WriteAudit(e AuditEntry) {
	at := e.At
	if at == "" {
		at = nowStr()
	}
	s.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail)
		VALUES(?,?,?,?,?,?,?)`, at, e.Actor, e.ActorOU, e.Action, e.TargetType, e.TargetID, e.Detail)
}

// ListAudit returns one page newest-first, plus the total matching the filter.
func (s *Store) ListAudit(f AuditFilter) ([]AuditEntry, int) {
	where, args := []string{"1=1"}, []any{}
	add := func(cond string, v ...any) {
		where = append(where, cond)
		args = append(args, v...)
	}
	if f.Actor != "" {
		add("actor=?", f.Actor)
	}
	if f.Action != "" {
		add("action=?", f.Action)
	}
	if f.TargetType != "" {
		add("target_type=?", f.TargetType)
	}
	if f.TargetID != "" {
		add("target_id=?", f.TargetID)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		add("(detail "+s.likeOp()+" ? OR target_id "+s.likeOp()+" ? OR actor "+s.likeOp()+" ?)",
			"%"+q+"%", "%"+q+"%", "%"+q+"%")
	}
	if f.Since != "" {
		add("at >= ?", f.Since)
	}
	cond := strings.Join(where, " AND ")

	var total int
	s.queryRow("SELECT COUNT(*) FROM audit_log WHERE "+cond, args...).Scan(&total)

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := s.query(fmt.Sprintf(`SELECT id,at,COALESCE(actor,''),COALESCE(actor_ou,0),action,
			COALESCE(target_type,''),COALESCE(target_id,''),COALESCE(detail,'')
		FROM audit_log WHERE %s ORDER BY at DESC, id DESC LIMIT %d OFFSET %d`, cond, limit, offset), args...)
	if err != nil {
		return nil, total
	}
	defer rows.Close()
	out := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var e AuditEntry
		var ou sql.NullInt64
		if rows.Scan(&e.ID, &e.At, &e.Actor, &ou, &e.Action, &e.TargetType, &e.TargetID, &e.Detail) == nil {
			e.ActorOU = ou.Int64
			out = append(out, e)
		}
	}
	return out, total
}

// DeleteAuditBefore drops entries older than the cutoff. `at` is written by nowStr() (local
// "2006-01-02 15:04:05"), so a lexical compare is a correct chronological one.
func (s *Store) DeleteAuditBefore(cutoff time.Time) (int64, error) {
	res, err := s.exec("DELETE FROM audit_log WHERE at <> '' AND at < ?",
		cutoff.Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// AuditActions lists the action values actually present, so the filter is built from the data
// rather than from the constants above — a log written by an older build still filters correctly.
func (s *Store) AuditActions() []string {
	rows, err := s.query("SELECT DISTINCT action FROM audit_log WHERE action <> '' ORDER BY 1")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if rows.Scan(&a) == nil {
			out = append(out, a)
		}
	}
	return out
}

// auditJSON renders a detail payload, returning "" rather than failing: a detail that cannot be
// encoded must not cost the audit line itself.
func auditJSON(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// CountAuditBefore is the dry-run counterpart of DeleteAuditBefore: the cleanup console previews a
// pass before it runs, and a preview that counted differently from the delete would be a lie.
func (s *Store) CountAuditBefore(cutoff time.Time) (int64, error) {
	var n int64
	err := s.queryRow("SELECT COUNT(*) FROM audit_log WHERE at <> '' AND at < ?",
		cutoff.Format("2006-01-02 15:04:05")).Scan(&n)
	return n, err
}

// GET /api/admin/audit — read the log. Admin-only for now, but gated on a PERMISSION rather than on
// role == "admin", so granting it to an OU-scoped role later is a registry entry and not a change
// here. That is also why every row carries actor_ou: narrowing this to one OU becomes a filter.
func (s *Server) apiAdminAudit(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	rows, total := s.st.ListAudit(AuditFilter{
		Actor:      strings.TrimSpace(q.Get("actor")),
		Action:     strings.TrimSpace(q.Get("action")),
		TargetType: strings.TrimSpace(q.Get("target_type")),
		TargetID:   strings.TrimSpace(q.Get("target_id")),
		Q:          strings.TrimSpace(q.Get("q")),
		Since:      strings.TrimSpace(q.Get("since")),
		Limit:      limit,
		Offset:     offset,
	})
	// OU ids are meaningless on screen; resolve them once here rather than per row in the client.
	ouNames := map[string]string{}
	for _, g := range s.st.ListUserGroups() {
		ouNames[strconv.FormatInt(g.ID, 10)] = g.Name
	}
	writeJSON(w, map[string]any{
		"items": rows, "total": total,
		"actions":  s.st.AuditActions(), // built from the data, so an older build's rows still filter
		"ou_names": ouNames,
	})
}
