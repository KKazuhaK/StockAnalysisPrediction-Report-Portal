package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkeys / WebAuthn (ADR 0023). Registered per user, several allowed, each labelled and
// revocable. In v1 a passkey is a SECOND factor (user verification "preferred"); passwordless is
// deliberately left to a later change so that requiring discoverable credentials and
// userVerification=required is a decision rather than an accident.
//
// The Relying Party ID derives from the same publicBaseURL() everything else uses. This is
// load-bearing: an RP ID that changes silently invalidates every credential already registered,
// which looks to users like their passkeys "just stopped working".

const passkeyChallengeTTL = 5 * time.Minute

// passkeyUser adapts an account to the library's user interface.
type passkeyUser struct {
	name  string
	creds []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return []byte(u.name) }
func (u passkeyUser) WebAuthnName() string                       { return u.name }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.name }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// webAuthn builds the relying party from the public URL.
func (s *Server) webAuthn() (*webauthn.WebAuthn, error) {
	base := s.publicBaseURL()
	if base == "" {
		return nil, fmt.Errorf("set the Public URL before using passkeys")
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("the Public URL is not a valid origin")
	}
	return webauthn.New(&webauthn.Config{
		RPDisplayName: s.totpIssuer(),
		RPID:          u.Hostname(),
		RPOrigins:     []string{base},
	})
}

func (s *Server) passkeyUser(username string) (passkeyUser, error) {
	creds, err := s.st.PasskeyCredentials(username)
	if err != nil {
		return passkeyUser{}, err
	}
	return passkeyUser{name: username, creds: creds}, nil
}

// POST /api/me/passkeys/register/begin
func (s *Server) apiPasskeyRegisterBegin(w http.ResponseWriter, r *http.Request, user string) {
	// Step-up: a live session alone must not be enough to add a credential. Otherwise a stolen
	// cookie becomes permanent access — the attacker registers their own authenticator and keeps
	// getting in after the cookie is revoked and the password changed.
	if !s.stepUpOK(r, user) {
		jsonError(w, http.StatusForbidden, "confirm with your password or a current code first")
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	pu, err := s.passkeyUser(user)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Exclude what is already registered, so a second tap on the same authenticator is reported
	// by the browser instead of silently creating a duplicate.
	opts, session, err := wa.BeginRegistration(pu, webauthn.WithExclusions(credentialDescriptors(pu.creds)))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := s.stashCeremony(user, "webauthn-reg", session)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "token": token, "options": opts})
}

// POST /api/me/passkeys/register/finish
func (s *Server) apiPasskeyRegisterFinish(w http.ResponseWriter, r *http.Request, user string) {
	token := r.URL.Query().Get("token")
	session, ok := s.takeCeremony(token, "webauthn-reg", user)
	if !ok {
		jsonError(w, http.StatusBadRequest, "that registration attempt has expired; start again")
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	pu, err := s.passkeyUser(user)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cred, err := wa.FinishRegistration(pu, *session, r)
	if err != nil {
		log.Printf("passkey: registration for %s rejected: %v", user, err)
		jsonError(w, http.StatusBadRequest, "that passkey could not be registered")
		return
	}
	label := strings.TrimSpace(r.URL.Query().Get("label"))
	if err := s.st.AddPasskey(user, firstNonEmpty(label, "Passkey"), cred); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("passkey registered for %s", user)
	writeJSON(w, okJSON)
}

// POST /api/login/passkey/begin — offer a passkey as the SECOND factor of a password login.
//
// This is deliberately not a passwordless entry point. A passkey here is registered with user
// verification "preferred", so it may be possession-only; accepting it as the sole credential would
// be weaker than the password it replaced. The caller must therefore present the single-use token
// from a completed password leg, exactly like the TOTP step. Passwordless is a later change, and it
// needs discoverable credentials plus userVerification=required to be made deliberately.
func (s *Server) apiPasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	readJSON(r, &in)
	// Peek at the pending login without consuming it — the ceremony below can still fail, and
	// burning the token here would force the user back to the password screen every time.
	pending, ok := s.st.PeekAuthRequest(in.Token, time.Now())
	if !ok || pending.Kind != "2fa" || pending.Username == "" {
		jsonError(w, http.StatusUnauthorized, "that sign-in attempt has expired; start again")
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	pu, err := s.passkeyUser(pending.Username)
	// A user with no passkeys and an unknown user must look identical, or this becomes an
	// account-enumeration oracle.
	if err != nil || len(pu.creds) == 0 {
		jsonError(w, http.StatusUnauthorized, "no passkey is registered for that account")
		return
	}
	opts, session, err := wa.BeginLogin(pu)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := s.stashCeremony(pu.name, "webauthn-login", session)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "token": token, "pending": in.Token, "options": opts})
}

// POST /api/login/passkey/finish — completes the second factor and consumes the password leg.
func (s *Server) apiPasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	session, ok := s.takeCeremonyAny(token, "webauthn-login")
	if !ok {
		jsonError(w, http.StatusUnauthorized, "that sign-in attempt has expired; start again")
		return
	}
	// Consume the password leg here, not at begin: this is the point of no return, and it must be
	// single-use so a completed ceremony cannot be replayed into a second session.
	pending, ok := s.st.ConsumeAuthRequest(r.URL.Query().Get("pending"), time.Now())
	if !ok || pending.Kind != "2fa" || pending.Username != string(session.UserID) {
		jsonError(w, http.StatusUnauthorized, "that sign-in attempt has expired; start again")
		return
	}
	username := string(session.UserID)
	u := s.st.GetUser(username)
	if u == nil || !u.Active || s.accountExpired(u) {
		jsonError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	pu, err := s.passkeyUser(username)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cred, err := wa.FinishLogin(pu, *session, r)
	if err != nil {
		log.Printf("passkey: login for %s rejected: %v", username, err)
		jsonError(w, http.StatusUnauthorized, "that passkey was not accepted")
		return
	}
	// A sign counter that goes BACKWARDS is the one thing the counter exists to detect: it means
	// the credential has been cloned. Refuse and tell the operator.
	if cred.Authenticator.CloneWarning {
		log.Printf("passkey: CLONE WARNING for %s — the authenticator's sign counter went backwards", username)
		jsonError(w, http.StatusUnauthorized, "that passkey was not accepted")
		return
	}
	s.st.TouchPasskey(cred.ID, cred.Authenticator.SignCount)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.signUser(*u), Path: "/",
		HttpOnly: true, Secure: requestIsHTTPS(r, s.trustedNets),
		SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	s.st.TouchLastLogin(username)
	log.Printf("login %s (passkey)", username)
	writeJSON(w, s.meJSON(username))
}

// GET /api/me/passkeys — list, so a user can see and revoke what is registered.
func (s *Server) apiPasskeyList(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"passkeys": s.st.PasskeyList(user)})
}

// DELETE /api/me/passkeys/{id}
func (s *Server) apiPasskeyDelete(w http.ResponseWriter, r *http.Request, user string) {
	if err := s.st.DeletePasskey(user, pathID(r, "id")); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, okJSON)
}

// stashCeremony parks a WebAuthn session in the same single-use table as every other one-shot
// token, so the challenge is server-side, restart-safe and unusable twice.
func (s *Server) stashCeremony(username, kind string, session *webauthn.SessionData) (string, error) {
	token, err := newAuthToken()
	if err != nil {
		return "", err
	}
	blob, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	return token, s.st.CreateAuthRequest(AuthRequest{
		Token: token, Kind: kind, Username: username, Nonce: string(blob),
	}, time.Now().Add(passkeyChallengeTTL))
}

func (s *Server) takeCeremony(token, kind, wantUser string) (*webauthn.SessionData, bool) {
	sd, ok := s.takeCeremonyAny(token, kind)
	if !ok || string(sd.UserID) != wantUser {
		return nil, false
	}
	return sd, true
}

func (s *Server) takeCeremonyAny(token, kind string) (*webauthn.SessionData, bool) {
	req, ok := s.st.ConsumeAuthRequest(token, time.Now())
	if !ok || req.Kind != kind {
		return nil, false
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal([]byte(req.Nonce), &sd); err != nil {
		return nil, false
	}
	return &sd, true
}

func credentialDescriptors(creds []webauthn.Credential) []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(creds))
	for _, c := range creds {
		out = append(out, c.Descriptor())
	}
	return out
}

// ---------- store ----------

// PasskeyCredentials loads a user's registered credentials for a ceremony.
func (s *Store) PasskeyCredentials(username string) ([]webauthn.Credential, error) {
	if username == "" {
		return nil, nil
	}
	// sign_count is read from the COLUMN, not from the stored blob: the blob is written once at
	// registration, so trusting it would compare every later ceremony against the registration-time
	// counter and make clone detection permanently useless.
	rows, err := s.query(`SELECT credential, COALESCE(sign_count,0) FROM webauthn_credentials WHERE username=? ORDER BY id`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webauthn.Credential
	for rows.Next() {
		var blob sql.NullString
		var signCount int64
		if rows.Scan(&blob, &signCount) != nil || blob.String == "" {
			continue
		}
		var c webauthn.Credential
		if json.Unmarshal([]byte(blob.String), &c) == nil {
			c.Authenticator.SignCount = uint32(signCount)
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// PasskeyList is the user-facing view: enough to recognise and revoke a key, never the key itself.
func (s *Store) PasskeyList(username string) []map[string]any {
	rows, err := s.query(`SELECT id,COALESCE(label,''),COALESCE(created_at,''),COALESCE(last_used_at,'')
		FROM webauthn_credentials WHERE username=? ORDER BY id`, username)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var label, created, used string
		if rows.Scan(&id, &label, &created, &used) == nil {
			out = append(out, map[string]any{"id": id, "label": label, "created_at": created, "last_used_at": used})
		}
	}
	return out
}

func (s *Store) AddPasskey(username, label string, cred *webauthn.Credential) error {
	blob, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	_, err = s.exec(`INSERT INTO webauthn_credentials(credential_id,username,label,credential,sign_count,created_at)
		VALUES(?,?,?,?,?,?)`, encodeCredID(cred.ID), username, label, string(blob),
		int64(cred.Authenticator.SignCount), nowStr())
	return err
}

// TouchPasskey records use and the new sign counter, which is what makes a later rollback
// detectable at all.
func (s *Store) TouchPasskey(credID []byte, signCount uint32) {
	s.exec(`UPDATE webauthn_credentials SET last_used_at=?, sign_count=? WHERE credential_id=?`,
		nowStr(), int64(signCount), encodeCredID(credID))
}

// DeletePasskey revokes one credential, scoped to its owner so an id from another account cannot
// be removed.
func (s *Store) DeletePasskey(username string, id int64) error {
	_, err := s.exec(`DELETE FROM webauthn_credentials WHERE id=? AND username=?`, id, username)
	return err
}

func encodeCredID(id []byte) string { return fmt.Sprintf("%x", id) }
