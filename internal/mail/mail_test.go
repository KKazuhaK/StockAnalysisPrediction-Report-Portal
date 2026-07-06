package mail

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestBuildMessage(t *testing.T) {
	msg := string(BuildMessage("noreply@x.io", []string{"a@x.io", "b@x.io"}, "重置密码 reset", "<p>hi</p>", time.Unix(0, 0).UTC()))
	for _, want := range []string{
		"From: noreply@x.io\r\n",
		"To: a@x.io, b@x.io\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/html; charset=UTF-8\r\n",
		"\r\n<p>hi</p>",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\n---\n%s", want, msg)
		}
	}
	// A non-ASCII subject must be RFC 2047 encoded (not raw UTF-8 in the header).
	if !strings.Contains(msg, "Subject: =?UTF-8?b?") {
		t.Errorf("subject not encoded:\n%s", msg)
	}
	if strings.Contains(msg, "重置密码") {
		t.Error("raw non-ASCII leaked into the header")
	}
}

// fakeSMTP is a throwaway server that speaks just enough SMTP to accept one message
// and captures its DATA payload.
func fakeSMTP(t *testing.T, got chan<- string) Config {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		fmt.Fprint(conn, "220 test ESMTP\r\n")
		inData := false
		var data strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					got <- data.String()
					fmt.Fprint(conn, "250 OK\r\n")
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				fmt.Fprint(conn, "250-test\r\n250 OK\r\n")
			case strings.HasPrefix(cmd, "DATA"):
				inData = true
				fmt.Fprint(conn, "354 go ahead\r\n")
			case strings.HasPrefix(cmd, "QUIT"):
				fmt.Fprint(conn, "221 bye\r\n")
				return
			default: // MAIL, RCPT, etc.
				fmt.Fprint(conn, "250 OK\r\n")
			}
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return Config{Host: "127.0.0.1", Port: addr.Port, From: "noreply@x.io", Security: "none"}
}

func TestSendPlain(t *testing.T) {
	got := make(chan string, 1)
	cfg := fakeSMTP(t, got)
	if err := cfg.Send([]string{"user@x.io"}, "Hello", "<p>body</p>"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case data := <-got:
		if !strings.Contains(data, "<p>body</p>") || !strings.Contains(data, "Content-Type: text/html") {
			t.Errorf("delivered message missing content:\n%s", data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server received no message")
	}
}

func TestSendUnconfigured(t *testing.T) {
	if err := (Config{}).Send([]string{"a@x.io"}, "s", "b"); err == nil {
		t.Error("expected an error sending with no config")
	}
}

// An address with an embedded CRLF must not inject extra headers.
func TestBuildMessageStripsCRLF(t *testing.T) {
	msg := string(BuildMessage("noreply@x.io\r\nBcc: evil@x.io", []string{"user@x.io\r\nBcc: evil2@x.io"}, "hi", "<p>b</p>", time.Unix(0, 0).UTC()))
	if strings.Contains(msg, "\r\nBcc:") {
		t.Errorf("CRLF header injection not stripped:\n%s", msg)
	}
}
