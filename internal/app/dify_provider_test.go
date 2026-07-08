package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/dify"
)

// difyRunStub streams a workflow run ending in the given status, or returns a status
// code to force an HTTP error. The provider runs in streaming mode now.
func difyRunStub(t *testing.T, runStatus string, httpCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if httpCode != 0 {
			w.WriteHeader(httpCode)
			w.Write([]byte(`{"code":"x","message":"boom"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"event":"workflow_started","task_id":"t1","workflow_run_id":"run-9","data":{}}`+"\n\n")
		io.WriteString(w, `data: {"event":"workflow_finished","task_id":"t1","workflow_run_id":"run-9","data":{"status":"`+runStatus+`","error":"detail","outputs":{}}}`+"\n\n")
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
	if _, err := buildDifyProvider(string(cfg), "report-portal", false, 0, 0, nil, nil); err != nil {
		t.Fatalf("buildDifyProvider: %v", err)
	}
	if _, err := buildDifyProvider(`{"base_url":"","api_key":""}`, "", false, 0, 0, nil, nil); err == nil {
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
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"event":"workflow_finished","task_id":"t","workflow_run_id":"r","data":{"status":"succeeded"}}`+"\n\n")
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

// A stream that drops after the run started must NOT re-run the workflow: the
// provider reconciles the outcome by polling the run id (the duplicate-run fix).
func TestDifyProviderReconnectDoesNotRerun(t *testing.T) {
	var runs int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workflows/run":
			atomic.AddInt32(&runs, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			// Start the run, then drop the connection before workflow_finished.
			io.WriteString(w, `data: {"event":"workflow_started","task_id":"t1","workflow_run_id":"run-42","data":{}}`+"\n\n")
		case "/workflows/run/run-42":
			io.WriteString(w, `{"id":"run-42","status":"succeeded","outputs":{"uid":"x"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := difyProvider{c: dify.New(srv.URL, "k", srv.Client()), user: "u"}
	res, err := p.Run(context.Background(), map[string]string{"symbol": "1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != batch.Ok || res.RunID != "run-42" {
		t.Fatalf("res = %+v, want Ok run-42", res)
	}
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Errorf("workflow was started %d times, want 1 (no re-run on reconnect)", n)
	}
}

// Poll mode: the stream returns as soon as the run id is captured (no workflow_finished
// read), then the outcome is polled. The workflow is started exactly once (no re-run).
func TestDifyProviderPollMode(t *testing.T) {
	var runs, gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workflows/run":
			atomic.AddInt32(&runs, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"event":"workflow_started","task_id":"t","workflow_run_id":"run-p","data":{}}`+"\n\n")
			io.WriteString(w, `data: {"event":"workflow_finished","task_id":"t","workflow_run_id":"run-p","data":{"status":"succeeded"}}`+"\n\n")
		case "/workflows/run/run-p":
			atomic.AddInt32(&gets, 1)
			io.WriteString(w, `{"id":"run-p","status":"succeeded"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := difyProvider{c: dify.New(srv.URL, "k", srv.Client()), user: "u", poll: true, reconcilePoll: time.Millisecond, reconcileTimeout: 5 * time.Second}
	res, err := p.Run(context.Background(), map[string]string{"symbol": "1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != batch.Ok || res.RunID != "run-p" {
		t.Fatalf("res = %+v, want Ok run-p", res)
	}
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Errorf("workflow started %d times, want 1", n)
	}
	if atomic.LoadInt32(&gets) < 1 {
		t.Error("poll mode should have polled the run status at least once")
	}
}

// A transient blip on the reconcile poll (e.g. a 502 right after the drop) is
// retried within the deadline — the run reconciles to its real outcome and is not
// re-run.
func TestDifyProviderReconcileRetriesTransient(t *testing.T) {
	var runs, gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workflows/run":
			atomic.AddInt32(&runs, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"event":"workflow_started","task_id":"t","workflow_run_id":"run-7","data":{}}`+"\n\n")
		case "/workflows/run/run-7":
			if atomic.AddInt32(&gets, 1) == 1 {
				w.WriteHeader(http.StatusBadGateway) // transient blip on the first poll
				return
			}
			io.WriteString(w, `{"id":"run-7","status":"succeeded"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := difyProvider{c: dify.New(srv.URL, "k", srv.Client()), user: "u", reconcilePoll: time.Millisecond, reconcileTimeout: 5 * time.Second}
	res, err := p.Run(context.Background(), map[string]string{"symbol": "1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != batch.Ok {
		t.Errorf("status = %v, want Ok (reconcile should retry the transient poll)", res.Status)
	}
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Errorf("workflow started %d times, want 1 (no re-run)", n)
	}
}

// When reconcile can't reach a terminal state before its deadline, the outcome is UNKNOWN, not a
// failure: the row comes back Untracked with NO error. A nil error is what stops the engine from
// re-running the already-started workflow (the money bug the review caught) — an error, transient
// or not, is the wrong signal here.
func TestDifyProviderReconcileFailureIsUntracked(t *testing.T) {
	var runs int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workflows/run":
			atomic.AddInt32(&runs, 1)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"event":"workflow_started","task_id":"t","workflow_run_id":"run-8","data":{}}`+"\n\n")
		case "/workflows/run/run-8":
			w.WriteHeader(http.StatusBadGateway) // never recovers → reconcile hits its deadline
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := difyProvider{c: dify.New(srv.URL, "k", srv.Client()), user: "u", reconcilePoll: time.Millisecond, reconcileTimeout: 200 * time.Millisecond}
	res, err := p.Run(context.Background(), map[string]string{"symbol": "1"})
	if err != nil {
		t.Fatalf("reconcile failure must NOT be an error (an error would let the engine re-run the started run): %v", err)
	}
	if res.Status != batch.Untracked {
		t.Errorf("status = %v, want Untracked (reconcile couldn't confirm the started run's outcome)", res.Status)
	}
	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Errorf("workflow started %d times, want 1 (no re-run)", n)
	}
}

// On cancel, the provider stops the run on Dify (via the captured task id) instead
// of leaving the workflow executing server-side.
func TestDifyProviderStopsOnCancel(t *testing.T) {
	stopCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/workflows/run":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"event":"workflow_started","task_id":"task-1","workflow_run_id":"run-1","data":{}}`+"\n\n")
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-r.Context().Done() // keep the run in flight until the client cancels
		case "/workflows/tasks/task-1/stop":
			stopCh <- "task-1"
			io.WriteString(w, `{"result":"success"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := difyProvider{c: dify.New(srv.URL, "k", srv.Client()), user: "u"}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond) // let the client read workflow_started (capture the task id)
		cancel()
	}()
	p.Run(ctx, map[string]string{"symbol": "1"})

	select {
	case tid := <-stopCh:
		if tid != "task-1" {
			t.Errorf("stopped task = %q, want task-1", tid)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StopWorkflow was not called on cancel")
	}
}

// A chat-mode target runs via /chat-messages, sending the row's query, and succeeds
// on message_end — the same reconcile/stop machinery applies.
func TestDifyChatProviderRunsChat(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		gotQuery, _ = body["query"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"event":"message","task_id":"t","message_id":"m1"}`+"\n\n")
		io.WriteString(w, `data: {"event":"message_end","task_id":"t","message_id":"m1"}`+"\n\n")
	}))
	defer srv.Close()

	cfg, _ := json.Marshal(difyTargetConfig{BaseURL: srv.URL, APIKey: "k", Mode: "chat"})
	prov, err := buildDifyProvider(string(cfg), "u", false, 0, 0, nil, nil)
	if err != nil {
		t.Fatalf("buildDifyProvider: %v", err)
	}
	res, err := prov.Run(context.Background(), map[string]string{"query": "研究一下"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotPath != "/chat-messages" {
		t.Errorf("path = %q, want /chat-messages", gotPath)
	}
	if gotQuery != "研究一下" {
		t.Errorf("query = %q, want 研究一下", gotQuery)
	}
	if res.Status != batch.Ok {
		t.Errorf("status = %v, want Ok", res.Status)
	}
}
