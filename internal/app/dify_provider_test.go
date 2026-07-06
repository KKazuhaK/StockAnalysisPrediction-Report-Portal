package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// difyRunStub returns a workflow-run response with the given status, or a status code
// to force an HTTP error.
func difyRunStub(t *testing.T, runStatus string, httpCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if httpCode != 0 {
			w.WriteHeader(httpCode)
			w.Write([]byte(`{"code":"x","message":"boom"}`))
			return
		}
		w.Write([]byte(`{"workflow_run_id":"run-9","data":{"status":"` + runStatus + `","error":"detail","outputs":{}}}`))
	}))
}

func TestDifyProviderStatusMapping(t *testing.T) {
	cases := map[string]batch.Outcome{"succeeded": batch.Ok, "failed": batch.Failed, "stopped": batch.Failed}
	for status, want := range cases {
		s := difyRunStub(t, status, 0)
		p := difyProvider{c: dify.New(s.URL, "app-key", s.Client())}
		res, err := p.Run(context.Background(), map[string]string{"symbol": "600160"})
		s.Close()
		if err != nil {
			t.Fatalf("status %s: unexpected err %v", status, err)
		}
		if res.Status != want || res.RunID != "run-9" {
			t.Fatalf("status %s → %v (run %q), want %v", status, res.Status, res.RunID, want)
		}
	}
}

func TestDifyProviderErrorClassification(t *testing.T) {
	// 4xx (not 429) is permanent; 5xx is transient.
	s4 := difyRunStub(t, "", http.StatusBadRequest)
	defer s4.Close()
	_, err := difyProvider{c: dify.New(s4.URL, "k", s4.Client())}.Run(context.Background(), nil)
	if err == nil || batch.IsTransient(err) {
		t.Fatalf("4xx should be permanent, got transient=%v err=%v", batch.IsTransient(err), err)
	}

	s5 := difyRunStub(t, "", http.StatusBadGateway)
	defer s5.Close()
	_, err = difyProvider{c: dify.New(s5.URL, "k", s5.Client())}.Run(context.Background(), nil)
	if err == nil || !batch.IsTransient(err) {
		t.Fatalf("5xx should be transient, got transient=%v err=%v", batch.IsTransient(err), err)
	}
}

func TestBuildDifyProviderAndInputs(t *testing.T) {
	cfg, _ := json.Marshal(difyTargetConfig{
		BaseURL: "https://dify.example/v1", APIKey: "app-key",
		Inputs: []dify.Input{{Variable: "symbol", Label: "上市公司代码", Type: "text-input", Required: true}},
	})
	if _, err := buildDifyProvider(string(cfg), "report-portal"); err != nil {
		t.Fatalf("buildDifyProvider: %v", err)
	}
	if _, err := buildDifyProvider(`{"base_url":"","api_key":""}`, ""); err == nil {
		t.Fatal("expected error for missing base_url/api_key")
	}

	// The run form gets {key,label,required} from the stored inputs.
	got := difyInputsJSON(string(cfg))
	if len(got) != 1 || got[0]["key"] != "symbol" || got[0]["required"] != true {
		t.Fatalf("difyInputsJSON = %v", got)
	}
}

// difyEndUser resolves the recorded end-user from the dify_end_user template:
// the fixed default, [username] substitution, and a blank-template fallback.
func TestDifyEndUserTemplate(t *testing.T) {
	s := batchServer(t)
	if got := s.difyEndUser("kazuha"); got != "report-portal" {
		t.Errorf("default = %q, want report-portal", got)
	}
	s.st.SetSetting("dify_end_user", "[username]@anchan.kazuha.org")
	if got := s.difyEndUser("kazuha"); got != "kazuha@anchan.kazuha.org" {
		t.Errorf("templated = %q, want kazuha@anchan.kazuha.org", got)
	}
	s.st.SetSetting("dify_end_user", "   ") // blank falls back to the fixed default
	if got := s.difyEndUser("kazuha"); got != "report-portal" {
		t.Errorf("blank template = %q, want report-portal", got)
	}
}

// The provider forwards its resolved end-user to Dify as the run's `user`.
func TestDifyProviderSendsEndUser(t *testing.T) {
	var gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		gotUser, _ = body["user"].(string)
		io.WriteString(w, `{"workflow_run_id":"r","data":{"status":"succeeded"}}`)
	}))
	defer srv.Close()
	p := difyProvider{c: dify.New(srv.URL, "k", srv.Client()), user: "kazuha@anchan.kazuha.org"}
	if _, err := p.Run(context.Background(), map[string]string{"symbol": "1"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotUser != "kazuha@anchan.kazuha.org" {
		t.Errorf("recorded user = %q, want kazuha@anchan.kazuha.org", gotUser)
	}
}
