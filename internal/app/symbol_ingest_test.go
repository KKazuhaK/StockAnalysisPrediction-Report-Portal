package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A workflow that cannot identify one company must say so by sending no symbol at all. It used to be
// able to say it with a placeholder instead: an upstream extractor returned free LLM text straight
// into this field, and nothing between there and the row checked its shape, so "..." and
// "1762827491370.symbol" became stock codes. They show up as a report filed under a company that
// does not exist, and as a ghost entry in the per-stock list.
//
// An EMPTY symbol stays valid and is not touched. Thematic and multi-stock research genuinely has no
// single holding, and those reports are meant to stand on their own — dropping a malformed symbol
// puts a report in exactly that state, which is the honest one.

func TestNormalizeSymbolKeepsOnlyWhatCouldBeACode(t *testing.T) {
	for name, tc := range map[string]struct {
		in          string
		want        string
		wantDropped bool
	}{
		"a plain code":                {"600498", "600498", false},
		"surrounding whitespace":      {"  600844 ", "600844", false},
		"an exchange suffix":          {"603688.SH", "603688", false},
		"a lowercase exchange suffix": {"605305.sz", "605305", false},
		"the Beijing exchange":        {"430139.BJ", "430139", false},
		"full-width digits":           {"６００８４４", "600844", false},
		// Empty is a legitimate answer, not a failure: it is what every thematic report carries.
		"absent":     {"", "", false},
		"whitespace": {"   ", "", false},
		// Every one of these was a real stored value.
		"a placeholder the model wrote":  {"...", "", true},
		"the variable name, echoed back": {"1762827491370.symbol", "", true},
		"two codes joined":               {"688539,688333", "", true},
		"a Hong Kong code":               {"01810", "", true},
		"the format description":         {"6位数字", "", true},
		"a company name":                 {"烽火通信", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			got, dropped := normalizeSymbol(tc.in)
			if got != tc.want || dropped != tc.wantDropped {
				t.Errorf("normalizeSymbol(%q) = (%q, %v), want (%q, %v)", tc.in, got, dropped, tc.want, tc.wantDropped)
			}
		})
	}
}

func TestIngestFilesAMalformedSymbolAsNoSymbolRatherThanRefusingIt(t *testing.T) {
	s := newV1Server(t)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok-all")
		rec := httptest.NewRecorder()
		s.v1Ingest(rec, req)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("response not JSON: %q", rec.Body.String())
		}
		return m
	}

	// Refusing would be worse than storing it: a 400 here means a finished report is lost behind a
	// status code nobody reads, which is why this endpoint deliberately accepts what it is given.
	// The report is kept; only the unusable symbol is dropped.
	rec := post(`{"symbol":"...","date":"2026-09-14","subtype":"综合深度研究","title":"烽火通信（600498）深度研究","body_md":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	m := decode(rec)
	if m["symbol"] != "" {
		t.Errorf("response symbol = %v, want the empty value actually stored", m["symbol"])
	}
	// The caller is told, because a silently emptied field is how this went unnoticed for months.
	if note, _ := m["note"].(string); !strings.Contains(note, "symbol") {
		t.Errorf("response note = %q, want it to name the dropped symbol", note)
	}
	if rep := repByIdent(t, s.st, "", "2026-09-14", "综合深度研究"); rep == nil {
		t.Fatal("report was not stored")
	} else if rep.Symbol != "" {
		t.Errorf("stored symbol = %q, want empty", rep.Symbol)
	}

	// A good code is untouched and reported back as itself.
	rec = post(`{"symbol":"603688.SH","date":"2026-09-14","subtype":"估值分析","title":"石英股份","body_md":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if m := decode(rec); m["symbol"] != "603688" {
		t.Errorf("response symbol = %v, want the normalized 603688", m["symbol"])
	}

	// With nothing else to identify it by, a malformed symbol leaves the request with neither a
	// symbol nor a title — which is the one case this endpoint already refuses.
	if rec := post(`{"symbol":"...","date":"2026-09-14","subtype":"综合深度研究","body_md":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("symbol-only-garbage status = %d, want 400", rec.Code)
	}
}
