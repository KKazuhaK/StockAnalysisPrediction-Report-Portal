package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

// TOTP two-factor for LOCAL accounts (ADR 0023). A federated account's factors belong to its IdP:
// asking for a second one we manage would be both confusing and weaker than what the IdP enforces.
//
// Enrolment is confirm-before-enable — the secret is stored but 2FA is not in force until the user
// proves one correct code. Enabling first would let a mistyped enrolment lock someone out of their
// own account, which is the classic way 2FA rollouts generate support tickets.

const (
	recoveryCodeCount = 10
	totpPendingTTL    = 5 * time.Minute
)

// totpIssuer labels the entry in the user's authenticator app.
func (s *Server) totpIssuer() string {
	if n := strings.TrimSpace(s.st.GetSetting("site_name", "")); n != "" {
		return n
	}
	return "Report Portal"
}

// POST /api/me/2fa/setup — mint a secret and return the provisioning URI. Nothing is in force yet.
//
// Step-up gates enrolment, not just removal. Turning 2FA ON is an attack in its own right: with a
// stolen cookie an attacker enrols THEIR authenticator, and from then on the owner's correct
// password is parked behind a second factor only the attacker holds — a lockout with no
// self-service way back. The enable step below needs a secret that only a stepped-up setup ever
// returns, so gating the mint gates the whole enrolment.
func (s *Server) apiTOTPSetup(w http.ResponseWriter, r *http.Request, user string) {
	// The preconditions come first so the caller gets the accurate message about their OWN account
	// rather than a confusing "confirm with your password" for a state no proof could change. They
	// leak nothing: the caller is already authenticated as this account.
	u := s.st.GetUser(user)
	if u == nil || u.IsFederated() {
		jsonError(w, http.StatusBadRequest, "two-factor is managed by your identity provider")
		return
	}
	if u.TOTPEnabled {
		jsonError(w, http.StatusConflict, "two-factor is already enabled; disable it first")
		return
	}
	if !s.requireStepUp(w, r, user) {
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: s.totpIssuer(), AccountName: user})
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not generate a secret")
		return
	}
	enc, err := s.sealSecret(user, "totp_secret", key.Secret())
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "could not store the secret")
		return
	}
	if err := s.st.SetTOTPSecret(user, enc); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The secret and URI are shown exactly once, at enrolment, and never returned again.
	writeJSON(w, map[string]any{"ok": true, "secret": key.Secret(), "uri": key.URL()})
}

// POST /api/me/2fa/enable — prove a code, then switch 2FA on and hand back the recovery codes.
func (s *Server) apiTOTPEnable(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Code string `json:"code"`
	}
	readJSON(r, &in)
	u := s.st.GetUser(user)
	if u == nil || u.IsFederated() || u.TOTPEnabled {
		jsonError(w, http.StatusBadRequest, "not available for this account")
		return
	}
	secret, err := s.userTOTPSecret(user)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "start the setup first")
		return
	}
	if !s.totpValid(user, secret, in.Code) {
		jsonError(w, http.StatusBadRequest, "that code is not right — check your authenticator's clock")
		return
	}
	codes, hashed := newRecoveryCodes()
	if err := s.st.EnableTOTP(user, hashed); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("2fa enabled for %s", user)
	// Shown once. They are password-equivalents, so only their hashes are kept.
	writeJSON(w, map[string]any{"ok": true, "recovery_codes": codes})
}

// POST /api/me/2fa/disable — turn 2FA off, re-proving a factor first (step-up).
func (s *Server) apiTOTPDisable(w http.ResponseWriter, r *http.Request, user string) {
	var in struct {
		Code string `json:"code"`
	}
	readJSON(r, &in)
	u := s.st.GetUser(user)
	if u == nil || !u.TOTPEnabled {
		jsonError(w, http.StatusBadRequest, "two-factor is not enabled")
		return
	}
	secret, err := s.userTOTPSecret(user)
	if err != nil || !(s.totpValid(user, secret, in.Code) || s.consumeRecoveryCode(user, in.Code)) {
		jsonError(w, http.StatusForbidden, "confirm with a current code first")
		return
	}
	if err := s.st.DisableTOTP(user); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("2fa disabled for %s", user)
	writeJSON(w, okJSON)
}

// POST /api/login/2fa — the second leg of a password login.
func (s *Server) apiLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
		Code  string `json:"code"`
	}
	readJSON(r, &in)
	req, ok := s.st.ConsumeAuthRequest(in.Token, time.Now())
	if !ok || req.Kind != "2fa" || req.Username == "" {
		jsonError(w, http.StatusUnauthorized, "that sign-in attempt has expired; start again")
		return
	}
	u := s.st.GetUser(req.Username)
	if u == nil || !u.Active || s.accountExpired(u) {
		jsonError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	// The throttle covers this leg too, keyed by account, so codes cannot be brute-forced by
	// replaying the first leg.
	key, now := "2fa:"+strings.ToLower(u.Username), time.Now()
	if s.loginThr != nil && s.loginThr.blocked(key, now) {
		jsonError(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}
	secret, err := s.userTOTPSecret(u.Username)
	if err != nil || !(s.totpValid(u.Username, secret, in.Code) || s.consumeRecoveryCode(u.Username, in.Code)) {
		if s.loginThr != nil {
			s.loginThr.record(key, now)
		}
		jsonError(w, http.StatusUnauthorized, "验证码不正确")
		return
	}
	if s.loginThr != nil {
		s.loginThr.reset(key)
	}
	s.setSessionCookie(w, r, *u)
	s.st.TouchLastLogin(u.Username)
	log.Printf("login %s (2fa)", u.Username)
	writeJSON(w, s.meJSON(u.Username))
}

// beginTOTPChallenge parks a password-verified login until a code is supplied. The pending token
// grants nothing on its own and is single-use, so it cannot be hoarded or replayed.
func (s *Server) beginTOTPChallenge(w http.ResponseWriter, username string) {
	token, err := newAuthToken()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal")
		return
	}
	if err := s.st.CreateAuthRequest(AuthRequest{
		Token: token, Kind: "2fa", Username: username,
	}, time.Now().Add(totpPendingTTL)); err != nil {
		jsonError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, map[string]any{"totp_required": true, "token": token})
}

// userTOTPSecret unseals a user's TOTP secret.
func (s *Server) userTOTPSecret(user string) (string, error) {
	enc := s.st.TOTPSecret(user)
	if enc == "" {
		return "", fmt.Errorf("no secret enrolled")
	}
	return s.openSecret(user, "totp_secret", enc)
}

// totpValid checks a code and burns its time step, so a code observed over someone's shoulder
// cannot be reused inside the ~30s window it is still arithmetically valid for.
func (s *Server) totpValid(user, secret, code string) bool {
	code = strings.TrimSpace(code)
	if secret == "" || code == "" {
		return false
	}
	// Find WHICH time step the code belongs to, rather than assuming the current one. Validation
	// allows ±1 step for clock drift, so a code from the previous step is legitimately accepted —
	// and burning "now" instead would both leave that earlier code replayable and wrongly consume
	// a step the user has not used yet.
	now := time.Now()
	matched, ok := int64(0), false
	for _, delta := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		at := now.Add(delta)
		want, err := totp.GenerateCode(secret, at)
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			matched, ok = at.Unix()/30, true
			break
		}
	}
	if !ok {
		return false
	}
	// The step marker rides in the same single-use table as every other one-shot token, so a code
	// cannot be replayed inside the window it stays arithmetically valid for.
	return s.st.MarkAssertionSeen("totp:"+user, fmt.Sprint(matched), now.Add(2*time.Minute))
}

// newRecoveryCodes returns the plaintext codes to show once, and the hashes to store. They are
// password-equivalents, so the plaintext is never persisted.
func newRecoveryCodes() (plain []string, hashed string) {
	var hashes []string
	for i := 0; i < recoveryCodeCount; i++ {
		b := make([]byte, 8)
		rand.Read(b)
		c := strings.ToLower(base64.RawURLEncoding.EncodeToString(b))
		plain = append(plain, c)
		hashes = append(hashes, hashRecoveryCode(c))
	}
	out, _ := json.Marshal(hashes)
	return plain, string(out)
}

func hashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.ToLower(code))))
	return hex.EncodeToString(sum[:])
}

// consumeRecoveryCode spends a recovery code, which is strictly single-use: the matching hash is
// removed before the caller is told it succeeded.
func (s *Server) consumeRecoveryCode(user, code string) bool {
	if strings.TrimSpace(code) == "" {
		return false
	}
	raw := s.st.RecoveryCodes(user)
	var hashes []string
	if err := json.Unmarshal([]byte(raw), &hashes); err != nil {
		return false
	}
	want := hashRecoveryCode(code)
	for i, h := range hashes {
		if subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1 {
			remaining := append(append([]string{}, hashes[:i]...), hashes[i+1:]...)
			out, _ := json.Marshal(remaining)
			// Conditional on the value we READ, so two concurrent uses of one code cannot both
			// win: the loser's compare-and-set finds the column already changed and fails. A
			// plain write here would make each code usable as many times as it is raced.
			ok, err := s.st.SwapRecoveryCodes(user, raw, string(out))
			if err != nil || !ok {
				return false
			}
			log.Printf("2fa recovery code used by %s (%d left)", user, len(remaining))
			return true
		}
	}
	return false
}

// ---------- store ----------

// TOTPSecret returns the sealed secret, or "" when the user has not enrolled.
func (s *Store) TOTPSecret(username string) string {
	var v sql.NullString
	s.queryRow(`SELECT totp_secret_enc FROM users WHERE username=?`, username).Scan(&v)
	return v.String
}

// SetTOTPSecret stores an enrolment secret WITHOUT switching 2FA on — confirm-before-enable.
func (s *Store) SetTOTPSecret(username, enc string) error {
	_, err := s.exec(`UPDATE users SET totp_secret_enc=?, updated_at=? WHERE username=?`, enc, nowStr(), username)
	return err
}

// EnableTOTP switches 2FA on once a code has been proven, and stores the hashed recovery codes.
func (s *Store) EnableTOTP(username, hashedCodes string) error {
	_, err := s.exec(`UPDATE users SET totp_enabled=1, totp_confirmed_at=?, recovery_codes=?, updated_at=? WHERE username=?`,
		nowStr(), hashedCodes, nowStr(), username)
	return err
}

// DisableTOTP clears every trace of the enrolment, so re-enabling starts from a fresh secret.
func (s *Store) DisableTOTP(username string) error {
	_, err := s.exec(`UPDATE users SET totp_enabled=0, totp_secret_enc=NULL, totp_confirmed_at=NULL,
		recovery_codes=NULL, updated_at=? WHERE username=?`, nowStr(), username)
	return err
}

func (s *Store) RecoveryCodes(username string) string {
	var v sql.NullString
	s.queryRow(`SELECT recovery_codes FROM users WHERE username=?`, username).Scan(&v)
	if v.String == "" {
		return "[]"
	}
	return v.String
}

func (s *Store) SetRecoveryCodes(username, hashedCodes string) error {
	_, err := s.exec(`UPDATE users SET recovery_codes=?, updated_at=? WHERE username=?`, hashedCodes, nowStr(), username)
	return err
}

// SwapRecoveryCodes replaces the code list only if it still holds the value the caller read. This
// is what makes spending a code single-use under concurrency: the write is conditional on the
// unchanged prior value, so of two racing attempts exactly one commits.
func (s *Store) SwapRecoveryCodes(username, from, to string) (bool, error) {
	res, err := s.exec(`UPDATE users SET recovery_codes=?, updated_at=? WHERE username=? AND COALESCE(recovery_codes,'[]')=?`,
		to, nowStr(), username, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// stepUpHeader carries the re-proved credential. A header, never the query string: the value is
// either the account password or a live TOTP/recovery code, and a query string is written verbatim
// into every reverse-proxy access log, kept in browser history, and sent in the Referer of any
// subresource the page loads. None of that is true of a header.
const stepUpHeader = "X-Step-Up-Proof"

// stepUpOK re-proves a factor inside an already-authenticated session, for actions that would
// otherwise let a stolen cookie become permanent access: registering or revoking a credential, and
// enrolling or turning off a factor. The caller supplies either the account password or a current
// 2FA/recovery code, so it works whether or not the account has a second factor enrolled.
//
// This is an online guessing oracle in exactly the way the login form is — the difference is only
// that the attacker already holds a session — so it shares the login form's lockout. Without that,
// a stolen cookie buys unlimited guesses at the password itself (reusable elsewhere) or at the
// ~3-in-10^6 live TOTP codes, which is hours of work, not an infeasible attack.
func (s *Server) stepUpOK(r *http.Request, user string) bool {
	proof := strings.TrimSpace(r.Header.Get(stepUpHeader))
	if proof == "" {
		return false
	}
	u := s.st.GetUser(user)
	if u == nil {
		return false
	}
	now := time.Now()
	key := "stepup:" + strings.ToLower(user)
	if s.loginThr != nil && s.loginThr.blocked(key, now) {
		return false
	}
	ok := s.stepUpProofValid(u, proof)
	if s.loginThr != nil {
		if ok {
			s.loginThr.reset(key)
		} else {
			s.loginThr.record(key, now)
		}
	}
	return ok
}

func (s *Server) stepUpProofValid(u *User, proof string) bool {
	if u.TOTPEnabled {
		secret, err := s.userTOTPSecret(u.Username)
		return err == nil && (s.totpValid(u.Username, secret, proof) || s.consumeRecoveryCode(u.Username, proof))
	}
	// No second factor enrolled yet: the password is the strongest thing the user has. A federated
	// account has none, so it cannot step up this way and must not be able to add local credentials.
	if u.IsFederated() || u.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(proof)) == nil
}

// requireStepUp is the handler-side guard. Every credential change goes through it, so the list of
// protected actions is visible in one place instead of being a property of whoever remembered.
func (s *Server) requireStepUp(w http.ResponseWriter, r *http.Request, user string) bool {
	if s.stepUpOK(r, user) {
		return true
	}
	jsonError(w, http.StatusForbidden, "confirm with your password or a current code first")
	return false
}
