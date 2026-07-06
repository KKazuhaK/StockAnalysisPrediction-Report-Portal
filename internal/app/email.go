package app

import (
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/mail"
)

// notifyJobDone emails the submitter that their batch job finished, if they opted in
// (jobNotify) and have an address. Best-effort — a send failure is only logged.
func (s *Server) notifyJobDone(j BatchJob) {
	v, ok := s.jobNotify.LoadAndDelete(j.ID)
	if b, _ := v.(bool); !ok || !b {
		return
	}
	u := s.st.GetUser(j.CreatedBy)
	if u == nil || u.Email == "" || !s.emailEnabled() {
		return
	}
	brand := s.brandName()
	body := fmt.Sprintf(
		`<p>Hi %s,</p><p>Your batch job #%d finished (%s): %d of %d succeeded, %d failed.</p>`,
		html.EscapeString(u.Name()), j.ID, html.EscapeString(j.Status), j.Succeeded, j.Total, j.Failed)
	if err := s.sendEmail([]string{u.Email}, fmt.Sprintf("%s — job #%d finished", brand, j.ID), body); err != nil {
		log.Printf("job-done email for job %d failed: %v", j.ID, err)
	}
}

// brandName is the site title (email subjects / bodies), falling back to a default.
func (s *Server) brandName() string {
	if t := strings.TrimSpace(s.st.GetSetting("site_title", "")); t != "" {
		return t
	}
	return "Research Portal"
}

// Email subsystem: SMTP config lives in DB meta (managed in the web UI, never in
// config.yaml), used for password reset (email.go / password_reset.go) and opt-in
// run-done notifications. All settings are additive meta keys — no schema change.

// mailConfig builds the SMTP config from settings.
func (s *Server) mailConfig() mail.Config {
	port, _ := strconv.Atoi(s.st.GetSetting("smtp_port", "587"))
	return mail.Config{
		Host:     s.st.GetSetting("smtp_host", ""),
		Port:     port,
		User:     s.st.GetSetting("smtp_user", ""),
		Pass:     s.st.GetSetting("smtp_pass", ""),
		From:     s.st.GetSetting("smtp_from", ""),
		Security: s.st.GetSetting("smtp_security", "starttls"),
	}
}

// emailEnabled reports whether email may be sent (admin toggle + a usable config).
func (s *Server) emailEnabled() bool {
	return s.st.GetSetting("smtp_enabled", "0") == "1" && s.mailConfig().Enabled()
}

// sendEmail delivers one message, honoring the test seam. It refuses to send when
// email is disabled/unconfigured.
func (s *Server) sendEmail(to []string, subject, htmlBody string) error {
	if s.mailFn != nil {
		return s.mailFn(to, subject, htmlBody)
	}
	if !s.emailEnabled() {
		return errors.New("email is not enabled")
	}
	return s.mailConfig().Send(to, subject, htmlBody)
}

// ---------- admin config ----------

// apiEmailGet returns the SMTP config for the admin form — never the password (only
// whether one is stored), the same way target api keys are handled.
func (s *Server) apiEmailGet(w http.ResponseWriter, r *http.Request, user string) {
	c := s.mailConfig()
	writeJSON(w, map[string]any{
		"enabled":    s.st.GetSetting("smtp_enabled", "0") == "1",
		"host":       c.Host,
		"port":       c.Port,
		"user":       c.User,
		"from":       c.From,
		"security":   c.Security,
		"has_pass":   c.Pass != "",
		"public_url": s.st.GetSetting("public_url", ""), // origin for reset links (avoids host-header poisoning)
	})
}

// apiEmailSave persists the SMTP config. A blank password keeps the stored one so
// editing other fields never forces re-entering the secret.
func (s *Server) apiEmailSave(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Enabled  *bool   `json:"enabled"`
		Host     *string `json:"host"`
		Port     *int    `json:"port"`
		User     *string `json:"user"`
		Pass     *string `json:"pass"`
		From      *string `json:"from"`
		Security  *string `json:"security"`
		PublicURL *string `json:"public_url"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	if in.PublicURL != nil {
		s.st.SetSetting("public_url", strings.TrimSpace(*in.PublicURL))
	}
	if in.Enabled != nil {
		s.st.SetSetting("smtp_enabled", boolStr(*in.Enabled))
	}
	if in.Host != nil {
		s.st.SetSetting("smtp_host", strings.TrimSpace(*in.Host))
	}
	if in.Port != nil && *in.Port > 0 {
		s.st.SetSetting("smtp_port", strconv.Itoa(*in.Port))
	}
	if in.User != nil {
		s.st.SetSetting("smtp_user", strings.TrimSpace(*in.User))
	}
	if in.Pass != nil && *in.Pass != "" { // blank → keep stored password
		s.st.SetSetting("smtp_pass", *in.Pass)
	}
	if in.From != nil {
		s.st.SetSetting("smtp_from", strings.TrimSpace(*in.From))
	}
	if in.Security != nil {
		s.st.SetSetting("smtp_security", strings.TrimSpace(*in.Security))
	}
	writeJSON(w, okJSON)
}

// apiEmailTest sends a test message to the given address using the saved config, so
// an admin can verify SMTP before relying on it.
func (s *Server) apiEmailTest(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		To string `json:"to"`
	}
	readJSON(r, &in)
	to := strings.TrimSpace(in.To)
	if to == "" {
		if u := s.st.GetUser(user); u != nil {
			to = u.Email
		}
	}
	if to == "" {
		jsonError(w, http.StatusBadRequest, "no recipient")
		return
	}
	if err := s.sendEmail([]string{to}, s.brandName()+" — SMTP test", "<p>This is a test email from "+html.EscapeString(s.brandName())+". SMTP is working.</p>"); err != nil {
		jsonError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// boolStr renders a bool as the "1"/"0" this codebase stores for boolean settings.
func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
