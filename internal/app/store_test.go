package app

import (
	"testing"
)

// newTestStore opens a fresh in-memory sqlite Store. In-memory (rather than a temp-dir
// file) means there's no directory for t.TempDir to RemoveAll while the just-closed
// sqlite connection is still releasing its files — a race that flaked on Linux CI with
// "directory not empty". OpenStore sets MaxOpenConns=1, so every query shares the one
// connection's in-memory database, and separate stores stay isolated.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// Links carry an optional icon name (chosen in the admin UI) that must survive
// add + edit round-trips.
func TestLinkIconRoundTrip(t *testing.T) {
	st := newTestStore(t)

	if err := st.AddLink("GitHub", "https://github.com", "github", true, 0, 0); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	ls := st.Links()
	if len(ls) != 1 {
		t.Fatalf("Links len = %d, want 1", len(ls))
	}
	if ls[0].Icon != "github" || !ls[0].NewTab || ls[0].GroupID != 0 {
		t.Errorf("got Icon=%q NewTab=%v GroupID=%v, want github/true/0", ls[0].Icon, ls[0].NewTab, ls[0].GroupID)
	}

	// Editing label/URL/icon/newTab preserves the row and updates the fields.
	if err := st.UpdateLinkFields(ls[0].ID, "GH", "https://gh.io", "book", false); err != nil {
		t.Fatalf("UpdateLinkFields: %v", err)
	}
	ls = st.Links()
	if len(ls) != 1 {
		t.Fatalf("Links len = %d, want 1", len(ls))
	}
	if ls[0].Icon != "book" || ls[0].Label != "GH" || ls[0].URL != "https://gh.io" || ls[0].NewTab {
		t.Errorf("after edit = %+v, want {Label:GH URL:https://gh.io Icon:book NewTab:false}", ls[0])
	}
}

// Entry-button groups: create/list/update, assign links to a group + order, and deleting a
// group returns its links to the top level (ungrouped).
func TestLinkGroups(t *testing.T) {
	st := newTestStore(t)
	gid, err := st.AddLinkGroup("决策分析", "popover", true, "bulb", 0)
	if err != nil {
		t.Fatalf("AddLinkGroup: %v", err)
	}
	gs := st.LinkGroups()
	if len(gs) != 1 || gs[0].Mode != "popover" || !gs[0].ShowLabel || gs[0].Icon != "bulb" {
		t.Fatalf("groups = %+v, want one popover/show-label/bulb group", gs)
	}
	if err := st.UpdateLinkGroup(gid, "决策", "modal", false, "rocket"); err != nil {
		t.Fatalf("UpdateLinkGroup: %v", err)
	}
	if gs = st.LinkGroups(); gs[0].Name != "决策" || gs[0].Mode != "modal" || gs[0].ShowLabel || gs[0].Icon != "rocket" {
		t.Fatalf("after update = %+v, want 决策/modal/no-label/rocket", gs[0])
	}

	// Two links; assign one into the group, keep one top-level.
	st.AddLink("A", "https://a", "", true, 0, 0)
	st.AddLink("B", "https://b", "", true, 0, 1)
	ls := st.Links()
	st.SetLinkGroupAndOrder(ls[1].ID, gid, 0)
	byID := map[int64]Link{}
	for _, l := range st.Links() {
		byID[l.ID] = l
	}
	if byID[ls[0].ID].GroupID != 0 || byID[ls[1].ID].GroupID != gid {
		t.Fatalf("group assignment wrong: %+v", st.Links())
	}

	// Deleting the group orphans its links back to the top level.
	if err := st.DeleteLinkGroup(gid); err != nil {
		t.Fatalf("DeleteLinkGroup: %v", err)
	}
	if len(st.LinkGroups()) != 0 {
		t.Fatal("group still present after delete")
	}
	for _, l := range st.Links() {
		if l.GroupID != 0 {
			t.Fatalf("link %d still in a deleted group (group_id=%d)", l.ID, l.GroupID)
		}
	}
}

// A link added without an icon reads back with an empty icon (not an error), so
// the frontend can fall back to the default link glyph.
func TestLinkEmptyIcon(t *testing.T) {
	st := newTestStore(t)
	if err := st.AddLink("Docs", "https://docs", "", true, 0, 0); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	ls := st.Links()
	if len(ls) != 1 || ls[0].Icon != "" {
		t.Fatalf("links = %+v, want one link with empty icon", ls)
	}
}
