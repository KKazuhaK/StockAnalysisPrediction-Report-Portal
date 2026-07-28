package app

import (
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Self-service account security (ADR 0023). The 2FA, recovery-code and passkey machinery all existed
// with no way for a user to reach it: enrolment was an admin errand and a forgotten password needed
// an emailed link even for someone already signed in. These are the two pieces that make the
// account page possible — the state it branches on, and the one credential change it did not have.

// apiChangePassword replaces the caller's own password.
//
// The current password IS the step-up here, rather than the X-Step-Up-Proof header the credential
// routes use: it is the very credential being replaced, so proving it is both necessary and
// sufficient, and it is the interaction every other product has trained users to expect. It shares
// the login throttle for the same reason step-up does — this is an online guessing oracle for the
// current password, and the login form is not allowed to be one either.
func (s *Server) apiChangePassword(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &in); err != nil {
		jsonError(w, http.StatusBadRequest, "bad json")
		return
	}
	u := s.st.GetUser(user)
	if u == nil {
		jsonError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// A federated account has no local password, and the SSO login path refuses one, so setting it
	// would produce a credential that cannot be used.
	if u.IsFederated() || u.PasswordHash == "" {
		jsonError(w, http.StatusBadRequest, "your password is managed by your identity provider")
		return
	}
	now := time.Now()
	key := "pwchange:" + strings.ToLower(user)
	if s.loginThr != nil && s.loginThr.blocked(key, now) {
		jsonError(w, http.StatusTooManyRequests, "too many attempts — wait a few minutes and try again")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Current)) != nil {
		if s.loginThr != nil {
			s.loginThr.record(key, now)
		}
		jsonError(w, http.StatusForbidden, "that is not your current password")
		return
	}
	if s.loginThr != nil {
		s.loginThr.reset(key)
	}
	if err := validateNewPassword(in.New); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	h, err := bcrypt.GenerateFromPassword([]byte(in.New), 12)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not set password")
		return
	}
	// SetUserPassword bumps session_rev, which invalidates every session issued before this moment.
	// That is the point of changing a password under suspicion: a change that left a stolen cookie
	// working would let the user believe they had locked an intruder out when they had not.
	if err := s.st.SetUserPassword(user, string(h)); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Everyone else is out, including the caller — so re-issue the caller's own session at the new
	// revision, or they would be signed out of the page they are standing on.
	if fresh := s.st.GetUser(user); fresh != nil {
		s.setSessionCookie(w, r, *fresh)
	}
	writeJSON(w, okJSON)
}

// sessionValid reports whether a signed session cookie value would still be accepted. It exists so
// the revocation behaviour of a password change is directly testable rather than inferred.
func (s *Server) sessionValid(cookie string) bool {
	user, rev := s.verify(cookie)
	if user == "" {
		return false
	}
	u := s.st.GetUser(user)
	return u != nil && u.Active && !s.accountExpired(u) && u.SessionRev == rev
}
