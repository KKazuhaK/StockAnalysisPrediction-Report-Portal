package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/webhook"
)

func callID(h func(http.ResponseWriter, *http.Request, string), method string, id int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/x", nil)
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	rec := httptest.NewRecorder()
	h(rec, req, "admin")
	return rec
}

func TestWebhookEndpoints(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}

	var gotSig string
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(webhook.SignatureHeader)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	// create
	added := post(t, s.apiWebhookAdd, fmt.Sprintf(`{"url":%q,"events":["report.ingested"],"secret":"topsecret"}`, recv.URL))
	id := int64(added["id"].(float64))

	// list: exposes the subscription but never the secret
	rec := httptest.NewRecorder()
	s.apiWebhooks(rec, httptest.NewRequest("GET", "/x", nil), "admin")
	var list struct {
		Webhooks []map[string]any `json:"webhooks"`
		Events   []string         `json:"events"`
	}
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Webhooks) != 1 {
		t.Fatalf("webhooks = %d, want 1", len(list.Webhooks))
	}
	if _, leaked := list.Webhooks[0]["secret"]; leaked {
		t.Error("the secret must never be returned to the browser")
	}
	if list.Webhooks[0]["has_secret"] != true {
		t.Error("has_secret should be true")
	}
	if len(list.Events) == 0 {
		t.Error("the event catalogue should be returned")
	}

	// test delivery reaches the subscriber, signed
	tr := callID(s.apiWebhookTest, "POST", id)
	var testRes map[string]any
	json.Unmarshal(tr.Body.Bytes(), &testRes)
	if testRes["ok"] != true {
		t.Errorf("test delivery not ok: %s", tr.Body.String())
	}
	if gotSig == "" {
		t.Error("test delivery was not signed")
	}

	// delete
	if code := callID(s.apiWebhookDelete, "DELETE", id).Code; code != http.StatusOK {
		t.Errorf("delete → %d", code)
	}
	if len(st.ListWebhooks()) != 0 {
		t.Error("webhook should be gone after delete")
	}
}
