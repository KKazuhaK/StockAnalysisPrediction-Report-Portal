package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// The queue console polls this endpoint every three seconds, per open tab, and the response is a
// full page of the queue. What it weighs is therefore not a detail: it is a standing cost paid by
// every watcher, and it was never measured — only reasoned about.
//
// This measures it against a queue shaped like a real one, so the number is a fact rather than an
// estimate, and asserts a ceiling so a later change that puts something large back into the list
// fails here instead of on somebody's connection.
//
// The shape that matters: an agent run carries its whole prompt in its inputs, and the list
// returns each job's first row. A few hundred queued agent runs is an ordinary Monday.
const (
	payloadJobs      = 300  // what the console asks for
	payloadPromptLen = 1500 // characters; measured against the prompts in a real deployment
)

func seedQueueForPayload(t *testing.T, s *Server) {
	t.Helper()
	tgt := seedTarget(t, s.st)
	prompt := strings.Repeat("请分析这家公司近三年的重组进展与舆情变化，重点关注", payloadPromptLen/25)
	for i := 0; i < payloadJobs; i++ {
		rows := []map[string]string{{
			"symbol": fmt.Sprintf("%06d", 600000+i),
			"date":   "2026-08-10",
			"query":  prompt,
		}}
		if _, err := s.st.CreateBatchJob(tgt, 1, 0, "op", rows, "50"); err != nil {
			t.Fatalf("seed job %d: %v", i, err)
		}
	}
}

func measureJobsPayload(t *testing.T, s *Server) int {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiBatchJobs(rec, httptest.NewRequest("GET", "/api/admin/batch/jobs?limit=300", nil), "op")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Jobs) != payloadJobs {
		t.Fatalf("returned %d jobs, want %d", len(out.Jobs), payloadJobs)
	}
	return rec.Body.Len()
}

func TestQueueListStaysSmallEnoughToPollEveryThreeSeconds(t *testing.T) {
	s := tenancyServer(t)
	seedQueueForPayload(t, s)

	size := measureJobsPayload(t, s)
	perTab := size * 20 / 60 // bytes per second at one poll every 3s
	t.Logf("queue list: %d jobs, %.0f KB per poll, ~%.0f KB/s per open tab", payloadJobs, float64(size)/1024, float64(perTab)/1024)

	// 300 jobs of identity — symbol, date, status, counts, priority, timestamps — is a few tens of
	// kilobytes. Anything approaching the prompts themselves means the list is shipping content
	// again, and the console only ever renders two lines of it.
	const ceiling = 200 << 10
	if size > ceiling {
		t.Errorf("queue list is %d bytes for %d jobs (ceiling %d): the list is carrying content, not identity", size, payloadJobs, ceiling)
	}
}

// The preview the console shows is bounded; what the server sends must be bounded by the same
// promise, or the bound is only a rendering trick over a payload that was already paid for.
func TestQueueListSendsAPreviewOfInputsNotTheWholePrompt(t *testing.T) {
	s := tenancyServer(t)
	seedQueueForPayload(t, s)

	rec := httptest.NewRecorder()
	s.apiBatchJobs(rec, httptest.NewRequest("GET", "/api/admin/batch/jobs?limit=300", nil), "op")
	var out struct {
		Jobs []struct {
			Inputs string `json:"inputs"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, j := range out.Jobs {
		if len([]rune(j.Inputs)) > queueInputsTotalMax*2 { // keys and JSON punctuation ride along with the values
			t.Fatalf("inputs preview is %d runes, want at most %d: %.80s…", len([]rune(j.Inputs)), queueInputsTotalMax*2, j.Inputs)
		}
	}
	// Still useful, not merely short: the fields a person scans for are the ones at the front.
	if !strings.Contains(out.Jobs[0].Inputs, "symbol") {
		t.Errorf("preview lost the identifying field: %q", out.Jobs[0].Inputs)
	}
}

// Bounding the preview would have quietly broken the console's search, which matched against the
// inputs it had been sent. So the search moved to the server, where it still sees them whole.
func TestQueueSearchReachesWhatThePreviewNoLongerCarries(t *testing.T) {
	s := tenancyServer(t)
	tgt := seedTarget(t, s.st)
	// The needle sits far past the preview budget — the case a client-side filter would now miss.
	buried := strings.Repeat("铺垫", 400) + "特别关注退市风险"
	if _, err := s.st.CreateBatchJob(tgt, 1, 0, "alice", []map[string]string{{"symbol": "600519", "query": buried}}, "50"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.CreateBatchJob(tgt, 1, 0, "bob", []map[string]string{{"symbol": "000001", "query": "无关"}}, "50"); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, q string
		want    int
	}{
		{"deep inside the prompt", "退市风险", 1},
		{"by submitter", "alice", 1},
		{"by workflow name", "My Workflow", 2},
		{"no match", "没有这个词", 0},
		{"empty query returns everything", "", 2},
	} {
		jobs, total := s.st.ListQueueJobs(300, tc.q)
		if len(jobs) != tc.want {
			t.Errorf("%s: %d jobs, want %d", tc.name, len(jobs), tc.want)
		}
		// The count beside "showing the most recent N" is the size of the queue, not of the search.
		if total != 2 {
			t.Errorf("%s: total = %d, want the unfiltered 2", tc.name, total)
		}
	}
}

// The console asks every three seconds; the queue answers far less often than that. An unchanged
// queue must cost an empty 304 — no body on the wire, and no parse, setState or re-render at the
// other end.
func TestAnUnchangedQueueAnswersNotModified(t *testing.T) {
	s := tenancyServer(t)
	seedQueueForPayload(t, s)

	first := httptest.NewRecorder()
	s.apiBatchJobs(first, httptest.NewRequest("GET", "/api/admin/batch/jobs?limit=300", nil), "op")
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag: a poller has nothing to revalidate against")
	}

	again := httptest.NewRequest("GET", "/api/admin/batch/jobs?limit=300", nil)
	again.Header.Set("If-None-Match", tag)
	second := httptest.NewRecorder()
	s.apiBatchJobs(second, again, "op")
	if second.Code != 304 {
		t.Fatalf("status %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 carried %d bytes of body", second.Body.Len())
	}

	// And a queue that DID change must not be answered from the tag.
	if _, err := s.st.CreateBatchJob(seedTarget(t, s.st), 1, 0, "op", []map[string]string{{"symbol": "1"}}, "50"); err != nil {
		t.Fatal(err)
	}
	third := httptest.NewRequest("GET", "/api/admin/batch/jobs?limit=300", nil)
	third.Header.Set("If-None-Match", tag)
	rec := httptest.NewRecorder()
	s.apiBatchJobs(rec, third, "op")
	if rec.Code != 200 || rec.Body.Len() == 0 {
		t.Errorf("a changed queue answered %d with %d bytes", rec.Code, rec.Body.Len())
	}
}
