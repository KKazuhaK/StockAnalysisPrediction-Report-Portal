package app

import "testing"

// htmlOf renders HTML from MD on demand when the HTML column wasn't persisted
// (new reports only store MD), but trusts a stored HTML value when present
// (legacy imports that have HTML and no MD).
func TestHtmlOfDerivesFromMD(t *testing.T) {
	got := htmlOf(Rep{MD: "# hi"})
	if got != "<h1>hi</h1>\n" {
		t.Errorf("htmlOf(md-only) = %q, want rendered heading", got)
	}
}

func TestHtmlOfPrefersStoredHTML(t *testing.T) {
	got := htmlOf(Rep{MD: "# hi", HTML: "<p>legacy</p>"})
	if got != "<p>legacy</p>" {
		t.Errorf("htmlOf(with stored HTML) = %q, want the stored value untouched", got)
	}
}

func TestHtmlOfEmptyReport(t *testing.T) {
	if got := htmlOf(Rep{}); got != "" {
		t.Errorf("htmlOf(empty) = %q, want empty", got)
	}
}

// htmlToStore governs what ingest persists: MD is authoritative, so HTML is only ever
// stored for a true HTML-only (legacy-import) submission — sending both drops the HTML.
func TestHtmlToStore(t *testing.T) {
	cases := []struct{ md, html, want string }{
		{md: "# hi", html: "", want: ""},                       // md-only → nothing to store
		{md: "", html: "<p>legacy</p>", want: "<p>legacy</p>"}, // html-only (legacy import) → kept
		{md: "# hi", html: "<p>drop me</p>", want: ""},         // both → md wins, html dropped
		{md: "", html: "", want: ""},                           // neither → nothing
	}
	for _, c := range cases {
		if got := htmlToStore(c.md, c.html); got != c.want {
			t.Errorf("htmlToStore(md=%q, html=%q) = %q, want %q", c.md, c.html, got, c.want)
		}
	}
}
