package app

import "testing"

// eastmoney sometimes pads Chinese company names with spaces (leading/trailing/internal,
// e.g. "柳    工" for 柳工) — a real name never contains any, so cleanName strips all of it.
func TestCleanName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"柳    工", "柳工"},
		{"新 大 陆", "新大陆"},
		{"  红宝丽  ", "红宝丽"},
		{"柳工", "柳工"},
		{"", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := cleanName(c.in); got != c.want {
			t.Errorf("cleanName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// mergeDiff parses eastmoney's clist response; a padded f14 name must come out clean so it
// never reaches the stocks table or a frozen report name.
func TestMergeDiffCleansNames(t *testing.T) {
	body := []byte(`{"data":{"diff":[{"f12":"000528","f14":"柳    工"},{"f12":"000001","f14":"平安银行"}]}}`)
	m := map[string]string{}
	n := mergeDiff(body, m)
	if n != 2 {
		t.Fatalf("mergeDiff parsed %d entries, want 2", n)
	}
	if m["000528"] != "柳工" {
		t.Errorf("m[000528] = %q, want 柳工", m["000528"])
	}
	if m["000001"] != "平安银行" {
		t.Errorf("m[000001] = %q, want 平安银行", m["000001"])
	}
}

// SyncStocks is the shared choke point every name source (bulk fetch, live single fetch,
// a stale on-disk names.json) writes through — it must clean on the way in regardless of
// whether the caller already did, so a dirty source can never survive a resync.
func TestSyncStocksCleansNames(t *testing.T) {
	st := newTestStore(t)
	st.SyncStocks(map[string]string{"000528": "柳    工"})
	got := st.AllStockNames()
	if got["000528"] != "柳工" {
		t.Errorf("stocks.name after SyncStocks = %q, want 柳工", got["000528"])
	}
}
