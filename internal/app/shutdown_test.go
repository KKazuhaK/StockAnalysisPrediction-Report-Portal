package app

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestServeFinishesInFlightRequests is the property a restart depends on. Without it, SIGTERM cuts
// every open response mid-body: a report upsert answered with truncated JSON the caller has to guess
// about, an export half-written, an audit entry that may or may not have been recorded.
func TestServeFinishesInFlightRequests(t *testing.T) {
	st := newTestStore(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release // held open across the signal, which is the whole test
		// The handler still uses the database on its way out, so this also pins the ordering:
		// closing the store before the handlers are done would fail the very request the grace
		// period was granted for.
		if err := st.Ping(r.Context()); err != nil {
			fmt.Fprint(w, "DB CLOSED UNDER ME: "+err.Error())
			return
		}
		fmt.Fprint(w, "finished")
	})
	srv := &http.Server{Addr: addr, Handler: mux}

	done := make(chan struct{})
	go func() {
		serve(srv, st)
		close(done)
	}()

	// The listener is bound asynchronously; wait for it rather than sleeping a guessed interval.
	client := &http.Client{Timeout: 10 * time.Second}
	waitListening(t, client, addr)

	body := make(chan string, 1)
	go func() {
		resp, err := client.Get("http://" + addr + "/slow")
		if err != nil {
			body <- "ERR: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()
	<-started

	// Stop while that request is still open.
	p, _ := os.FindProcess(os.Getpid())
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	// New connections stop being accepted, but the one in flight is allowed to finish.
	time.Sleep(150 * time.Millisecond)
	if _, err := client.Get("http://" + addr + "/slow"); err == nil {
		t.Error("a shutting-down server must stop accepting new requests")
	}
	close(release)

	select {
	case got := <-body:
		if got != "finished" {
			t.Errorf("the in-flight response was %q; want the complete body", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight request never completed")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the shutdown")
	}
	// The store is closed on the way out, and only after the handlers are done — closing it earlier
	// would fail the very requests the grace period was granted for.
	if err := st.Ping(t.Context()); err == nil {
		t.Error("the database should have been closed as part of the shutdown")
	}
}

func waitListening(t *testing.T, client *http.Client, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the server never started listening")
}
