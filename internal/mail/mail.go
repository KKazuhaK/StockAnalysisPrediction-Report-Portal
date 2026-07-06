// Package mail is a small SMTP sender for the portal's transactional email
// (password reset, run-done notifications). Configuration lives in the DB (managed
// in the web UI), never in config.yaml. Three transport modes cover the common
// providers: implicit TLS (465), STARTTLS (587), and plain (25 / localhost).
package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Config is an SMTP endpoint + sender identity.
type Config struct {
	Host     string
	Port     int
	User     string // "" → no auth
	Pass     string
	From     string
	Security string // "tls" (implicit) | "starttls" | "none"
}

// Enabled reports whether the config is complete enough to send.
func (c Config) Enabled() bool {
	return c.Host != "" && c.Port > 0 && c.From != ""
}

// stripCRLF removes carriage returns / line feeds so a value can't inject extra
// headers (or a body) when written into a header line.
func stripCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// BuildMessage assembles an RFC 5322 HTML message. The subject is RFC 2047 encoded
// so non-ASCII (e.g. Chinese) survives; From/To are stripped of CR/LF to prevent
// header injection.
func BuildMessage(from string, to []string, subject, htmlBody string, date time.Time) []byte {
	safeTo := make([]string, len(to))
	for i, a := range to {
		safeTo[i] = stripCRLF(a)
	}
	var b strings.Builder
	b.WriteString("From: " + stripCRLF(from) + "\r\n")
	b.WriteString("To: " + strings.Join(safeTo, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.BEncoding.Encode("UTF-8", subject) + "\r\n")
	b.WriteString("Date: " + date.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return []byte(b.String())
}

// Send delivers one HTML message. It negotiates TLS per Security and authenticates
// when a user is set.
func (c Config) Send(to []string, subject, htmlBody string) error {
	if !c.Enabled() {
		return errors.New("mail: not configured")
	}
	if len(to) == 0 {
		return errors.New("mail: no recipients")
	}
	msg := BuildMessage(c.From, to, subject, htmlBody, time.Now())
	addr := net.JoinHostPort(c.Host, fmt.Sprint(c.Port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}

	var conn net.Conn
	var err error
	if c.Security == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: c.Host})
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: dial: %w", err)
	}

	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: client: %w", err)
	}
	defer cl.Close()

	if c.Security == "starttls" {
		if err := cl.StartTLS(&tls.Config{ServerName: c.Host}); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	}
	if c.User != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.User, c.Pass, c.Host)); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return fmt.Errorf("mail: from: %w", err)
	}
	for _, rcpt := range to {
		if err := cl.Rcpt(rcpt); err != nil {
			return fmt.Errorf("mail: rcpt %s: %w", rcpt, err)
		}
	}
	wc, err := cl.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		wc.Close()
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("mail: close: %w", err)
	}
	return cl.Quit()
}
