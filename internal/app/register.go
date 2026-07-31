package app

import (
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

// Self-service registration.
//
// A visitor signs up with their email, which becomes their username — there is no second name to
// pick, and no way for two people to race for one. The account is created DISABLED and, unless the
// admin turned verification off, stays that way until the emailed link is followed; otherwise
// anyone could create an account against someone else's address.
//
// Where a new account lands is the security-critical part, and it is a setting with two shapes:
//
//   - no OU configured (the default) — the account is marked restricted with no version grants, so
//     the person can sign in and see NOTHING until an admin places them. Safe, at the cost of a
//     manual step per signup.
//   - an OU configured — the account joins it and inherits its grants, so it is usable immediately.
//     Convenient, at the cost that misconfiguring that one OU exposes every future registrant.
//
// The default is the safe one and opening it up is an explicit action, which is the only ordering
// that fails in the right direction.
//
// Verification reuses the pending-request table (ADR 0023): same TTL, same single-use conditional
// delete, no second mechanism to keep correct.

const (
	setRegEnabled    = "reg_enabled"
	setRegVerify     = "reg_require_verify"
	setRegDomains    = "reg_email_domains"
	setRegGroup      = "reg_default_group"
	setRegExpiryDays = "reg_default_expiry_days"
)

const verifyTTL = 24 * time.Hour

// registrationOpen reports whether the public signup route exists at all.
func (s *Server) registrationOpen() bool { return s.st.GetSetting(setRegEnabled, "") == "1" }

// registrationNeedsVerify reports whether a new account must confirm its email. Stored positively
// and defaulting to ON: an unset setting must mean the safe thing.
func (s *Server) registrationNeedsVerify() bool {
	return s.st.GetSetting(setRegVerify, "1") == "1"
}

// emailDomainAllowed applies the admin's domain allow-list. Empty means any domain.
func (s *Server) emailDomainAllowed(email string) bool {
	list := strings.TrimSpace(s.st.GetSetting(setRegDomains, ""))
	if list == "" {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range strings.Split(list, ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" && d == domain {
			return true
		}
	}
	return false
}

// validEmail is a deliberately small check: exactly one @, something either side, a dot in the
// domain, and no whitespace. Anything stricter rejects addresses that are legal, and the real proof
// of an address is that its owner follows the link we send to it.
func validEmail(e string) bool {
	if e == "" || len(e) > 254 || strings.ContainsAny(e, " \t\r\n") {
		return false
	}
	at := strings.Index(e, "@")
	return at > 0 && at == strings.LastIndex(e, "@") &&
		at < len(e)-1 && strings.Contains(e[at+1:], ".") && !strings.HasSuffix(e, ".")
}

// POST /api/register — create a local account.
func (s *Server) apiRegister(w http.ResponseWriter, r *http.Request) {
	if !s.registrationOpen() {
		jsonError(w, http.StatusNotFound, "self-service registration is not enabled")
		return
	}
	var in struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		captchaProof
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if !s.requireCaptcha(w, r, ctxRegister, email, in.captchaProof) {
		return
	}
	// Rate-limited on the same counter as login, keyed by address: signup is an account-creation
	// and outbound-email flood in one, and the captcha alone does not bound it.
	now := time.Now()
	ipKey := "reg:" + clientIP(r, s.trustedNets)
	if s.loginThr != nil && s.loginThr.blocked(ipKey, now) {
		jsonError(w, http.StatusTooManyRequests, "too many attempts — try again later")
		return
	}
	if s.loginThr != nil {
		s.loginThr.record(ipKey, now)
	}

	if !validEmail(email) {
		jsonError(w, http.StatusBadRequest, "that does not look like an email address")
		return
	}
	if !s.emailDomainAllowed(email) {
		jsonError(w, http.StatusBadRequest, "that email domain is not accepted here")
		return
	}
	if err := validateNewPassword(in.Password); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.registrationNeedsVerify() && !s.emailEnabled() {
		// Refusing beats creating an account whose activation email can never be sent.
		jsonError(w, http.StatusServiceUnavailable, "registration is temporarily unavailable")
		return
	}
	// Taken-address IS revealed, unlike password recovery. It is the expected signup experience —
	// the alternative is a form that silently does nothing for someone who simply already has an
	// account — and an attacker learns the same fact by trying to register anyway.
	// UsernameTaken, not GetUser: the address is folded above, but a database written before the
	// fold may hold `Alice@corp.example`, and an exact-match guard would create a second account on
	// the one principal `u:alice@corp.example`. UserByEmail still covers the different-address case.
	if s.st.UsernameTaken(email) || s.st.UserByEmail(email) != nil {
		jsonError(w, http.StatusConflict, "that email is already registered")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not create the account")
		return
	}
	needVerify := s.registrationNeedsVerify()
	// UpsertUser writes only the credentials; the profile and the enabled flag have their own
	// setters, and the account must be disabled BEFORE anything else can reach it.
	if err := s.st.UpsertUser(User{Username: email, PasswordHash: string(hash), Role: "user"}); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if needVerify {
		if err := s.st.SetUserActive(email, false); err != nil {
			log.Printf("registration: could not disable %s pending verification: %v", email, err)
			s.st.DeleteUser(email) // fail closed: never leave an unverifiable account enabled
			jsonError(w, http.StatusInternalServerError, "could not create the account")
			return
		}
	}
	s.st.SetUserProfile(email, strings.TrimSpace(in.DisplayName), email)
	s.placeRegistrant(email)
	if !needVerify {
		writeJSON(w, map[string]any{"ok": true, "requires_verification": false})
		return
	}
	token, err := newAuthToken()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not create the account")
		return
	}
	if err := s.st.CreateAuthRequest(AuthRequest{Token: token, Kind: "verify", Username: email},
		time.Now().Add(verifyTTL)); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go func() {
		if err := s.sendVerifyEmail(email, token); err != nil {
			log.Printf("registration: verification email to %s failed: %v", email, err)
		}
	}()
	writeJSON(w, map[string]any{"ok": true, "requires_verification": true})
}

// placeRegistrant puts a new account where the admin configured, and marks it restricted when they
// configured nowhere. A registrant who lands unplaced must see nothing until someone decides what
// they may see — the account exists, it simply discloses nothing.
func (s *Server) placeRegistrant(username string) {
	group, _ := strconv.ParseInt(strings.TrimSpace(s.st.GetSetting(setRegGroup, "")), 10, 64)
	if group > 0 && s.st.GroupExists(group) {
		if err := s.st.SetPrimaryGroup(username, group); err != nil {
			log.Printf("registration: could not place %s in OU %d: %v", username, group, err)
		}
	} else if err := s.st.SetUserRestricted(username, true); err != nil {
		log.Printf("registration: could not scope %s: %v", username, err)
	}
	if days, _ := strconv.Atoi(s.st.GetSetting(setRegExpiryDays, "")); days > 0 {
		s.st.SetUserExpiry(username, time.Now().In(s.panelLocation()).
			AddDate(0, 0, days).Format("2006-01-02"))
	}
}

// POST /api/register/verify — confirm an address and enable the account.
func (s *Server) apiRegisterVerify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	// Consumed, not peeked: one link, one activation, decided by the conditional delete rather than
	// by a check-then-act that two concurrent clicks could both pass.
	req, ok := s.st.ConsumeAuthRequest(strings.TrimSpace(in.Token), time.Now())
	if !ok || req.Kind != "verify" || req.Username == "" {
		jsonError(w, http.StatusBadRequest, "this verification link is invalid or has expired")
		return
	}
	if err := s.st.SetUserActive(req.Username, true); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("registration: %s verified", req.Username)
	writeJSON(w, okJSON)
}

func (s *Server) sendVerifyEmail(email, token string) error {
	base := s.publicBaseURL()
	if base == "" {
		return fmt.Errorf("no public URL configured")
	}
	link := base + "/verify?token=" + url.QueryEscape(token)
	brand := s.brandName()
	body := fmt.Sprintf(
		`<p>Hi,</p><p>Confirm this address to finish creating your %s account:</p><p><a href="%s">%s</a></p><p>The link is valid for 24 hours. If you did not sign up, ignore this email — the account stays disabled and is removed automatically.</p>`,
		html.EscapeString(brand), link, html.EscapeString(link))
	return s.sendEmail([]string{email}, brand+" — confirm your email", body)
}

// GET /api/register/config — what the public signup page needs to render, and nothing else. The
// domain allow-list is deliberately NOT exposed: it is a hint about the organization, and the form
// learns the same thing from a refusal.
func (s *Server) apiRegisterConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"enabled":               s.registrationOpen(),
		"requires_verification": s.registrationNeedsVerify(),
	})
}
