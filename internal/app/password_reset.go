package app

import (
	"crypto/hmac"
	"encoding/base64"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Password reset by email. The token is stateless: an HMAC (reusing the session
// secret) over "pwreset|<user>|<expiry>" AND the user's current password hash — so
// once the password changes the token stops verifying (effectively single-use), with
// no reset table to maintain.

const resetTokenTTL = time.Hour

func (s *Server) resetToken(u *User) string {
	exp := time.Now().Add(resetTokenTTL).Unix()
	msg := fmt.Sprintf("pwreset|%s|%d", u.Username, exp)
	sig := s.hmac(msg + "|" + u.PasswordHash) // binding to the current hash makes it single-use
	return base64.RawURLEncoding.EncodeToString([]byte(msg)) + "." + sig
}

// verifyResetToken returns the username a valid, unexpired reset token authorizes, or
// "" if the token is malformed, expired, or no longer matches the account.
func (s *Server) verifyResetToken(token string) string {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ""
	}
	seg := strings.Split(string(raw), "|")
	if len(seg) != 3 || seg[0] != "pwreset" {
		return ""
	}
	exp, err := strconv.ParseInt(seg[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return ""
	}
	u := s.st.GetUser(seg[1])
	if u == nil || !u.Active { // a disabled account can't be reset
		return ""
	}
	if !hmac.Equal([]byte(s.hmac(string(raw)+"|"+u.PasswordHash)), []byte(parts[1])) {
		return ""
	}
	return u.Username
}

// resetLinkBase is the trusted public origin for reset links: the admin-set
// public_url, or "" if unset. There is deliberately NO request-derived fallback —
// r.Host and X-Forwarded-* are all attacker-controllable and a forged one would
// poison the emailed link into an account-takeover primitive. Reset-by-email
// therefore requires public_url to be configured (Manage → Email).
func (s *Server) resetLinkBase() string {
	return strings.TrimRight(strings.TrimSpace(s.st.GetSetting("public_url", "")), "/")
}

// apiForgotPassword emails a reset link to the account matching the username or email.
// It always returns 200 (and only sends when email is enabled and the account has an
// address) so it can't be used to enumerate accounts.
func (s *Server) apiForgotPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Account string `json:"account"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	acct := strings.TrimSpace(in.Account)
	u := s.st.GetUser(acct)
	if u == nil {
		u = s.st.UserByEmail(acct)
	}
	base := s.resetLinkBase()
	eligible := u != nil && u.Active && u.Email != "" && s.emailEnabled()
	if eligible && base != "" {
		link := base + "/reset?token=" + url.QueryEscape(s.resetToken(u))
		// Send off the request path: a synchronous SMTP round-trip only for real
		// accounts would leak account existence via response latency.
		go func() {
			if err := s.sendResetEmail(u, link); err != nil {
				log.Printf("password reset email to %s failed: %v", u.Username, err)
			}
		}()
	} else if eligible {
		log.Printf("password reset for %q skipped: set a Public URL in Email settings to enable reset links", u.Username)
	}
	writeJSON(w, okJSON)
}

func (s *Server) sendResetEmail(u *User, link string) error {
	brand := s.brandName()
	body := fmt.Sprintf(
		`<p>Hi %s,</p><p>A password reset was requested for your %s account. Use the link below within the hour to set a new password:</p><p><a href="%s">%s</a></p><p>If you didn't request this, you can ignore this email — your password stays unchanged.</p>`,
		html.EscapeString(u.Name()), html.EscapeString(brand), link, html.EscapeString(link))
	return s.sendEmail([]string{u.Email}, brand+" — password reset", body)
}

// apiResetPassword sets a new password given a valid reset token.
func (s *Server) apiResetPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	user := s.verifyResetToken(in.Token)
	if user == "" {
		jsonError(w, http.StatusBadRequest, "this reset link is invalid or has expired")
		return
	}
	if len(in.Password) < 6 {
		jsonError(w, http.StatusBadRequest, "password must be at least 6 characters")
		return
	}
	h, err := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not set password")
		return
	}
	if err := s.st.SetUserPassword(user, string(h)); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}
