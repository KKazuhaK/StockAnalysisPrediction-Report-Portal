package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

func in(v string, req bool) dify.Input {
	return dify.Input{Variable: v, Required: req, Type: "text-input"}
}

// The diff is what an admin reads before agreeing to change how every future run of a target is
// submitted, so each kind of change has to be named separately: "it differs" is not a thing anybody
// can approve.
func TestDiffInputsNamesEachKindOfChange(t *testing.T) {
	cases := []struct {
		name       string
		local      []dify.Input
		remote     []dify.Input
		added      []string
		removed    []string
		reqChanged []string
		reordered  bool
		changed    bool
	}{
		{
			name:   "identical",
			local:  []dify.Input{in("symbol", true), in("report_date", false)},
			remote: []dify.Input{in("symbol", true), in("report_date", false)},
		},
		{
			name:    "a new parameter",
			local:   []dify.Input{in("symbol", true)},
			remote:  []dify.Input{in("symbol", true), in("rumor", false)},
			added:   []string{"rumor"},
			changed: true,
		},
		{
			name:    "a parameter is gone",
			local:   []dify.Input{in("symbol", true), in("rumor", false)},
			remote:  []dify.Input{in("symbol", true)},
			removed: []string{"rumor"},
			changed: true,
		},
		{
			name:       "an optional parameter became required",
			local:      []dify.Input{in("symbol", true), in("report_date", false)},
			remote:     []dify.Input{in("symbol", true), in("report_date", true)},
			reqChanged: []string{"report_date"},
			changed:    true,
		},
		{
			// The column order is the order the batch console shows, so a swap upstream is a real
			// change even though the set is identical. It is reported apart from the rest: an
			// admin who sees only "reordered" can approve it without reading a list.
			name:      "same parameters in a different order",
			local:     []dify.Input{in("symbol", true), in("report_date", false)},
			remote:    []dify.Input{in("report_date", false), in("symbol", true)},
			reordered: true,
			changed:   true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := diffInputs(c.local, c.remote)
			eq := func(what string, got, want []string) {
				t.Helper()
				if strings.Join(got, ",") != strings.Join(want, ",") {
					t.Errorf("%s = %v, want %v", what, got, want)
				}
			}
			eq("added", d.Added, c.added)
			eq("removed", d.Removed, c.removed)
			eq("requiredChanged", d.RequiredChanged, c.reqChanged)
			if d.Reordered != c.reordered {
				t.Errorf("reordered = %v, want %v", d.Reordered, c.reordered)
			}
			if d.Changed != c.changed {
				t.Errorf("changed = %v, want %v", d.Changed, c.changed)
			}
		})
	}
}

// A refresh writes the parameter list. If the parameter carrying the stock code disappears from it,
// same-day reuse (ADR 0022) silently stops working — the target keeps running, it just regenerates
// a report it already has. That has to be said out loud, not left for somebody to notice in a bill.
func TestRefreshFlagsALostSymbolInput(t *testing.T) {
	d := diffInputs([]dify.Input{in("symbol", true), in("report_date", false)}, []dify.Input{in("code", true)})
	if !contains(d.Removed, "symbol") {
		t.Fatalf("removed = %v, want it to contain symbol", d.Removed)
	}
	if lost := symbolInputLost("symbol", []dify.Input{in("code", true)}); lost != "symbol" {
		t.Errorf("symbolInputLost = %q, want %q", lost, "symbol")
	}
	if lost := symbolInputLost("symbol", []dify.Input{in("symbol", true)}); lost != "" {
		t.Errorf("a symbol input that is still there was reported lost: %q", lost)
	}
	// Not configured at all is not a loss.
	if lost := symbolInputLost("", []dify.Input{in("code", true)}); lost != "" {
		t.Errorf("an unset symbol input was reported lost: %q", lost)
	}
}

// The whole point of the feature, and the reason apply takes no name at all: a refresh pulls the
// parameter list and NOTHING else. The name an admin chose, the report type, the stock-code column
// and the credential are local decisions that Dify has no say in.
func TestRefreshApplyWritesOnlyTheInputs(t *testing.T) {
	s := tenancyServer(t)
	cfg, _ := json.Marshal(difyTargetConfig{
		BaseURL: "https://dify.example/v1", APIKey: "app-secret", Mode: "workflow",
		Inputs: []dify.Input{in("symbol", true)}, OutputSubtype: "估值分析", SymbolInput: "symbol",
	})
	id, err := s.st.CreateTarget(difyPluginSlug, "我起的名字", string(cfg))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := `{"items":[{"id":` + itoa64(id) + `,"mode":"workflow","inputs":[{"variable":"symbol","required":true},{"variable":"rumor"}]}]}`
	req := httptest.NewRequest("POST", "/api/admin/batch/dify/refresh/apply", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.apiBatchDifyRefreshApply(rr, req, "admin")
	if rr.Code != 200 {
		t.Fatalf("apply: %d %s", rr.Code, rr.Body.String())
	}

	tgt, ok := s.st.GetTarget(id)
	if !ok {
		t.Fatal("target vanished")
	}
	if tgt.Name != "我起的名字" {
		t.Errorf("the target was renamed to %q", tgt.Name)
	}
	var got difyTargetConfig
	if err := json.Unmarshal([]byte(tgt.Config), &got); err != nil {
		t.Fatalf("config: %v", err)
	}
	if len(got.Inputs) != 2 || got.Inputs[1].Variable != "rumor" {
		t.Errorf("inputs = %+v, want the refreshed pair", got.Inputs)
	}
	if got.OutputSubtype != "估值分析" || got.SymbolInput != "symbol" {
		t.Errorf("tenancy declarations were rewritten: subtype=%q symbol=%q", got.OutputSubtype, got.SymbolInput)
	}
	if got.APIKey != "app-secret" || got.BaseURL != "https://dify.example/v1" {
		t.Errorf("the address or credential was rewritten: %+v", got)
	}
}

// An empty parameter list is what a probe returns when Dify answered but /parameters did not, and
// writing it would wipe a list the admin may have built by hand. Refusing costs nothing: a workflow
// with genuinely no inputs has nothing to refresh either.
func TestRefreshApplyRefusesToEmptyTheInputs(t *testing.T) {
	s := tenancyServer(t)
	cfg, _ := json.Marshal(difyTargetConfig{BaseURL: "https://dify.example/v1", APIKey: "app-secret",
		Inputs: []dify.Input{in("symbol", true)}})
	id, _ := s.st.CreateTarget(difyPluginSlug, "t", string(cfg))

	req := httptest.NewRequest("POST", "/api/admin/batch/dify/refresh/apply",
		strings.NewReader(`{"items":[{"id":`+itoa64(id)+`,"inputs":[]}]}`))
	rr := httptest.NewRecorder()
	s.apiBatchDifyRefreshApply(rr, req, "admin")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("apply = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	tgt, _ := s.st.GetTarget(id)
	var got difyTargetConfig
	json.Unmarshal([]byte(tgt.Config), &got)
	if len(got.Inputs) != 1 {
		t.Errorf("the stored inputs were emptied anyway: %+v", got.Inputs)
	}
}
