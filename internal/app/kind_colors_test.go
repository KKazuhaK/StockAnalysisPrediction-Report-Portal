package app

import (
	"net/http"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

// KindColors lets an admin assign an antd Tag preset color to each report kind
// (大类), replacing the frontend's previously-hardcoded KIND_COLORS map.
func TestKindColorsStoreCRUD(t *testing.T) {
	st := newTestStore(t)
	if got := st.KindColors(); len(got) != 0 {
		t.Fatalf("KindColors on a fresh store = %v, want empty", got)
	}
	if err := st.SetKindColor("投资决策", "blue"); err != nil {
		t.Fatalf("SetKindColor: %v", err)
	}
	if err := st.SetKindColor("深度研究", "geekblue"); err != nil {
		t.Fatalf("SetKindColor: %v", err)
	}
	got := st.KindColors()
	if got["投资决策"] != "blue" || got["深度研究"] != "geekblue" {
		t.Fatalf("KindColors = %v", got)
	}
	// Re-setting the same kind upserts (overwrites), it doesn't add a second row.
	if err := st.SetKindColor("投资决策", "volcano"); err != nil {
		t.Fatalf("SetKindColor (overwrite): %v", err)
	}
	got = st.KindColors()
	if got["投资决策"] != "volcano" {
		t.Fatalf("KindColors[投资决策] = %q after overwrite, want volcano", got["投资决策"])
	}
	if len(got) != 2 {
		t.Fatalf("KindColors has %d entries after overwrite, want 2: %v", len(got), got)
	}
}

// On a fresh database, the shipped default kind→color mapping (matching the
// portal's pipeline kinds) is seeded exactly once, mirroring seedDefaultTypes.
func TestSeedDefaultKindColors(t *testing.T) {
	st := newTestStore(t)
	n := seedDefaultKindColors(st)
	if n != len(defaultKindColors) {
		t.Fatalf("seedDefaultKindColors = %d, want %d", n, len(defaultKindColors))
	}
	got := st.KindColors()
	for _, c := range defaultKindColors {
		if got[c.Kind] != c.Color {
			t.Errorf("KindColors[%q] = %q, want %q", c.Kind, got[c.Kind], c.Color)
		}
	}
}

// POST /api/admin/kind-colors upserts a single kind's color.
func TestApiKindColorSave(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "test-secret"}}
	code, _ := call(t, s.apiKindColorSave, `{"kind":"投资决策","color":"volcano"}`, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiKindColorSave → %d", code)
	}
	if got := s.st.KindColors()["投资决策"]; got != "volcano" {
		t.Fatalf("KindColors[投资决策] = %q, want volcano", got)
	}

	// A blank kind is rejected.
	code, _ = call(t, s.apiKindColorSave, `{"kind":"","color":"volcano"}`, "admin")
	if code != http.StatusBadRequest {
		t.Fatalf("apiKindColorSave with blank kind → %d, want 400", code)
	}
}

// The Types admin page fetches groups/kinds and the configured colors from the
// same /api/admin/types response.
func TestApiAdminTypesIncludesColors(t *testing.T) {
	s := &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: "test-secret"}}
	s.st.SetKindColor("投资决策", "volcano")
	code, out := call(t, s.apiAdminTypes, `{}`, "admin")
	if code != http.StatusOK {
		t.Fatalf("apiAdminTypes → %d", code)
	}
	colors, ok := out["colors"].(map[string]any)
	if !ok {
		t.Fatalf("/api/admin/types response missing colors field: %v", out)
	}
	if colors["投资决策"] != "volcano" {
		t.Fatalf("colors[投资决策] = %v, want volcano", colors["投资决策"])
	}
}

// The home feed also returns kindColors so the browser can color each card's
// kind Tag without a second round-trip.
func TestApiHomeIncludesKindColors(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "test-secret"}, names: LoadNames(t.TempDir(), st)}
	s.st.SetKindColor("深度研究", "geekblue")
	code, out := call(t, s.apiHome, "", "admin")
	if code != http.StatusOK {
		t.Fatalf("apiHome → %d", code)
	}
	colors, ok := out["kindColors"].(map[string]any)
	if !ok {
		t.Fatalf("/api/home response missing kindColors field: %v", out)
	}
	if colors["深度研究"] != "geekblue" {
		t.Fatalf("kindColors[深度研究] = %v, want geekblue", colors["深度研究"])
	}
}
