package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/geoip"
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
	// IP is the request's source address, "" for a writer with no request (CLI, scheduler loops).
	// A column rather than a detail field because the question it answers — "what else did this host
	// try" — is an equality lookup, and detail is only reachable through an unindexed substring
	// match that would also hit a target_id containing the same bytes.
	IP string `json:"ip"`
	// Geo is resolved when the log is READ, never stored. A database improves and an
	// address changes hands, so a place written beside the row would be a snapshot of what
	// one build once thought, shown later as fact. The address is the record.
	Geo *geoip.Location `json:"geo,omitempty"`
}

// AuditFilter narrows the log. The zero value is everything.
type AuditFilter struct {
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	IP         string
	Q          string // substring of the detail
	Since      string // "YYYY-MM-DD"; inclusive
	Limit      int
	Offset     int
}

// The action vocabulary the portal itself writes. Kept as constants so a rename is a compile error
// rather than a filter that silently stops matching.
// Naming: <object>.<verb>, one dot, lowercase. The verb is create/change/delete/read where CRUD
// describes it, and a domain verb where CRUD would lie (login, submit, install).
//
// Two rules settle the cases that look ambiguous:
//
//   - auth.* is what a principal does to its OWN authentication; user.change is what an
//     administrator does to somebody ELSE's account. Both carry the username as the target, so one
//     target_id filter is a complete per-account timeline no matter who acted.
//   - A subsystem that already keeps durable, admin-visible history (batch_items, cleanup_runs)
//     gets a row for the human DECISION and none for the machine outcome. That is what keeps run.*
//     at human rate instead of scheduler rate.
//
// The five original strings keep their exact spelling: rows written by older builds have to go on
// filtering, and the console's dropdown is SELECT DISTINCT action, so new ones need no migration.
const (
	AuditReportRead   = "report.read"
	AuditGrantChange  = "grant.change"
	AuditUserChange   = "user.change"
	AuditGroupChange  = "group.change"
	AuditPolicyChange = "policy.change"

	// Authentication — the principal acting on its own credentials.
	AuditLogin          = "auth.login"
	AuditLoginFailed    = "auth.login_failed"
	AuditLockout        = "auth.lockout"
	AuditLogout         = "auth.logout"
	AuditPasswordChange = "auth.password_change"
	AuditPasswordReset  = "auth.password_reset"
	AuditMFAChange      = "auth.mfa_change"
	AuditIdentityLink   = "auth.identity_link"
	AuditIdentityUnlink = "auth.identity_unlink"

	// Accounts and the OU tree, acted on by an administrator.
	AuditUserCreate  = "user.create"
	AuditUserDelete  = "user.delete"
	AuditGroupCreate = "group.create"
	AuditGroupDelete = "group.delete"

	// Reports and runs.
	AuditReportIngest = "report.ingest"
	AuditReportDelete = "report.delete"
	AuditRunSubmit    = "run.submit"
	AuditRunCancel    = "run.cancel"
	AuditRunChange    = "run.change"
	AuditRunDelete    = "run.delete"

	// Credentials and egress — the things that let something out of the portal.
	AuditTokenCreate   = "token.create"
	AuditTokenDelete   = "token.delete"
	AuditAppInstall    = "app.install"
	AuditAppDelete     = "app.delete"
	AuditWebhookCreate = "webhook.create"
	AuditWebhookDelete = "webhook.delete"
	AuditTargetChange  = "target.change"
)

// WriteAudit records one action. Deliberately best-effort and never returns an error to its caller:
// an audit write must not be able to fail the operation it is describing. A portal that refuses to
// serve a report because it could not log the read has turned an observability feature into an
// outage.
func (s *Store) WriteAudit(e AuditEntry) {
	at := e.At
	if at == "" {
		// A UTC instant, in the RFC3339 form the rest of the portal already uses for instants
		// (a report's sent_at, /api/v1/now). It used to be the host's local wall clock, so lining an
		// audit row up against a Dify run — the sandbox is UTC — meant knowing the server's timezone,
		// which nothing recorded. The client renders it in the panel timezone.
		at = time.Now().UTC().Format(time.RFC3339)
	}
	s.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,?,?,?,?,?,?,?)`, at, e.Actor, e.ActorOU, e.Action, e.TargetType, e.TargetID, e.Detail, e.IP)
}

// recordChange records an administrative action: one principal acting on something that is not
// its own credentials. r may be nil for a writer with no request (CLI, scheduler).
//
// Two recorders rather than one WriteAudit everywhere, because the two things every call site got
// wrong are the two these fill in: the actor's OU AT THE TIME, and the source address.
func (s *Server) recordChange(r *http.Request, actor, action, targetType, targetID string, detail map[string]any) {
	s.st.WriteAudit(AuditEntry{
		Actor: actor, ActorOU: s.st.PrimaryGroupOf(actor), Action: action,
		TargetType: targetType, TargetID: targetID,
		Detail: auditJSON(detail), IP: s.auditIP(r),
	})
}

// recordAuth records something a principal did to its own authentication. The target is the account
// either way, so an account's timeline is one target_id filter whether the actor was its holder or
// an administrator — and for a FAILED sign-in the actor may be empty while the target is not, which
// is the case that matters: nobody has authenticated yet, but a name was tried.
func (s *Server) recordAuth(r *http.Request, action, actor, account string, detail map[string]any) {
	s.st.WriteAudit(AuditEntry{
		Actor: actor, ActorOU: s.st.PrimaryGroupOf(actor), Action: action,
		TargetType: "user", TargetID: account,
		Detail: auditJSON(detail), IP: s.auditIP(r),
	})
}

// changedSettingFields lists the fields a partial-update payload actually carried.
//
// Every field on those payloads is a pointer precisely so that "omitted" and "cleared" are
// different, which makes the set of non-nil fields exactly the set an admin touched. The NAMES
// go in the row and the values do not: half of them are of no forensic interest and one of them
// is an SMTP password, so "which knobs moved" is the useful half and carries none of the risk.
func changedSettingFields(in any) []string {
	v := reflect.ValueOf(in)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	t := v.Type()
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.Pointer && !f.IsNil() {
			out = append(out, t.Field(i).Name)
		}
	}
	sort.Strings(out) // stable, so two identical saves read identically in the log
	return out
}

// auditIP resolves the source address through the same trusted-proxy configuration the throttle
// uses, so the two agree about who a request came from. Behind a misconfigured proxy they would
// otherwise disagree, and the log would exonerate whoever the throttle blocked.
func (s *Server) auditIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	return clientIP(r, s.trustedNets)
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
	if f.IP != "" {
		add("ip=?", f.IP)
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
			COALESCE(target_type,''),COALESCE(target_id,''),COALESCE(detail,''),COALESCE(ip,'')
		FROM audit_log WHERE %s ORDER BY at DESC, id DESC LIMIT %d OFFSET %d`, cond, limit, offset), args...)
	if err != nil {
		return nil, total
	}
	defer rows.Close()
	out := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var e AuditEntry
		var ou sql.NullInt64
		if rows.Scan(&e.ID, &e.At, &e.Actor, &ou, &e.Action, &e.TargetType, &e.TargetID, &e.Detail, &e.IP) == nil {
			e.ActorOU = ou.Int64
			out = append(out, e)
		}
	}
	return out, total
}

// DeleteAuditBefore drops entries older than the cutoff.
func (s *Store) DeleteAuditBefore(cutoff time.Time) (int64, error) {
	cond, args := auditBefore(cutoff)
	res, err := s.exec("DELETE FROM audit_log WHERE "+cond, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// auditBefore is the retention predicate, and it handles BOTH stamp formats explicitly rather than
// relying on them happening to sort compatibly.
//
// Rows written before v0.4.15 are the host's local wall clock ("2006-01-02 15:04:05"); rows written
// since are UTC RFC3339. A single lexical cutoff would be wrong for one of them by the host's UTC
// offset — harmless at a 30-day retention, but the kind of nearly-right that survives until the day
// somebody sets retention to one day. Matching each row against a cutoff in its OWN format is exact
// for both, and needs no rewrite of existing rows: an instant that was never recorded as UTC cannot
// be converted to UTC without assuming an offset nobody wrote down.
func auditBefore(cutoff time.Time) (string, []any) {
	return "at <> '' AND ((at " + likeT + " AND at < ?) OR (at NOT " + likeT + " AND at < ?))",
		[]any{cutoff.UTC().Format(time.RFC3339), cutoff.Format("2006-01-02 15:04:05")}
}

// likeT distinguishes the two formats: an RFC3339 stamp has a T between the date and the time, a
// legacy one has a space.
const likeT = "LIKE '%T%'"

// auditVocabulary is every action this build can write. The filter offers these WHETHER OR NOT
// they have happened, because a dropdown built only from the data cannot distinguish "not
// recorded" from "has not happened yet" — a freshly upgraded portal would offer one option and
// look like a log that records one thing.
var auditVocabulary = []string{
	AuditReportRead, AuditReportIngest, AuditReportDelete,
	AuditLogin, AuditLoginFailed, AuditLockout, AuditLogout,
	AuditPasswordChange, AuditPasswordReset, AuditMFAChange,
	AuditIdentityLink, AuditIdentityUnlink,
	AuditUserCreate, AuditUserChange, AuditUserDelete,
	AuditGroupCreate, AuditGroupChange, AuditGroupDelete,
	AuditGrantChange, AuditPolicyChange,
	AuditRunSubmit, AuditRunCancel, AuditRunChange, AuditRunDelete,
	AuditTokenCreate, AuditTokenDelete,
	AuditAppInstall, AuditAppDelete,
	AuditWebhookCreate, AuditWebhookDelete,
	AuditTargetChange,
}

// AuditActions lists what the filter offers: this build's whole vocabulary, plus anything present
// in the data that is not in it. The second half is what keeps a row written by an older build —
// or by a build that has since retired an action — reachable instead of stranded.
func (s *Store) AuditActions() []string {
	seen := map[string]bool{}
	for _, a := range auditVocabulary {
		seen[a] = true
	}
	for _, a := range s.auditActionsPresent() {
		seen[a] = true
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// auditActionsPresent is what the data actually contains.
func (s *Store) auditActionsPresent() []string {
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
	cond, args := auditBefore(cutoff)
	err := s.queryRow("SELECT COUNT(*) FROM audit_log WHERE "+cond, args...).Scan(&n)
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
		IP:         strings.TrimSpace(q.Get("ip")),
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
	// Resolved here rather than in the store, so the log stays a log and the place stays a
	// rendering. One memory-mapped lookup per row, no network, no cache to keep coherent.
	for i := range rows {
		if loc := s.geo.Lookup(rows[i].IP); !loc.Empty() {
			rows[i].Geo = &loc
		}
	}
	writeJSON(w, map[string]any{
		"items": rows, "total": total,
		"actions":  s.st.AuditActions(), // built from the data, so an older build's rows still filter
		"ou_names": ouNames,
		// The stamps are UTC; the panel timezone is what they are READ in. Sent with the page rather
		// than fetched separately, because a time column cannot render without it.
		"timezone": s.st.GetSetting("timezone", ""),
		// Whether an IP database is loaded, and which. Without this the page cannot tell "no
		// database installed" from "installed but every address so far is on the LAN" — and an
		// operator who copied a file in has no way to find out it was rejected.
		"geo": s.geo.Status(),
	})
}
