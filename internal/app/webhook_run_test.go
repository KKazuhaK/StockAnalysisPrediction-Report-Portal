package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/webhook"
)

// fireEvent delivers a signed payload to matching active subscribers only, and
// records the delivery status.
func TestFireEventDeliversToSubscribers(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}

	type recvd struct{ event, sig, body string }
	got := make(chan recvd, 4)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- recvd{r.Header.Get(webhook.EventHeader), r.Header.Get(webhook.SignatureHeader), string(b)}
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	id, _ := st.CreateWebhook(recv.URL, []string{"report.ingested"}, "shh")
	// A subscriber to a different event must NOT be called.
	st.CreateWebhook(recv.URL, []string{"other.event"}, "")

	s.fireEvent(EventReportIngested, map[string]any{"id": int64(42)})

	select {
	case r := <-got:
		if r.event != EventReportIngested {
			t.Errorf("event header = %q", r.event)
		}
		if want := "sha256=" + webhook.Sign("shh", []byte(r.body)); r.sig != want {
			t.Errorf("signature = %q, want %q", r.sig, want)
		}
		if !strings.Contains(r.body, `"id":42`) {
			t.Errorf("body = %q, want the numeric report id", r.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("matching subscriber was not called")
	}

	// Only one subscriber should have fired.
	select {
	case r := <-got:
		t.Fatalf("a non-matching subscriber was called: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}

	// The delivery status is recorded.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if w, _ := st.GetWebhook(id); w.LastStatus == 200 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("last_status was not recorded as 200")
}
