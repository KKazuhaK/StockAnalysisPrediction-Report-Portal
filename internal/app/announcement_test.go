package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------- helpers ----------

func announcementReq(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("{}")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	// PathValue only gets populated by the mux, so set the id the handlers read.
	if i := strings.LastIndex(path, "/"); i >= 0 && method != "POST" {
		if last := path[i+1:]; last != "" && last != "announcements" && last != "reorder" {
			req.SetPathValue("id", last)
		}
	}
	switch {
	case method == http.MethodPost && strings.HasSuffix(path, "/reorder"):
		s.apiAnnouncementReorder(rec, req, "admin")
	case method == http.MethodPost:
		s.apiAnnouncementAdd(rec, req, "admin")
	case method == http.MethodPut:
		s.apiAnnouncementSave(rec, req, "admin")
	case method == http.MethodPatch:
		s.apiAnnouncementToggle(rec, req, "admin")
	case method == http.MethodDelete:
		s.apiAnnouncementDelete(rec, req, "admin")
	default:
		s.apiAdminAnnouncements(rec, req, "admin")
	}
	return rec
}

func mustAddAnnouncement(t *testing.T, s *Server, body string) int64 {
	t.Helper()
	rec := announcementReq(t, s, http.MethodPost, "/api/admin/announcements", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("add announcement: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct{ ID int64 }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode add response: %v", err)
	}
	return out.ID
}

// readerFeed calls the reader endpoint as one user and returns the items.
func readerFeed(t *testing.T, s *Server, user string) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiAnnouncements(rec, httptest.NewRequest("GET", "/api/announcements", nil), user)
	if rec.Code != http.StatusOK {
		t.Fatalf("apiAnnouncements status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode reader feed: %v", err)
	}
	return out.Items
}

// feedTitles labels each item by its title, falling back to its body: an announcement is allowed
// to be body-only, and a test that indexed on title alone would report those as blanks.
func feedTitles(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		label := fmt.Sprint(it["title"])
		if label == "" {
			label = fmt.Sprint(it["content"])
		}
		out = append(out, label)
	}
	return out
}

// ---------- the public payload ----------

// The announcement left /api/site because that endpoint is served with no auth at all — the login
// page reads it before anyone signs in. While there was one announcement for everybody, publishing
// it there was merely untidy; with per-audience rows it would mean an anonymous visitor could read
// a notice addressed to one OU, and could poll for new ones. This pins the whole key set rather
// than just the five names, because the way this regresses is somebody adding a sixth field that
// "obviously" belongs with the branding.
func TestPublicSiteSettingsKeySetIsFrozen(t *testing.T) {
	s := newV1Server(t)
	postSettingsJSON(t, s, map[string]any{
		"announcementEnabled": true,
		"announcementTitle":   "机密通告",
		"announcementContent": "只给内部看",
	})

	want := map[string]bool{
		"siteTitle": true, "siteLogoUrl": true, "homeMoreStyle": true, "footerText": true,
		"footerShowInfo": true, "footerShowVersion": true, "pwaEnabled": true, "pwaIconUrl": true,
	}
	got := publicSiteSettings(t, s)
	for k := range got {
		if !want[k] {
			t.Errorf("public /api/site published an unexpected key %q — anonymous callers can read it", k)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("public /api/site lost key %q", k)
		}
	}
}

// ---------- the v0.4 adoption step ----------

func TestImportLegacyAnnouncementFoldsTheOldSingleAnnouncement(t *testing.T) {
	st := newTestStore(t)
	// Simulate a database written by the previous release: five meta keys, no rows, no marker.
	st.SetSetting("announcement_enabled", "true")
	st.SetSetting("announcement_popup", "on")
	st.SetSetting("announcement_level", " Warning ")
	st.SetSetting("announcement_title", " 提示：iFind数据源暂不可用 ")
	st.SetSetting("announcement_content", " 预计 18:00 恢复。 ")
	// newTestStore already booted (and therefore already marked the import done), so drop the
	// marker to put the store back in the shape a database from the previous release is in.
	if _, err := st.exec("DELETE FROM meta WHERE k='announcements_imported'"); err != nil {
		t.Fatalf("clear marker: %v", err)
	}

	if err := st.upgradeV04(); err != nil {
		t.Fatalf("upgradeV04: %v", err)
	}
	list := st.Announcements()
	if len(list) != 1 {
		t.Fatalf("imported %d rows, want 1", len(list))
	}
	a := list[0]
	// Upgrade day must look like nothing happened: same text, same level, same popup, and the
	// scope/audience that reproduce the old home-page-only, everybody behaviour.
	if a.Title != "提示：iFind数据源暂不可用" || a.Content != "预计 18:00 恢复。" {
		t.Errorf("imported text = %q / %q", a.Title, a.Content)
	}
	if a.Level != "warning" || !a.Enabled || !a.Popup {
		t.Errorf("imported flags = level %q enabled %v popup %v", a.Level, a.Enabled, a.Popup)
	}
	if a.Scope != "home" || a.Audience != "all" || a.Dismissible {
		t.Errorf("imported row should reproduce today's behaviour, got scope=%q audience=%q dismissible=%v",
			a.Scope, a.Audience, a.Dismissible)
	}
	// Rolling back to the previous binary must show no announcement rather than a stale one.
	if settingBool(st.GetSetting("announcement_enabled", ""), false) {
		t.Errorf("legacy announcement_enabled should be turned off after the import")
	}

	// Idempotent, and NOT because the table is non-empty: the guard is the marker.
	if err := st.upgradeV04(); err != nil {
		t.Fatalf("second upgradeV04: %v", err)
	}
	if n := len(st.Announcements()); n != 1 {
		t.Fatalf("re-running the import produced %d rows, want 1", n)
	}

	// An operator who deletes every announcement has an empty table on purpose. The next restart
	// must leave it empty — "looks empty, so import again" is the bug this marker exists to avoid.
	if err := st.DeleteAnnouncement(a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := st.upgradeV04(); err != nil {
		t.Fatalf("third upgradeV04: %v", err)
	}
	if n := len(st.Announcements()); n != 0 {
		t.Fatalf("import resurrected %d deleted rows", n)
	}
}

func TestImportLegacyAnnouncementSkipsAnEmptyOne(t *testing.T) {
	st := newTestStore(t)
	if err := st.upgradeV04(); err != nil {
		t.Fatalf("upgradeV04: %v", err)
	}
	if n := len(st.Announcements()); n != 0 {
		t.Fatalf("fresh database imported %d rows, want 0", n)
	}
	if st.GetSetting("announcements_imported", "") == "" {
		t.Errorf("marker not written on a fresh database — the import would re-run every boot")
	}
}

// ---------- CRUD, ordering ----------

func TestAnnouncementReorderIsWholeSetOrNothing(t *testing.T) {
	s := newV1Server(t)
	a := mustAddAnnouncement(t, s, `{"title":"一"}`)
	b := mustAddAnnouncement(t, s, `{"title":"二"}`)
	c := mustAddAnnouncement(t, s, `{"title":"三"}`)

	if got := feedTitles(readerFeed(t, s, "admin")); strings.Join(got, ",") != "一,二,三" {
		t.Fatalf("initial order = %v", got)
	}
	rec := announcementReq(t, s, http.MethodPost, "/api/admin/announcements/reorder",
		fmt.Sprintf(`{"ids":[%d,%d,%d]}`, c, a, b))
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := feedTitles(readerFeed(t, s, "admin")); strings.Join(got, ",") != "三,一,二" {
		t.Fatalf("after reorder = %v", got)
	}

	// A page whose list is stale — another admin added or deleted a row, or its own GET failed and
	// it never rendered — must not be able to rewrite the whole order from what it thinks it has.
	for name, body := range map[string]string{
		"missing an id": fmt.Sprintf(`{"ids":[%d,%d]}`, a, b),
		"unknown id":    fmt.Sprintf(`{"ids":[%d,%d,%d,9999]}`, a, b, c),
		"duplicate id":  fmt.Sprintf(`{"ids":[%d,%d,%d]}`, a, a, b),
		"empty":         `{"ids":[]}`,
	} {
		rec := announcementReq(t, s, http.MethodPost, "/api/admin/announcements/reorder", body)
		if rec.Code != http.StatusConflict {
			t.Errorf("reorder %s: status=%d, want 409 (body=%s)", name, rec.Code, rec.Body.String())
		}
	}
	// ...and none of those rejections may have moved anything.
	if got := feedTitles(readerFeed(t, s, "admin")); strings.Join(got, ",") != "三,一,二" {
		t.Fatalf("a rejected reorder changed the order: %v", got)
	}
}

// Dragging a row must not look like editing it. The reader keys "don't show again" on the
// announcement's id plus a hash of its text, so ord has to be the only thing a reorder touches —
// otherwise every drag re-fires every popup somebody had already dismissed.
func TestReorderLeavesContentAndTimestampsAlone(t *testing.T) {
	s := newV1Server(t)
	a := mustAddAnnouncement(t, s, `{"title":"一","content":"正文"}`)
	b := mustAddAnnouncement(t, s, `{"title":"二"}`)
	before := *s.st.GetAnnouncement(a)

	rec := announcementReq(t, s, http.MethodPost, "/api/admin/announcements/reorder",
		fmt.Sprintf(`{"ids":[%d,%d]}`, b, a))
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder status=%d", rec.Code)
	}
	after := *s.st.GetAnnouncement(a)
	if after.Title != before.Title || after.Content != before.Content || after.Level != before.Level ||
		after.UpdatedAt != before.UpdatedAt {
		t.Errorf("reorder changed more than ord:\nbefore %+v\nafter  %+v", before, after)
	}
	if after.Ord == before.Ord {
		t.Errorf("reorder did not change ord")
	}
}

// The inline switches go through PATCH, and this is why: a whole-row PUT would carry back the
// title, body and audience the admin's browser loaded minutes ago. On a targeted row that turns a
// convenience toggle into a silent disclosure change.
func TestToggleTouchesOnlyTheFlag(t *testing.T) {
	s := newV1Server(t)
	id := mustAddAnnouncement(t, s, `{"title":"原标题","content":"原正文","popup":true}`)

	// Somebody else edits the text while the list page holds a stale copy.
	if rec := announcementReq(t, s, http.MethodPut, fmt.Sprintf("/api/admin/announcements/%d", id),
		`{"title":"新标题"}`); rec.Code != http.StatusOK {
		t.Fatalf("edit status=%d", rec.Code)
	}
	if rec := announcementReq(t, s, http.MethodPatch, fmt.Sprintf("/api/admin/announcements/%d", id),
		`{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("toggle status=%d", rec.Code)
	}
	got := s.st.GetAnnouncement(id)
	if got.Enabled {
		t.Errorf("toggle did not disable the row")
	}
	if got.Title != "新标题" {
		t.Errorf("toggle reverted the other admin's edit: title=%q", got.Title)
	}
	if !got.Popup {
		t.Errorf("toggling enabled also changed popup")
	}
}

func TestConcurrentEditIsRejectedNotMerged(t *testing.T) {
	s := newV1Server(t)
	id := mustAddAnnouncement(t, s, `{"title":"原标题"}`)
	stale := s.st.GetAnnouncement(id).UpdatedAt

	// Admin A saves first.
	if rec := announcementReq(t, s, http.MethodPut, fmt.Sprintf("/api/admin/announcements/%d", id),
		fmt.Sprintf(`{"title":"A 的版本","updatedAt":%q}`, stale)); rec.Code != http.StatusOK {
		t.Fatalf("first save status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Admin B still holds the old timestamp and must be told to reload rather than winning.
	rec := announcementReq(t, s, http.MethodPut, fmt.Sprintf("/api/admin/announcements/%d", id),
		fmt.Sprintf(`{"title":"B 的版本","updatedAt":%q}`, stale))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale save status=%d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := s.st.GetAnnouncement(id).Title; got != "A 的版本" {
		t.Errorf("stale save won: title=%q", got)
	}
}

// ---------- reader-side filtering ----------

func TestReaderFeedHonoursEnabledEmptyAndWindow(t *testing.T) {
	s := newV1Server(t)
	past := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	justPast := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	future := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)

	mustAddAnnouncement(t, s, `{"title":"生效中"}`)
	mustAddAnnouncement(t, s, `{"title":"已关闭","enabled":false}`)
	mustAddAnnouncement(t, s, `{"content":"只有正文"}`)
	mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"未开始","startsAt":%q}`, future))
	mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"已过期","startsAt":%q,"endsAt":%q}`, past, justPast))
	mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"窗口内","startsAt":%q,"endsAt":%q}`, past, future))

	got := feedTitles(readerFeed(t, s, "admin"))
	want := "生效中,只有正文,窗口内"
	if strings.Join(got, ",") != want {
		t.Errorf("reader feed = %v, want %s", got, want)
	}
}

func TestWindowRejectsAnInvertedRange(t *testing.T) {
	s := newV1Server(t)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	rec := announcementReq(t, s, http.MethodPost, "/api/admin/announcements",
		fmt.Sprintf(`{"title":"永不显示","startsAt":%q,"endsAt":%q}`, future, past))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("inverted window status=%d, want 400", rec.Code)
	}
	if n := s.st.CountAnnouncements(); n != 0 {
		t.Errorf("rejected announcement was still written (%d rows)", n)
	}
}

// A time bound is stored as a UTC instant, never as the operator's civil string: the panel
// timezone is a setting, and a civil string would silently change what the row means when it moves.
func TestWindowIsStoredAsAUTCInstant(t *testing.T) {
	s := newV1Server(t)
	id := mustAddAnnouncement(t, s, `{"title":"维护","endsAt":"2026-09-01T22:00:00+08:00"}`)
	if got := s.st.GetAnnouncement(id).EndsAt; got != "2026-09-01T14:00:00Z" {
		t.Errorf("stored endsAt=%q, want the same instant in UTC", got)
	}
}

// ---------- targeting ----------

func TestAudienceTargetingFollowsTheOUChain(t *testing.T) {
	s := newV1Server(t)
	mustAddUser(t, s, "east", false)
	mustAddUser(t, s, "eastchild", false)
	mustAddUser(t, s, "west", false)
	mustAddUser(t, s, "nobodyelse", false)

	parent, err := s.st.CreateUserGroup("华东", "", 0)
	if err != nil {
		t.Fatalf("create parent OU: %v", err)
	}
	child, err := s.st.CreateUserGroup("华东-上海", "", 0)
	if err != nil {
		t.Fatalf("create child OU: %v", err)
	}
	other, err := s.st.CreateUserGroup("华西", "", 0)
	if err != nil {
		t.Fatalf("create other OU: %v", err)
	}
	if err := s.st.SetGroupParent(child, parent); err != nil {
		t.Fatalf("nest OU: %v", err)
	}
	s.st.SetPrimaryGroup("east", parent)
	s.st.SetPrimaryGroup("eastchild", child)
	s.st.SetPrimaryGroup("west", other)

	mustAddAnnouncement(t, s, `{"title":"全体通告"}`)
	mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"华东停电","audience":"grant","grants":["g:%d"]}`, parent))
	mustAddAnnouncement(t, s, `{"title":"给一个人","audience":"grant","grants":["u:west"]}`)

	cases := map[string][]string{
		// A notice sent to a parent OU reaches the whole subtree — a broadcast, not a right.
		"east":       {"全体通告", "华东停电"},
		"eastchild":  {"全体通告", "华东停电"},
		"west":       {"全体通告", "给一个人"},
		"nobodyelse": {"全体通告"},
	}
	for user, want := range cases {
		got := feedTitles(readerFeed(t, s, user))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("feed for %s = %v, want %v", user, got, want)
		}
	}

	// An anonymous caller cannot reach this endpoint at all, but the filter must be safe on its
	// own terms: no principals means no targeted announcement, never "no filter, so everything".
	got := feedTitles(readerFeed(t, s, ""))
	if strings.Join(got, ",") != "全体通告" {
		t.Errorf("empty-principal feed = %v, want only the untargeted one", got)
	}
}

// The reader payload must not even hint that a targeted announcement exists: which OUs are being
// addressed is the shape of the org chart.
func TestReaderPayloadNeverCarriesAudience(t *testing.T) {
	s := newV1Server(t)
	mustAddUser(t, s, "alice", false)
	gid, err := s.st.CreateUserGroup("华东", "", 0)
	if err != nil {
		t.Fatalf("create OU: %v", err)
	}
	s.st.SetPrimaryGroup("alice", gid)
	mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"华东停电","audience":"grant","grants":["g:%d"]}`, gid))

	items := readerFeed(t, s, "alice")
	if len(items) != 1 {
		t.Fatalf("feed = %v", items)
	}
	for _, k := range []string{"audience", "grants", "ord", "enabled", "createdBy"} {
		if _, ok := items[0][k]; ok {
			t.Errorf("reader payload carries admin-only key %q", k)
		}
	}
}

func TestTargetedAnnouncementRequiresSomebodyToSendItTo(t *testing.T) {
	s := newV1Server(t)
	rec := announcementReq(t, s, http.MethodPost, "/api/admin/announcements",
		`{"title":"发给空气","audience":"grant","grants":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty audience status=%d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if n := s.st.CountAnnouncements(); n != 0 {
		t.Errorf("an announcement nobody can see was stored anyway")
	}
}

// The Default OU is on every account's chain, so granting it means "everyone" — which
// audience=all already says, and which would quietly include every external tenant beneath it.
func TestDefaultOUIsRejectedAsAnAudience(t *testing.T) {
	s := newV1Server(t)
	def := s.st.DefaultGroupID()
	if def == 0 {
		t.Skip("no default OU in this store")
	}
	rec := announcementReq(t, s, http.MethodPost, "/api/admin/announcements",
		fmt.Sprintf(`{"title":"看着像定向","audience":"grant","grants":["g:%d"]}`, def))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("default-OU audience status=%d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// A principal that does not resolve is refused rather than stored: "u:Alice" saved verbatim would
// never match the lower-cased principal the read path builds, producing an announcement nobody
// receives and nothing anywhere to say why.
func TestUnknownPrincipalIsRefused(t *testing.T) {
	s := newV1Server(t)
	for _, p := range []string{"g:9999", "u:ghost", "alice", ""} {
		rec := announcementReq(t, s, http.MethodPost, "/api/admin/announcements",
			fmt.Sprintf(`{"title":"x","audience":"grant","grants":[%q]}`, p))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("principal %q: status=%d, want 400", p, rec.Code)
		}
	}
}

func TestPrincipalIsNormalizedOnSave(t *testing.T) {
	s := newV1Server(t)
	mustAddUser(t, s, "alice", false)
	id := mustAddAnnouncement(t, s, `{"title":"给 Alice","audience":"grant","grants":["u:  ALICE  "]}`)
	if got := s.st.AllAnnouncementGrants()[id]; len(got) != 1 || got[0] != "u:alice" {
		t.Fatalf("stored grants = %v, want [u:alice]", got)
	}
	if got := feedTitles(readerFeed(t, s, "alice")); strings.Join(got, ",") != "给 Alice" {
		t.Errorf("alice's feed = %v", got)
	}
}

// An omitted grants field means "leave the audience alone". If it were a plain slice, every
// partial save — the list page toggling something, a title fix — would silently clear it.
func TestOmittedGrantsLeaveTheAudienceAlone(t *testing.T) {
	s := newV1Server(t)
	mustAddUser(t, s, "alice", false)
	id := mustAddAnnouncement(t, s, `{"title":"原标题","audience":"grant","grants":["u:alice"]}`)

	rec := announcementReq(t, s, http.MethodPut, fmt.Sprintf("/api/admin/announcements/%d", id),
		`{"title":"改个错别字"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := s.st.AllAnnouncementGrants()[id]; len(got) != 1 || got[0] != "u:alice" {
		t.Fatalf("a title edit changed the audience: %v", got)
	}
}

// ---------- "preview as" ----------

func announcementPreview(t *testing.T, s *Server, principal string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/admin/announcements/preview?principal="+principal, nil)
	s.apiAnnouncementPreview(rec, req, "admin")
	return rec
}

// Admins are not exempt from the audience filter, so a targeted announcement fails SILENTLY: the
// admin who wrote it cannot see it, and the people who should get it do not know to expect it.
// This endpoint is the only cheap way to tell "addressed correctly" from "addressed to nobody".
func TestPreviewAnswersWhatAPrincipalWouldSee(t *testing.T) {
	s := newV1Server(t)
	mustAddUser(t, s, "shanghai", false)
	parent, err := s.st.CreateUserGroup("华东", "", 0)
	if err != nil {
		t.Fatalf("create parent OU: %v", err)
	}
	child, err := s.st.CreateUserGroup("华东-上海", "", 0)
	if err != nil {
		t.Fatalf("create child OU: %v", err)
	}
	other, err := s.st.CreateUserGroup("华西", "", 0)
	if err != nil {
		t.Fatalf("create other OU: %v", err)
	}
	if err := s.st.SetGroupParent(child, parent); err != nil {
		t.Fatalf("nest OU: %v", err)
	}
	s.st.SetPrimaryGroup("shanghai", child)

	mustAddAnnouncement(t, s, `{"title":"全体通告"}`)
	mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"华东停电","audience":"grant","grants":["g:%d"]}`, parent))
	mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"华西停电","audience":"grant","grants":["g:%d"]}`, other))

	read := func(principal string) []string {
		t.Helper()
		rec := announcementPreview(t, s, principal)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview %s: status=%d body=%s", principal, rec.Code, rec.Body.String())
		}
		var out struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode preview: %v", err)
		}
		return feedTitles(out.Items)
	}

	// Previewing an OU means "a member of this OU", ancestry included — so the child OU sees the
	// notice sent to its parent, exactly as the real read path would deliver it.
	if got := strings.Join(read(fmt.Sprintf("g:%d", child)), ","); got != "全体通告,华东停电" {
		t.Errorf("preview of the child OU = %s", got)
	}
	if got := strings.Join(read(fmt.Sprintf("g:%d", other)), ","); got != "全体通告,华西停电" {
		t.Errorf("preview of the unrelated OU = %s", got)
	}
	// An account preview must agree with what that account's own feed returns. A preview that can
	// disagree with the real answer is worse than no preview.
	if got, want := strings.Join(read("u:shanghai"), ","), strings.Join(feedTitles(readerFeed(t, s, "shanghai")), ","); got != want {
		t.Errorf("preview of u:shanghai = %s, but their feed = %s", got, want)
	}
}

func TestPreviewRefusesAPrincipalThatDoesNotResolve(t *testing.T) {
	s := newV1Server(t)
	for _, p := range []string{"g:9999", "u:ghost", "alice", ""} {
		if rec := announcementPreview(t, s, p); rec.Code != http.StatusBadRequest {
			t.Errorf("preview %q: status=%d, want 400", p, rec.Code)
		}
	}
}

// ---------- the audience picker ----------

func TestAdminListOffersAddressablePrincipalsButNotTheDefaultOU(t *testing.T) {
	s := newV1Server(t)
	mustAddUser(t, s, "alice", false)
	named, err := s.st.CreateUserGroup("华东", "", 0)
	if err != nil {
		t.Fatalf("create OU: %v", err)
	}

	rec := announcementReq(t, s, http.MethodGet, "/api/admin/announcements", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list status=%d", rec.Code)
	}
	var out struct {
		Groups []struct{ Principal string } `json:"groups"`
		Users  []struct{ Principal string } `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode admin list: %v", err)
	}
	seen := map[string]bool{}
	for _, g := range out.Groups {
		seen[g.Principal] = true
	}
	if !seen[groupPrincipal(named)] {
		t.Errorf("the named OU is missing from the picker: %v", out.Groups)
	}
	// The Default OU is on every account's chain, so offering it would be offering "everyone"
	// under a name that reads like a subset. The save path refuses it; the picker must not tempt.
	if seen[groupPrincipal(s.st.DefaultGroupID())] {
		t.Errorf("the Default OU was offered as an audience")
	}
	if len(out.Users) == 0 {
		t.Errorf("no accounts offered")
	}
}

// ---------- validation, deletion ----------

func TestAnnouncementValidationRejectsBeforeWriting(t *testing.T) {
	s := newV1Server(t)
	long := func(n int) string { return strings.Repeat("界", n) }
	for name, body := range map[string]string{
		"bad level":    `{"level":"critical"}`,
		"bad scope":    `{"scope":"everywhere"}`,
		"bad audience": `{"audience":"secret"}`,
		"bad window":   `{"startsAt":"not-a-time"}`,
		"long title":   fmt.Sprintf(`{"title":%q}`, long(maxAnnouncementTitleRunes+1)),
		"long content": fmt.Sprintf(`{"content":%q}`, long(maxAnnouncementContentRunes+1)),
	} {
		rec := announcementReq(t, s, http.MethodPost, "/api/admin/announcements", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400 (body=%s)", name, rec.Code, rec.Body.String())
		}
	}
	if n := s.st.CountAnnouncements(); n != 0 {
		t.Errorf("%d rejected announcements were written anyway", n)
	}
}

// There is no ceiling on the count, deliberately: refusing a save is not how you stop readers
// ignoring an overcrowded band, and it refuses at the moment an operator most needs to broadcast.
// The console warns instead, and the reader folds the overflow behind a counter.
func TestAnnouncementCountIsNotCapped(t *testing.T) {
	s := newV1Server(t)
	for i := 0; i < 60; i++ {
		mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"第 %d 条"}`, i))
	}
	if n := s.st.CountAnnouncements(); n != 60 {
		t.Fatalf("stored %d announcements, want 60", n)
	}
	if n := len(readerFeed(t, s, "admin")); n != 60 {
		t.Errorf("reader feed returned %d, want all 60", n)
	}
}

func groupDelete(t *testing.T, s *Server, id int64, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/admin/groups/%d%s", id, query), nil)
	req.SetPathValue("id", fmt.Sprint(id))
	s.apiGroupDelete(rec, req, "admin")
	return rec
}

// Deleting an OU sweeps its grants, which can leave an announcement addressed to nobody: still
// enabled, still "live", reaching no one and saying nothing about it. The endpoint refuses until
// the caller decides what should happen — at the API, not only in the console, so a script that
// deletes OUs cannot take the silent path.
func TestDeletingAnOUDemandsADecisionAboutItsAnnouncements(t *testing.T) {
	s := newV1Server(t)
	gid, err := s.st.CreateUserGroup("华东", "", 0)
	if err != nil {
		t.Fatalf("create OU: %v", err)
	}
	id := mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"华东停电","audience":"grant","grants":["g:%d"]}`, gid))

	rec := groupDelete(t, s, gid, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete without a choice: status=%d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if s.st.GetAnnouncement(id) == nil || len(s.st.ListUserGroups()) == 0 {
		t.Fatalf("the refused delete went ahead anyway")
	}

	if rec := groupDelete(t, s, gid, "?orphans=disable"); rec.Code != http.StatusOK {
		t.Fatalf("delete with a choice: status=%d body=%s", rec.Code, rec.Body.String())
	}
	a := s.st.GetAnnouncement(id)
	if a == nil {
		t.Fatal("the announcement was deleted; it should have been switched off")
	}
	if a.Enabled {
		t.Errorf("orphans=disable left the announcement enabled and unreachable")
	}
	if got := s.st.AllAnnouncementGrants()[id]; len(got) != 0 {
		t.Errorf("grants outlived the OU: %v", got)
	}
}

func TestKeepingAnOrphanedAnnouncementIsAllowedWhenAsked(t *testing.T) {
	s := newV1Server(t)
	gid, err := s.st.CreateUserGroup("华东", "", 0)
	if err != nil {
		t.Fatalf("create OU: %v", err)
	}
	id := mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"华东停电","audience":"grant","grants":["g:%d"]}`, gid))

	if rec := groupDelete(t, s, gid, "?orphans=keep"); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if a := s.st.GetAnnouncement(id); a == nil || !a.Enabled {
		t.Errorf("orphans=keep should leave the row exactly as it was; the console flags it instead")
	}
	// And it really does reach nobody now — which is what the console has to say out loud.
	if n := len(readerFeed(t, s, "admin")); n != 0 {
		t.Errorf("an announcement with no recipients was delivered to %d readers", n)
	}
}

// An OU that is one of several recipients is not an orphan-maker: the announcement keeps working,
// so there is nothing to decide and the delete goes straight through.
func TestDeletingOneOfSeveralRecipientsNeedsNoDecision(t *testing.T) {
	s := newV1Server(t)
	a1, _ := s.st.CreateUserGroup("华东", "", 0)
	a2, _ := s.st.CreateUserGroup("华西", "", 0)
	id := mustAddAnnouncement(t, s,
		fmt.Sprintf(`{"title":"两地停电","audience":"grant","grants":["g:%d","g:%d"]}`, a1, a2))

	if rec := groupDelete(t, s, a1, ""); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if a := s.st.GetAnnouncement(id); a == nil || !a.Enabled {
		t.Fatalf("the announcement should be untouched")
	}
	if got := s.st.AllAnnouncementGrants()[id]; len(got) != 1 || got[0] != groupPrincipal(a2) {
		t.Errorf("remaining grants = %v, want just the surviving OU", got)
	}
}

func TestGroupAnnouncementImpactIsReadableBeforeDeleting(t *testing.T) {
	s := newV1Server(t)
	gid, _ := s.st.CreateUserGroup("华东", "", 0)
	other, _ := s.st.CreateUserGroup("华西", "", 0)
	mustAddAnnouncement(t, s, fmt.Sprintf(`{"title":"只发华东","audience":"grant","grants":["g:%d"]}`, gid))
	mustAddAnnouncement(t, s,
		fmt.Sprintf(`{"title":"两地都发","audience":"grant","grants":["g:%d","g:%d"]}`, gid, other))
	mustAddAnnouncement(t, s, `{"title":"全体通告"}`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/admin/groups/%d/announcements", gid), nil)
	req.SetPathValue("id", fmt.Sprint(gid))
	s.apiGroupAnnouncements(rec, req, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var out struct {
		Affected []struct {
			Title    string `json:"title"`
			Orphaned bool   `json:"orphaned"`
		} `json:"affected"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Affected) != 2 {
		t.Fatalf("affected = %v, want the two addressed to this OU (not the untargeted one)", out.Affected)
	}
	got := map[string]bool{}
	for _, a := range out.Affected {
		got[a.Title] = a.Orphaned
	}
	// The distinction the operator needs: one of these stops reaching anybody, the other does not.
	if !got["只发华东"] || got["两地都发"] {
		t.Errorf("orphaned flags = %v", got)
	}
}

func TestDeleteRemovesTheGrantsToo(t *testing.T) {
	s := newV1Server(t)
	mustAddUser(t, s, "alice", false)
	id := mustAddAnnouncement(t, s, `{"title":"x","audience":"grant","grants":["u:alice"]}`)
	if rec := announcementReq(t, s, http.MethodDelete, fmt.Sprintf("/api/admin/announcements/%d", id), ""); rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d", rec.Code)
	}
	if got := s.st.AllAnnouncementGrants(); len(got) != 0 {
		t.Errorf("grants outlived their announcement: %v", got)
	}
}

// An empty stored level reads as notice — the rule the old public endpoint used to carry, now on
// the row instead. Written straight to the store, because the API normalizes on the way in.
func TestAnnouncementLevelFallback(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.exec(`INSERT INTO announcements(level,title,enabled,scope,audience) VALUES('','x',1,'','')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := st.Announcements()[0]
	if a.Level != "notice" || a.Scope != "home" || a.Audience != "all" {
		t.Errorf("row defaults = level %q scope %q audience %q", a.Level, a.Scope, a.Audience)
	}
}
