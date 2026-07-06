package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func postJSON(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/x", strings.NewReader(body)))
	return rec
}

func TestResetTokenRoundTrip(t *testing.T) {
	s := batchServer(t)
	s.st.UpsertUser(User{Username: "kaz", PasswordHash: "hash1", Role: "user"})

	tok := s.resetToken(s.st.GetUser("kaz"))
	if got := s.verifyResetToken(tok); got != "kaz" {
		t.Fatalf("verify = %q, want kaz", got)
	}
	if s.verifyResetToken(tok+"x") != "" {
		t.Error("a tampered token verified")
	}
	// Binding to the current hash makes it single-use: changing the password kills it.
	s.st.SetUserPassword("kaz", "hash2")
	if s.verifyResetToken(tok) != "" {
		t.Error("token still valid after the password changed")
	}
}

// enableEmail turns email on and returns a channel that receives each sent message
// ("to\nbody"). A channel (not a shared string) keeps it race-free when the reset
// send runs on its own goroutine.
func enableEmail(s *Server) chan string {
	s.st.SetSetting("smtp_enabled", "1")
	s.st.SetSetting("smtp_host", "smtp.x.io")
	s.st.SetSetting("smtp_port", "587")
	s.st.SetSetting("smtp_from", "noreply@x.io")
	s.st.SetSetting("public_url", "https://portal.example.com") // required for reset links
	ch := make(chan string, 8)
	s.mailFn = func(to []string, subject, body string) error {
		ch <- strings.Join(to, ",") + "\n" + body
		return nil
	}
	return ch
}

// wantEmail asserts one message arrives; wantNoEmail asserts none does (briefly).
func wantEmail(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("expected an email, got none")
		return ""
	}
}
func wantNoEmail(t *testing.T, ch chan string) {
	t.Helper()
	select {
	case m := <-ch:
		t.Fatalf("expected no email, got: %q", m)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestForgotPassword(t *testing.T) {
	s := batchServer(t)
	s.st.UpsertUser(User{Username: "kaz", PasswordHash: "hash1", Role: "user"})
	s.st.SetUserProfile("kaz", "Kazuha", "kaz@x.io")
	sent := enableEmail(s)

	// By username: emails a reset link.
	if rec := postJSON(t, s.apiForgotPassword, `{"account":"kaz"}`); rec.Code != http.StatusOK {
		t.Fatalf("forgot → %d", rec.Code)
	}
	if m := wantEmail(t, sent); !strings.HasPrefix(m, "kaz@x.io\n") || !strings.Contains(m, "/reset?token=") {
		t.Errorf("reset email = %q, want to kaz@x.io with a reset link", m)
	}

	// By email address too (case-insensitive).
	if rec := postJSON(t, s.apiForgotPassword, `{"account":"KAZ@x.io"}`); rec.Code != http.StatusOK {
		t.Fatalf("forgot by email → %d", rec.Code)
	}
	if m := wantEmail(t, sent); !strings.HasPrefix(m, "kaz@x.io\n") {
		t.Errorf("case-insensitive email lookup failed: %q", m)
	}

	// Unknown account: still 200, but no email (no account enumeration).
	if rec := postJSON(t, s.apiForgotPassword, `{"account":"ghost"}`); rec.Code != http.StatusOK {
		t.Fatalf("forgot unknown → %d, want 200", rec.Code)
	}
	wantNoEmail(t, sent)
}

// The reset link uses the configured public_url, never a forged Host / X-Forwarded-
// Host — otherwise a forged host would poison the link (account takeover).
func TestForgotPasswordIgnoresForgedHost(t *testing.T) {
	s := batchServer(t)
	s.st.UpsertUser(User{Username: "kaz", PasswordHash: "hash1", Role: "user"})
	s.st.SetUserProfile("kaz", "Kazuha", "kaz@x.io")
	sent := enableEmail(s) // sets public_url = https://portal.example.com

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"account":"kaz"}`))
	req.Header.Set("X-Forwarded-Host", "attacker.example")
	req.Host = "attacker.example"
	s.apiForgotPassword(rec, req)

	m := wantEmail(t, sent)
	if !strings.Contains(m, "https://portal.example.com/reset?token=") {
		t.Errorf("link not from public_url: %q", m)
	}
	if strings.Contains(m, "attacker.example") {
		t.Error("forged host leaked into the reset link")
	}
}

// With no public_url configured, reset-by-email is disabled (fail closed) — no link
// can be trusted, so nothing is sent.
func TestForgotPasswordFailsClosedWithoutPublicURL(t *testing.T) {
	s := batchServer(t)
	s.st.UpsertUser(User{Username: "kaz", PasswordHash: "hash1", Role: "user"})
	s.st.SetUserProfile("kaz", "Kazuha", "kaz@x.io")
	sent := enableEmail(s)
	s.st.SetSetting("public_url", "") // unset

	if rec := postJSON(t, s.apiForgotPassword, `{"account":"kaz"}`); rec.Code != http.StatusOK {
		t.Fatalf("forgot → %d, want 200", rec.Code)
	}
	wantNoEmail(t, sent)
}

// A disabled account can't be reset even with an otherwise-valid token.
func TestResetTokenRejectsDisabled(t *testing.T) {
	s := batchServer(t)
	s.st.UpsertUser(User{Username: "kaz", PasswordHash: "hash1", Role: "user"})
	tok := s.resetToken(s.st.GetUser("kaz"))
	if s.verifyResetToken(tok) != "kaz" {
		t.Fatal("token should verify while active")
	}
	s.st.SetUserActive("kaz", false)
	if s.verifyResetToken(tok) != "" {
		t.Error("reset token verified for a disabled account")
	}
}

func TestResetPassword(t *testing.T) {
	s := batchServer(t)
	s.st.UpsertUser(User{Username: "kaz", PasswordHash: "hash1", Role: "user"})
	tok := s.resetToken(s.st.GetUser("kaz"))

	if rec := postJSON(t, s.apiResetPassword, `{"token":"`+tok+`","password":"newpass123"}`); rec.Code != http.StatusOK {
		t.Fatalf("reset → %d: %s", rec.Code, rec.Body.String())
	}
	if u := s.st.GetUser("kaz"); bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("newpass123")) != nil {
		t.Error("password was not updated")
	}
	// The token is now spent (the hash it bound to changed).
	if rec := postJSON(t, s.apiResetPassword, `{"token":"`+tok+`","password":"another12"}`); rec.Code == http.StatusOK {
		t.Error("a reused reset token was accepted")
	}
	// Garbage token and short password are rejected.
	if rec := postJSON(t, s.apiResetPassword, `{"token":"garbage","password":"whatever1"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("garbage token → %d, want 400", rec.Code)
	}
	fresh := s.resetToken(s.st.GetUser("kaz"))
	if rec := postJSON(t, s.apiResetPassword, `{"token":"`+fresh+`","password":"123"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("short password → %d, want 400", rec.Code)
	}
}

func TestEmailSaveGetHidesPassword(t *testing.T) {
	s := batchServer(t)
	post(t, s.apiEmailSave, `{"enabled":true,"host":"smtp.x.io","port":587,"user":"u","pass":"secret","from":"noreply@x.io","security":"starttls"}`)

	rec := httptest.NewRecorder()
	s.apiEmailGet(rec, httptest.NewRequest("GET", "/x", nil), "admin")
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["host"] != "smtp.x.io" || out["has_pass"] != true || out["enabled"] != true {
		t.Fatalf("get = %v", out)
	}
	if _, leaked := out["pass"]; leaked {
		t.Error("email GET leaked the password")
	}
	// A blank password on re-save keeps the stored one.
	post(t, s.apiEmailSave, `{"host":"smtp2.x.io"}`)
	if s.st.GetSetting("smtp_pass", "") != "secret" {
		t.Error("blank password wiped the stored one")
	}
	if s.st.GetSetting("smtp_host", "") != "smtp2.x.io" {
		t.Error("host not updated")
	}
}

func TestNotifyJobDone(t *testing.T) {
	s := batchServer(t)
	s.st.UpsertUser(User{Username: "kaz", PasswordHash: "h", Role: "user"})
	s.st.SetUserProfile("kaz", "Kazuha", "kaz@x.io")
	sent := enableEmail(s)
	j := BatchJob{ID: 5, CreatedBy: "kaz", Status: "finished", Total: 3, Succeeded: 2, Failed: 1}

	// Without opt-in, no email.
	s.notifyJobDone(j)
	wantNoEmail(t, sent)

	// Opted in → the submitter is emailed.
	s.jobNotify.Store(int64(5), true)
	s.notifyJobDone(j)
	if m := wantEmail(t, sent); !strings.HasPrefix(m, "kaz@x.io\n") || !strings.Contains(m, "#5") {
		t.Errorf("job-done email = %q", m)
	}
	// The opt-in is single-shot (cleared after sending).
	s.notifyJobDone(j)
	wantNoEmail(t, sent)
}
