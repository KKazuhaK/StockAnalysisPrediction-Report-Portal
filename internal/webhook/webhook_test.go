package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSignDeterministicAndKeyed(t *testing.T) {
	a := Sign("s3cret", []byte("hello"))
	if a != Sign("s3cret", []byte("hello")) {
		t.Error("Sign must be deterministic")
	}
	if a == Sign("other", []byte("hello")) {
		t.Error("the secret must affect the signature")
	}
	if len(a) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(a))
	}
}

// Deliver signs the body, sets the event + signature headers, and returns the
// response status.
func TestDeliverSignsAndSetsHeaders(t *testing.T) {
	var gotSig, gotEvent, gotType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(SignatureHeader)
		gotEvent = r.Header.Get(EventHeader)
		gotType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	body := []byte(`{"x":1}`)
	code, err := Deliver(context.Background(), srv.Client(), srv.URL, "sekret", "report.ingested", body)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if code != http.StatusOK {
		t.Errorf("code = %d, want 200", code)
	}
	if gotEvent != "report.ingested" {
		t.Errorf("event header = %q", gotEvent)
	}
	if gotType != "application/json" {
		t.Errorf("content-type = %q", gotType)
	}
	if string(gotBody) != string(body) {
		t.Errorf("body = %q, want %q", gotBody, body)
	}
	if want := "sha256=" + Sign("sekret", body); gotSig != want {
		t.Errorf("signature = %q, want %q", gotSig, want)
	}
}

// A non-2xx status is returned (not an error); a dead endpoint is a transport error.
func TestDeliverStatusAndTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	code, err := Deliver(context.Background(), srv.Client(), srv.URL, "", "e", []byte("{}"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", code)
	}
	client := srv.Client()
	srv.Close()
	if _, err := Deliver(context.Background(), client, srv.URL, "", "e", []byte("{}")); err == nil {
		t.Error("expected a transport error after the server closed")
	}
}
