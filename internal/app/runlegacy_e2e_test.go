package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestRunLegacyImportE2E drives the exact function the `import-legacy` CLI calls
// (RunLegacyImport): it loads config, opens the store, reads the old-portal
// credentials from Settings, and runs the import against a mock old system. This
// covers the full plumbing the direct-wiring E2E test skips.
func TestRunLegacyImportE2E(t *testing.T) {
	reports := []OldRaw{
		{ID: 201, Title: "策略月报", Category: "策略", Author: "赵六", Time: "2026-06-05 09:00:00", ReportDate: "2026-06-05", StockCode: "000001"},
		{ID: 202, Title: "行业纵览", Category: "行业", Author: "钱七", Time: "2026-06-06 09:00:00", ReportDate: "2026-06-06", StockCode: ""},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/token":
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		case r.URL.Path == "/api/reports":
			if r.URL.Query().Get("page") == "1" {
				json.NewEncoder(w).Encode(map[string]any{"reports": reports, "total": len(reports)})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"reports": []OldRaw{}, "total": len(reports)})
			}
		default: // /api/reports/{id}
			json.NewEncoder(w).Encode(OldDetail{Content: "body-" + r.URL.Path, ContentHTML: "<p>x</p>"})
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	dbPath := filepath.Join(dir, "portal.db")
	os.WriteFile(cfgPath, []byte("secret_key: k\ndb_driver: sqlite\ndb_path: "+dbPath+"\n"), 0o644)

	// Seed the legacy credentials into the DB (as the admin Settings page would), then
	// release the file so RunLegacyImport can open it.
	seed, err := OpenStore("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	seed.SetSetting("old_base", srv.URL)
	seed.SetSetting("old_user", "u")
	seed.SetSetting("old_pass", "p")
	seed.Close()

	imported, failed, failedIDs, err := RunLegacyImport(cfgPath, nil)
	if err != nil {
		t.Fatalf("RunLegacyImport: %v", err)
	}
	if imported != 2 || failed != 0 || len(failedIDs) != 0 {
		t.Fatalf("imported=%d failed=%d failedIDs=%v, want 2/0/[]", imported, failed, failedIDs)
	}

	// Verify the rows actually landed.
	st, err := OpenStore("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	if n := st.CountNew(); n != 2 {
		t.Errorf("reports count = %d, want 2", n)
	}
	var src, md string
	st.queryRow("SELECT source,body_md FROM reports WHERE uid=?", "legacy|201").Scan(&src, &md)
	if src != "legacy" || md == "" {
		t.Errorf("legacy|201 = src%q md%q, want source=legacy with a non-empty body", src, md)
	}
}
