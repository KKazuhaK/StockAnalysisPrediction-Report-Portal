package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// callPath calls a cookie-session handler directly with the path values the mux would have
// populated, so handlers that read r.PathValue can be exercised without a live server.
func callPath(t *testing.T, h handler, method, body string, path map[string]string, actor string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	for k, v := range path {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req, actor)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// Entry-button links: add through the handler defaults to new-tab and appends, edit is a full
// field update, delete removes, and the admin GET lists what the store holds.
func TestEntryLinkAdminHandlersCRUD(t *testing.T) {
	s := userAdminServer(t)

	if code, _ := call(t, s.apiLinkAdd, `{"label":" GitHub ","url":" https://github.com ","icon":" github "}`, "admin"); code != http.StatusOK {
		t.Fatalf("apiLinkAdd → %d", code)
	}
	if code, _ := call(t, s.apiLinkAdd, `{"label":"Docs","url":"https://docs.example","newTab":false}`, "admin"); code != http.StatusOK {
		t.Fatalf("apiLinkAdd second → %d", code)
	}
	ls := s.st.Links()
	if len(ls) != 2 {
		t.Fatalf("links = %d, want 2", len(ls))
	}
	// Inputs are trimmed; an omitted newTab means open in a new tab; the first link starts at
	// ord 0 and the second appends after it, ungrouped.
	if ls[0].Label != "GitHub" || ls[0].URL != "https://github.com" || ls[0].Icon != "github" || !ls[0].NewTab || ls[0].Ord != 0 {
		t.Fatalf("first link = %+v", ls[0])
	}
	if ls[1].NewTab || ls[1].Ord != 1 || ls[1].GroupID != 0 {
		t.Fatalf("second link = %+v", ls[1])
	}

	code, out := call(t, s.apiAdminLinks, ``, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiAdminLinks → %d", code)
	}
	if got, ok := out["links"].([]any); !ok || len(got) != 2 {
		t.Fatalf("admin links payload = %#v", out["links"])
	}

	// Edit carries every field: rename, point elsewhere, unset new-tab and hide.
	id := ls[0].ID
	code, _ = callPath(t, s.apiLinkEdit, http.MethodPost, `{"label":"GH","url":"https://gh.io","icon":"book","newTab":false,"visible":false}`,
		map[string]string{"id": fmt.Sprint(id)}, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiLinkEdit → %d", code)
	}
	got := s.st.Links()[0]
	if got.Label != "GH" || got.URL != "https://gh.io" || got.Icon != "book" || got.NewTab || got.Visible {
		t.Fatalf("edited link = %+v", got)
	}
	// An omitted visible defaults back to visible: the partial-update contract the password
	// reset modal and the edit form share.
	code, _ = callPath(t, s.apiLinkEdit, http.MethodPost, `{"label":"GH2","url":"https://gh.io","icon":"book","newTab":false}`,
		map[string]string{"id": fmt.Sprint(id)}, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiLinkEdit partial → %d", code)
	}
	if got := s.st.Links()[0]; !got.Visible || got.Label != "GH2" {
		t.Fatalf("partial edit = %+v, want visible again", got)
	}

	if code, _ = callPath(t, s.apiLinkDelete, http.MethodPost, ``, map[string]string{"id": fmt.Sprint(id)}, "admin"); code != http.StatusOK {
		t.Fatalf("apiLinkDelete → %d", code)
	}
	if ls = s.st.Links(); len(ls) != 1 || ls[0].Label != "Docs" {
		t.Fatalf("links after delete = %+v", ls)
	}
}

// Entry-button groups: an unknown display mode normalizes to the default row, show-label
// defaults on, edits apply, and deleting a group returns its links to the top level.
func TestEntryLinkGroupAdminHandlersCRUD(t *testing.T) {
	s := userAdminServer(t)

	code, out := call(t, s.apiLinkGroupAdd, `{"name":" 研报 ","mode":"drawer","icon":" bulb "}`, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiLinkGroupAdd → %d", code)
	}
	gid := int64(out["id"].(float64))
	gs := s.st.LinkGroups()
	if len(gs) != 1 {
		t.Fatalf("groups = %d, want 1", len(gs))
	}
	if gs[0].ID != gid || gs[0].Name != "研报" || gs[0].Mode != "row" || !gs[0].ShowLabel || gs[0].Icon != "bulb" || !gs[0].Visible {
		t.Fatalf("group = %+v, want drawer normalized to row with defaults", gs[0])
	}

	// Put a link inside, then edit the group to a real mode and hide it.
	if err := s.st.AddLink("A", "https://a", "", true, gid, 0); err != nil {
		t.Fatal(err)
	}
	code, _ = callPath(t, s.apiLinkGroupEdit, http.MethodPost, `{"name":"研报","mode":"popover","showLabel":false,"visible":false}`,
		map[string]string{"id": fmt.Sprint(gid)}, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiLinkGroupEdit → %d", code)
	}
	g := s.st.LinkGroups()[0]
	if g.Mode != "popover" || g.ShowLabel || g.Visible {
		t.Fatalf("edited group = %+v", g)
	}

	// Deleting the group moves its link to the top level rather than orphaning it.
	if code, _ = callPath(t, s.apiLinkGroupDelete, http.MethodPost, ``, map[string]string{"id": fmt.Sprint(gid)}, "admin"); code != http.StatusOK {
		t.Fatalf("apiLinkGroupDelete → %d", code)
	}
	if len(s.st.LinkGroups()) != 0 {
		t.Fatal("group survived delete")
	}
	if ls := s.st.Links(); len(ls) != 1 || ls[0].GroupID != 0 {
		t.Fatalf("link after group delete = %+v, want back at top level", ls)
	}
}

// Report types: add infers the top-level kind when omitted, save propagates a kind change to
// already-stored reports, delete drops the config row, and recompute re-derives from the config.
func TestTypeAdminHandlersAddSaveDeleteRecompute(t *testing.T) {
	s := userAdminServer(t)

	// An unknown type infers 未分类; a 重组 keyword infers 重组决策.
	if code, _ := call(t, s.apiTypesAdd, `{"name":"盘前提示"}`, "admin"); code != http.StatusOK {
		t.Fatalf("apiTypesAdd → %d", code)
	}
	if code, _ := call(t, s.apiTypesAdd, `{"name":"重组跟进分析","label":"跟进"}`, "admin"); code != http.StatusOK {
		t.Fatalf("apiTypesAdd second → %d", code)
	}
	cfg := s.st.TypeConfigs()
	if cfg["盘前提示"].Kind != "每日金股" {
		t.Errorf("盘前提示 kind = %q, want 每日金股 (inferred)", cfg["盘前提示"].Kind)
	}
	if cfg["重组跟进分析"].Kind != "重组决策" || cfg["重组跟进分析"].Label != "跟进" {
		t.Errorf("重组跟进分析 = %+v", cfg["重组跟进分析"])
	}

	// A blank name is rejected.
	if code, _ := call(t, s.apiTypesAdd, `{"name":"   "}`, "admin"); code != http.StatusBadRequest {
		t.Errorf("apiTypesAdd with blank name → %d, want 400", code)
	}

	// Save updates label/summary and pushes a changed kind onto stored reports of that type.
	if _, _, err := s.st.UpsertReport(Rep{Title: "量化周报", Symbol: "600519", RType: "量化策略", Kind: "未分类", Date: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	code, _ := call(t, s.apiTypesSave, `{"rows":[{"name":"量化策略","label":"量策","kind":"技术分析","summary":true}]}`, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiTypesSave → %d", code)
	}
	c := s.st.TypeConfigs()["量化策略"]
	if c.Kind != "技术分析" || c.Label != "量策" || !c.IsSummary {
		t.Fatalf("saved config = %+v", c)
	}
	var kind string
	s.st.queryRow("SELECT kind FROM reports WHERE rtype='量化策略'").Scan(&kind)
	if kind != "技术分析" {
		t.Fatalf("stored report kind = %q, want propagated 技术分析", kind)
	}

	// Recompute re-derives from the config even after the stored row was written with a stale kind.
	if _, err := s.st.exec("UPDATE reports SET kind='未分类' WHERE rtype='量化策略'"); err != nil {
		t.Fatal(err)
	}
	code, out := call(t, s.apiTypesRecompute, `{}`, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiTypesRecompute → %d", code)
	}
	if updated := int(out["updated"].(float64)); updated != 1 {
		t.Fatalf("recompute updated = %d, want 1", updated)
	}
	s.st.queryRow("SELECT kind FROM reports WHERE rtype='量化策略'").Scan(&kind)
	if kind != "技术分析" {
		t.Fatalf("recomputed kind = %q, want 技术分析", kind)
	}

	// Delete removes the config row; the report data it classifies is untouched.
	if code, _ = callPath(t, s.apiTypesDelete, http.MethodPost, ``, map[string]string{"name": "量化策略"}, "admin"); code != http.StatusOK {
		t.Fatalf("apiTypesDelete → %d", code)
	}
	if _, ok := s.st.TypeConfigs()["量化策略"]; ok {
		t.Fatal("type config survived delete")
	}
	s.st.queryRow("SELECT kind FROM reports WHERE rtype='量化策略'").Scan(&kind)
	if kind != "技术分析" {
		t.Fatalf("delete touched report data: kind = %q", kind)
	}
}
