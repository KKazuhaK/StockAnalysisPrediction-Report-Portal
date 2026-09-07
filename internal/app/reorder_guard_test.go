package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// Reordering is not a creating operation, and it is not a moving-into-nowhere operation.
//
// Both of these came from the same shape: the browser posts the list it last read, so anything
// deleted since is still in the payload. The handlers took it at face value — one upsert put a
// deleted report type back, the other stamped entry buttons with a group id nothing resolves. Both
// are races rather than mistakes, so both are absorbed rather than rejected: the rows that still
// exist keep the order the admin just made.

func reorderFixture(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		names: LoadNames(t.TempDir(), st)}
	s.names.fetch = func(string) string { return "" }
	return s
}

func reorderPost(t *testing.T, h func(http.ResponseWriter, *http.Request, string), body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)), "admin")
	return rec
}

func TestReorderingTypesCannotRecreateADeletedOne(t *testing.T) {
	s := reorderFixture(t)
	// Two types an admin configured, one of which has no reports — the kind that vanishes entirely
	// on delete, and so the kind a stale drag can bring back.
	s.st.UpsertTypeConfig("深度分析", "深度研究", "深度分析", 0, false)
	s.st.UpsertTypeConfig("已废弃的类型", "深度研究", "已废弃的类型", 1, false)
	s.st.DeleteTypeConfig("已废弃的类型")

	// The browser still holds the pre-delete list and drags it.
	rec := reorderPost(t, s.apiTypesReorder, `{"names":["已废弃的类型","深度分析"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, n := range s.st.DiscoveredTypes() {
		if n == "已废弃的类型" {
			t.Fatal("the deleted type came back — and deleting it again would only hold until the next drag")
		}
	}
	// The rows that DO still exist keep the order the admin just made, rather than the whole save
	// failing because one row of it was stale.
	if got := s.st.TypeConfigs()["深度分析"].Ord; got != 0 {
		t.Fatalf("surviving type's order = %d, want 0", got)
	}
	var out struct {
		Dropped []string `json:"dropped"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Dropped) != 1 || out.Dropped[0] != "已废弃的类型" {
		t.Fatalf("dropped = %v", out.Dropped)
	}
}

// A type that reports carry but nobody configured has no row to update, so the upsert has to keep
// creating one for it — the behaviour the guard must not break.
func TestReorderingStillGivesADiscoveredTypeAnOrder(t *testing.T) {
	s := reorderFixture(t)
	if _, _, err := s.st.UpsertReport(Rep{
		Symbol: "600519", Date: "2026-09-02", RType: "从未配置过的类型", Title: "t"}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if rec := reorderPost(t, s.apiTypesReorder, `{"names":["从未配置过的类型"]}`); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := s.st.TypeConfigs()["从未配置过的类型"]; !ok {
		t.Fatal("a discovered type got no order row, so dragging it does nothing")
	}
}

func TestDraggingIntoADeletedGroupSendsButtonsToTheTopLevel(t *testing.T) {
	s := reorderFixture(t)
	gid, err := s.st.AddLinkGroup("研究", "row", true, "", 0)
	if err != nil {
		t.Fatalf("add group: %v", err)
	}
	if err := s.st.AddLink("入口", "https://example.test", "", true, gid, 0); err != nil {
		t.Fatalf("add link: %v", err)
	}
	var lid int64
	for _, l := range s.st.Links() {
		lid = l.ID
	}
	s.st.DeleteLinkGroup(gid)

	// The browser still holds the group header and drags the button under it.
	rec := reorderPost(t, s.apiLinkLayout, fmt.Sprintf(
		`{"items":[{"kind":"group","id":%d},{"kind":"link","id":%d}]}`, gid, lid))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Top level, which is exactly where DeleteLinkGroup already puts a deleted group's links —
	// rather than a group id nothing resolves, which hides the button from the home page and from
	// the admin page at once, with no way back through the UI.
	var found bool
	for _, l := range s.st.Links() {
		if l.ID == lid {
			found = true
			if l.GroupID != 0 {
				t.Fatalf("button landed in group %d, which no longer exists", l.GroupID)
			}
		}
	}
	if !found {
		t.Fatal("the button disappeared entirely")
	}
	var out struct {
		Orphaned int `json:"orphanedGroups"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Orphaned != 1 {
		t.Fatalf("orphanedGroups = %d, want 1", out.Orphaned)
	}
}

// The ordinary drag still works: a live group keeps its buttons and both get their order.
func TestAnOrdinaryLinkDragStillGroupsAndOrders(t *testing.T) {
	s := reorderFixture(t)
	gid, _ := s.st.AddLinkGroup("研究", "row", true, "", 0)
	s.st.AddLink("甲", "https://a.test", "", true, 0, 0)
	s.st.AddLink("乙", "https://b.test", "", true, 0, 1)
	var a, b int64
	for _, l := range s.st.Links() {
		switch l.Label {
		case "甲":
			a = l.ID
		case "乙":
			b = l.ID
		}
	}

	rec := reorderPost(t, s.apiLinkLayout, fmt.Sprintf(
		`{"items":[{"kind":"link","id":%d},{"kind":"group","id":%d},{"kind":"link","id":%d}]}`, a, gid, b))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := map[int64]int64{}
	for _, l := range s.st.Links() {
		got[l.ID] = l.GroupID
	}
	if got[a] != 0 || got[b] != gid {
		t.Fatalf("grouping = %v, want %d at top level and %d under %d", got, a, b, gid)
	}
}
