package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// eventSink subscribes to report.ingested and hands back the payloads as they arrive.
func eventSink(t *testing.T, s *Server) <-chan map[string]any {
	t.Helper()
	out := make(chan map[string]any, 8)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Errorf("webhook body is not JSON: %v", err)
		}
		out <- m
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(recv.Close)
	if _, err := s.st.CreateWebhook(recv.URL, []string{EventReportIngested}, ""); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	return out
}

func waitEvent(t *testing.T, ch <-chan map[string]any, why string) map[string]any {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(3 * time.Second):
		t.Fatalf("no report.ingested event for %s", why)
		return nil
	}
}

func expectNoEvent(t *testing.T, ch <-chan map[string]any, why string) {
	t.Helper()
	select {
	case m := <-ch:
		t.Fatalf("%s must not fire an event; got %v", why, m)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestHandWrittenReportsFireTheIngestEvent closes a hole that opened the day the editor shipped:
// a report pushed by a workflow told every subscriber, and a report a person wrote told nobody. The
// event is the same one on purpose — a subscriber's contract is "a report arrived", and a new event
// type would leave every existing subscriber missing hand-written reports exactly as before.
func TestHandWrittenReportsFireTheIngestEvent(t *testing.T) {
	s := editorFixture(t)
	events := eventSink(t, s)

	body := `{"symbol":"002594","name":"比亚迪","date":"2026-09-04","subtype":"深度分析",
		"title":"手写的一篇","body_md":"# 正文","source":"人工","audience":"all"}`
	rec := editorCall(t, s, http.MethodPost, "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)

	ev := waitEvent(t, events, "a newly written report")
	if got := fmt.Sprint(ev["id"]); got != fmt.Sprint(float64(created.ID)) {
		t.Errorf("event id = %v, want the new report's id %d", ev["id"], created.ID)
	}
	if ev["title"] != "手写的一篇" || ev["symbol"] != "002594" {
		t.Errorf("event payload lost the report's identity: %v", ev)
	}
	if ev["created"] != true {
		t.Error("a newly written report must be announced as created")
	}
	if ev["version"] != s.st.ManualVersion() {
		t.Errorf("version = %v; a subscriber tells hand-written from machine by this field", ev["version"])
	}
	if ev["author"] != "editor" {
		t.Errorf("author = %v; want the person who wrote it", ev["author"])
	}
}

// TestEditingFiresOnlyWhenTheWordsChange keeps the event stream and the revision history agreeing on
// what an edit is. A save that rewrote nothing — an audience change — files no revision, and it
// should not wake every subscriber either.
func TestEditingFiresOnlyWhenTheWordsChange(t *testing.T) {
	s := editorFixture(t)

	body := `{"symbol":"002594","date":"2026-09-04","subtype":"深度分析","title":"稿子",
		"body_md":"第一版","audience":"all"}`
	rec := editorCall(t, s, http.MethodPost, "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	id := fmt.Sprint(created.ID)

	// Subscribe only now, so the create's own event does not have to be consumed first.
	events := eventSink(t, s)

	rep, _ := s.st.GetNew(created.ID, nil)
	save := func(md, audience string) {
		t.Helper()
		rec := editorCall(t, s, http.MethodPut, id, fmt.Sprintf(
			`{"symbol":"002594","date":"2026-09-04","subtype":"深度分析","title":"稿子",
			  "body_md":%q,%s,"updated_at":%q}`, md, audience, rep.Time))
		if rec.Code != http.StatusOK {
			t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			UpdatedAt string `json:"updated_at"`
		}
		json.Unmarshal(rec.Body.Bytes(), &out)
		rep.Time = out.UpdatedAt
	}

	save("第二版", `"audience":"all"`)
	ev := waitEvent(t, events, "an edit that rewrote the body")
	if ev["created"] != false {
		t.Error("an edit must be announced as an update, not a creation")
	}
	if ev["id"] != float64(created.ID) {
		t.Errorf("event id = %v, want %d", ev["id"], created.ID)
	}

	save("第二版", `"audience":"grant","viewers":["u:editor"]`) // same words, narrower audience
	expectNoEvent(t, events, "a save that changed no content")
}
