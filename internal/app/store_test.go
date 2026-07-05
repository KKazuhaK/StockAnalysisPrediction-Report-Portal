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

	if err := st.AddLink("GitHub", "https://github.com", "github", true, 0); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	ls := st.Links()
	if len(ls) != 1 {
		t.Fatalf("Links len = %d, want 1", len(ls))
	}
	if ls[0].Icon != "github" || !ls[0].NewTab {
		t.Errorf("got Icon=%q NewTab=%v, want github/true", ls[0].Icon, ls[0].NewTab)
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

// A link added without an icon reads back with an empty icon (not an error), so
// the frontend can fall back to the default link glyph.
func TestLinkEmptyIcon(t *testing.T) {
	st := newTestStore(t)
	if err := st.AddLink("Docs", "https://docs", "", true, 0); err != nil {
		t.Fatalf("AddLink: %v", err)
	}
	ls := st.Links()
	if len(ls) != 1 || ls[0].Icon != "" {
		t.Fatalf("links = %+v, want one link with empty icon", ls)
	}
}
