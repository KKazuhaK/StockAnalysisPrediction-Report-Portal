package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
)

func reportOwner(t *testing.T, st *Store, id int64) (int64, bool) {
	t.Helper()
	var ou sql.NullInt64
	if err := st.queryRow("SELECT owner_group FROM reports WHERE id=?", id).Scan(&ou); err != nil {
		t.Fatalf("read owner_group(%d): %v", id, err)
	}
	return ou.Int64, ou.Valid
}

// TestOwnerGroupOf resolves a user to the OU that owns their output: the primary group, or the
// Default group when unassigned (ADR 0022 R1).
func TestOwnerGroupOf(t *testing.T) {
	st := newTestStore(t)
	def := st.EnsureDefaultGroup()
	st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	if got := st.OwnerGroupOf("alice"); got != def {
		t.Fatalf("unassigned owner = %d, want default %d", got, def)
	}
	gid, err := st.CreateUserGroup("extco", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	st.SetPrimaryGroup("alice", gid)
	if got := st.OwnerGroupOf("alice"); got != gid {
		t.Fatalf("assigned owner = %d, want %d", got, gid)
	}
}

// TestStampReportOwnerFirstWriterWins proves the owner is stamped only while NULL, so a re-ingest
// or a second OU racing the same shared identity row never reassigns it.
func TestStampReportOwnerFirstWriterWins(t *testing.T) {
	st := newTestStore(t)
	id, _, err := st.UpsertReport(Rep{Symbol: "600519", Date: "2026-07-24", RType: "val", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if ou, ok := reportOwner(t, st, id); ok {
		t.Fatalf("fresh report should be unattributed, got owner %d", ou)
	}
	if stamped, err := st.StampReportOwner(id, 7); err != nil || !stamped {
		t.Fatalf("first stamp: stamped=%v err=%v", stamped, err)
	}
	if stamped, _ := st.StampReportOwner(id, 9); stamped {
		t.Fatal("second stamp must be a no-op (first-writer-wins)")
	}
	if ou, ok := reportOwner(t, st, id); !ok || ou != 7 {
		t.Fatalf("owner = %d (valid %v), want 7 (the first writer)", ou, ok)
	}
	// ou 0 is never stamped.
	id2, _, _ := st.UpsertReport(Rep{Symbol: "000001", Date: "2026-07-24", RType: "val", Title: "y"})
	if stamped, _ := st.StampReportOwner(id2, 0); stamped {
		t.Fatal("ou 0 should never stamp")
	}
}

// TestOwnerTokenRoundTrip locks the server-authoritative attribution token: it round-trips the OU,
// and a tampered / empty / foreign token is rejected so ownership can't be forged.
func TestOwnerTokenRoundTrip(t *testing.T) {
	s := &Server{cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	tok := s.mintOwnerToken(42, "asker")
	if tok == "" {
		t.Fatal("mintOwnerToken returned empty")
	}
	if ou, who, ok := s.ownerFromToken(tok); !ok || ou != 42 || who != "asker" {
		t.Fatalf("round-trip ou=%d ok=%v, want 42/true", ou, ok)
	}
	if _, _, ok := s.ownerFromToken(tok + "x"); ok {
		t.Fatal("tampered token accepted")
	}
	if _, _, ok := s.ownerFromToken(""); ok {
		t.Fatal("empty token accepted")
	}
	// A session cookie must not be usable as an owner token (different prefix).
	if _, _, ok := s.ownerFromToken(s.signUser(User{Username: "x"})); ok {
		t.Fatal("a session token was accepted as an owner token")
	}
	// ou 0 mints nothing.
	if s.mintOwnerToken(0, "") != "" {
		t.Fatal("ou 0 should mint no token")
	}
}

// TestV1IngestStampsOwnerFromToken proves ingest stamps owner_group from a valid owner_token, and
// leaves the report unattributed (NULL) when the token is absent or invalid.
func TestV1IngestStampsOwnerFromToken(t *testing.T) {
	s := newV1Server(t)
	s.cfg = &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}

	ingest := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-all")
		rec := httptest.NewRecorder()
		s.v1Ingest(rec, req)
		return rec
	}
	idOf := func(rec *httptest.ResponseRecorder) int64 {
		var m struct {
			ID int64 `json:"id"`
		}
		json.Unmarshal(rec.Body.Bytes(), &m)
		return m.ID
	}

	tok := s.mintOwnerToken(55, "asker")
	rec := ingest(`{"symbol":"300750","date":"2026-07-24","subtype":"val","owner_token":"` + tok + `"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest with token: code %d (%s)", rec.Code, rec.Body.String())
	}
	if ou, ok := reportOwner(t, s.st, idOf(rec)); !ok || ou != 55 {
		t.Fatalf("owner after token ingest = %d (valid %v), want 55", ou, ok)
	}

	// No token → unattributed.
	rec = ingest(`{"symbol":"000001","date":"2026-07-24","subtype":"val"}`)
	if ou, ok := reportOwner(t, s.st, idOf(rec)); ok {
		t.Fatalf("owner without token = %d, want NULL/unattributed", ou)
	}

	// Tampered token → unattributed (never trust a bad token).
	rec = ingest(`{"symbol":"600000","date":"2026-07-24","subtype":"val","owner_token":"garbage.sig"}`)
	if ou, ok := reportOwner(t, s.st, idOf(rec)); ok {
		t.Fatalf("owner with tampered token = %d, want NULL", ou)
	}
}
