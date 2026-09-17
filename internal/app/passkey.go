package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KazuhaHub/authcore/passkey"
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
//
// The ceremony orchestration (Begin/Finish, session parking, counter write-back) is delegated to
// github.com/KazuhaHub/authcore/passkey. This file supplies the two things that package leaves to
// its caller: SessionStore (ceremonySessionStore, wrapping the single-use auth_requests table this
// codebase already uses for every other one-shot token) and CredentialStore
// (passkeyCredentialStore, wrapping webauthn_credentials). webAuthn(), passkeyUser() and
// credentialDescriptors() below are no longer on the production path — every handler goes through
// passkeyService() instead — but stay in place because passkey_test.go calls them directly to
// check the relying-party config and the identity adapter in isolation.

const passkeyChallengeTTL = 5 * time.Minute

// passkeyUser adapts an account to the library's user interface. Retained for
// passkey_test.go (TestPasskeyUserAdapterAndList, TestPasskeyRelyingPartyComesFromPublicURL); no
// longer used by any handler, which now goes through authcore's own identity adapter instead.
type passkeyUser struct {
	name  string
	creds []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return []byte(u.name) }
func (u passkeyUser) WebAuthnName() string                       { return u.name }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.name }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// webAuthn builds the relying party from the public URL. Retained for
// TestPasskeyRelyingPartyComesFromPublicURL, which asserts on wa.Config.RPID directly; production
// code derives the same relying-party configuration through passkeyService() instead.
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

// passkeyLabelKey carries a new credential's display label through to
// passkeyCredentialStore.Save. authcore's CredentialStore.Save takes only a StoredCredential
// (UserHandle + webauthn.Credential) — a passkey's label is account-model data with no
// WebAuthn-ceremony content, which authcore deliberately excludes from its API (see the passkey
// package doc, "Credential management"). Context is the only channel Save's signature leaves for
// it, so the label rides in on ctx from apiPasskeyRegisterFinish rather than a second write after
// the fact — see passkeyCredentialStore.Save for why a second write was rejected.
type passkeyLabelKey struct{}

// passkeyStoreError marks an error that originated in RP's own storage layer (a *Store method
// failure) rather than in the WebAuthn ceremony itself. authcore wraps every CredentialStore /
// SessionStore error it receives in its own fmt.Errorf before returning it from Begin/Finish, so
// by the time a handler sees the error it is otherwise indistinguishable from a cryptographic
// ceremony failure. Before this migration, RP's handlers called the store directly and could tell
// the two apart by ordering alone (load the store, check its error, only then call go-webauthn);
// wrapping storage errors in this type and unwrapping with errors.As in the handler recovers that
// same distinction — a store failure is a 500, a rejected ceremony is a 400/401 — through
// authcore's necessarily generic error wrapping.
type passkeyStoreError struct{ err error }

func (e *passkeyStoreError) Error() string { return e.err.Error() }
func (e *passkeyStoreError) Unwrap() error { return e.err }

// ceremonySessionStore implements authcore/passkey.SessionStore on top of the same single-use
// auth_requests table (via stashCeremony / takeCeremonyAny) every other one-shot token in this
// codebase already uses — restart-safe and shared across every instance against one Postgres,
// which authcore's own default in-memory SessionStore is neither (see the migration notes: a
// deployment that built one long-lived authcore Service per process, backed by the in-memory
// default, would lose every in-flight ceremony to the next instance an LB routed the Finish call
// to). kind is fixed per instance so one Service only ever reads back sessions it itself parked as
// that ceremony kind — passkeyService constructs a separate instance (and a separate Service) per
// ceremony kind for exactly this reason.
type ceremonySessionStore struct {
	s    *Server
	kind string
}

func (c ceremonySessionStore) Put(_ context.Context, data *webauthn.SessionData) (string, error) {
	return c.s.stashCeremony(string(data.UserID), c.kind, data)
}

func (c ceremonySessionStore) Take(_ context.Context, id string) (*webauthn.SessionData, bool) {
	return c.s.takeCeremonyAny(id, c.kind)
}

// passkeyCredentialStore implements authcore/passkey.CredentialStore on top of
// webauthn_credentials. The WebAuthn user handle IS the username — that mapping is not new here,
// it is simply made explicit: the pre-migration passkeyUser adapter already used
// []byte(u.name) as WebAuthnID, so existing credentials' handle semantics are unchanged by this
// migration.
type passkeyCredentialStore struct{ s *Server }

func (c passkeyCredentialStore) FindByID(_ context.Context, credentialID []byte) (passkey.StoredCredential, error) {
	username, cred, err := c.s.st.PasskeyByCredentialID(credentialID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return passkey.StoredCredential{}, passkey.ErrCredentialNotFound
		}
		return passkey.StoredCredential{}, &passkeyStoreError{err}
	}
	return passkey.StoredCredential{UserHandle: []byte(username), Credential: *cred}, nil
}

func (c passkeyCredentialStore) FindByUserHandle(_ context.Context, handle []byte) ([]passkey.StoredCredential, error) {
	creds, err := c.s.st.PasskeyCredentials(string(handle))
	if err != nil {
		return nil, &passkeyStoreError{err}
	}
	out := make([]passkey.StoredCredential, len(creds))
	for i, cr := range creds {
		out[i] = passkey.StoredCredential{UserHandle: handle, Credential: cr}
	}
	return out, nil
}

// Save persists a newly registered credential. The label travels in ctx (see passkeyLabelKey) so
// this is one write, not two: an earlier draft of this adapter saved a placeholder label here and
// UPDATEd it after the fact from apiPasskeyRegisterFinish, which left a real (if short) window
// where PasskeyList would show a blank label for a credential that had, from the caller's
// perspective, already finished registering. Threading the label through ctx avoids that window
// entirely at the cost of a context value carrying business data — a trade this package's
// mechanism/policy split forces on every caller that needs to persist policy-owned data alongside
// what Save is given, not something specific to this store.
func (c passkeyCredentialStore) Save(ctx context.Context, cred passkey.StoredCredential) error {
	label, _ := ctx.Value(passkeyLabelKey{}).(string)
	if err := c.s.st.AddPasskey(string(cred.UserHandle), firstNonEmpty(label, "Passkey"), &cred.Credential); err != nil {
		return &passkeyStoreError{err}
	}
	return nil
}

// UpdateSignCount persists a login's advanced authenticator state — except when cred carries a
// clone-rollback signal, in which case it deliberately does nothing.
//
// authcore's own package doc is explicit that go-webauthn's storage guidance is to write the
// counter back unconditionally, clone warning or not, and that is what authcore's Service does on
// every call to this method. RP's policy predates this migration and stays unchanged by it: a
// clone warning is refused outright by the caller (see apiPasskeyLoginFinish), and refusing it
// must not also advance the counter baseline stored for the credential — writing the cloned
// device's counter to disk would raise the bar the NEXT clone attempt has to clear, which defeats
// the rollback check the counter exists for in the first place. This is the one place this
// migration's behavior intentionally diverges from authcore's documented default, and it is done
// entirely on RP's side of the CredentialStore interface — authcore itself is untouched.
func (c passkeyCredentialStore) UpdateSignCount(_ context.Context, credentialID []byte, cred webauthn.Credential, _ time.Time) error {
	if cred.Authenticator.CloneWarning {
		return nil
	}
	c.s.st.TouchPasskey(credentialID, cred.Authenticator.SignCount)
	return nil
}

// passkeyService builds an authcore/passkey.Service scoped to one ceremony kind
// ("webauthn-reg" or "webauthn-login"), reading the relying-party configuration fresh on every
// call exactly like webAuthn() did before this migration. That freshness is load-bearing, not a
// leftover habit: an admin changing the Public URL must take effect on the next request without a
// restart, and passkeys must refuse outright when it is unset (TestPasskeyRelyingPartyComesFromPublicURL)
// rather than silently falling back to something guessed from the request. Rebuilding the Service
// per request is only safe because its session state lives in the auth_requests table via
// ceremonySessionStore, not in the Service itself — a deployment that paired "rebuild every
// request" with authcore's default in-memory SessionStore would lose every Begin the moment the
// matching Finish arrived, since it would almost never hit the same freshly-built Service.
func (s *Server) passkeyService(kind string) (*passkey.Service, error) {
	base := s.publicBaseURL()
	if base == "" {
		return nil, fmt.Errorf("set the Public URL before using passkeys")
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("the Public URL is not a valid origin")
	}
	return passkey.New(passkey.Config{
		RPID:          u.Hostname(),
		RPDisplayName: s.totpIssuer(),
		RPOrigins:     []string{base},
		Sessions:      ceremonySessionStore{s: s, kind: kind},
		Credentials:   passkeyCredentialStore{s: s},
	})
}

// POST /api/me/passkeys/register/begin
func (s *Server) apiPasskeyRegisterBegin(w http.ResponseWriter, r *http.Request, user string) {
	// Step-up: a live session alone must not be enough to add a credential. Otherwise a stolen
	// cookie becomes permanent access — the attacker registers their own authenticator and keeps
	// getting in after the cookie is revoked and the password changed.
	if !s.requireStepUp(w, r, user) {
		return
	}
	svc, err := s.passkeyService("webauthn-reg")
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	// authcore excludes what is already registered under this handle automatically (its own
	// FindByUserHandle + WithExclusions), so a second tap on the same authenticator is reported by
	// the browser instead of silently creating a duplicate — same behavior as before the migration.
	opts, token, err := svc.BeginRegistration(r.Context(), []byte(user), user, user)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "token": token, "options": opts})
}

// POST /api/me/passkeys/register/finish
func (s *Server) apiPasskeyRegisterFinish(w http.ResponseWriter, r *http.Request, user string) {
	token := r.URL.Query().Get("token")
	svc, err := s.passkeyService("webauthn-reg")
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	label := firstNonEmpty(strings.TrimSpace(r.URL.Query().Get("label")), "Passkey")
	ctx := context.WithValue(r.Context(), passkeyLabelKey{}, label)
	if _, err := svc.FinishRegistration(ctx, []byte(user), token, r); err != nil {
		if errors.Is(err, passkey.ErrSessionNotFound) {
			jsonError(w, http.StatusBadRequest, "that registration attempt has expired; start again")
			return
		}
		var storeErr *passkeyStoreError
		if errors.As(err, &storeErr) {
			jsonError(w, http.StatusInternalServerError, storeErr.Error())
			return
		}
		log.Printf("passkey: registration for %s rejected: %v", user, err)
		jsonError(w, http.StatusBadRequest, "that passkey could not be registered")
		return
	}
	s.recordAuth(r, AuditMFAChange, user, user, map[string]any{
		"factor": "passkey", "op": "add", "label": label})
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
	svc, err := s.passkeyService("webauthn-login")
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	opts, token, err := svc.BeginLogin(r.Context(), []byte(pending.Username))
	// A user with no passkeys and an unknown user must look identical, or this becomes an
	// account-enumeration oracle — every failure here (ErrNoCredentials, or a store error loading
	// them) collapses into the same response, exactly as it did before the migration.
	if err != nil {
		jsonError(w, http.StatusUnauthorized, "no passkey is registered for that account")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "token": token, "pending": in.Token, "options": opts})
}

// POST /api/login/passkey/finish — completes the second factor and consumes the password leg.
func (s *Server) apiPasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	pendingTok := r.URL.Query().Get("pending")
	// Peek the password leg first, same as apiPasskeyLoginBegin: it identifies whose ceremony this
	// is (the handle svc.FinishLogin needs) without spending the single use a garbage or stale
	// "pending" value would otherwise waste on a webauthn ceremony that was never going to complete
	// anyway. The password leg is not actually consumed until after the ceremony succeeds, below —
	// that is still the point of no return, it has just moved past the WebAuthn verification
	// instead of before it.
	pending, ok := s.st.PeekAuthRequest(pendingTok, time.Now())
	if !ok || pending.Kind != "2fa" || pending.Username == "" {
		jsonError(w, http.StatusUnauthorized, "that sign-in attempt has expired; start again")
		return
	}
	username := pending.Username
	u := s.st.GetUser(username)
	if u == nil || !u.Active || s.accountExpired(u) {
		jsonErrorCode(w, http.StatusUnauthorized, "bad_credentials", "用户名或密码错误")
		return
	}
	svc, err := s.passkeyService("webauthn-login")
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	// handle is username, taken from the already-verified password leg, not from the webauthn
	// session — go-webauthn cross-checks it against the handle the Begin call parked in the
	// session and fails the ceremony on a mismatch, which is exactly the cross-user check the
	// pre-migration code made by hand via takeCeremony's wantUser parameter.
	result, err := svc.FinishLogin(r.Context(), []byte(username), token, r)
	if err != nil {
		if errors.Is(err, passkey.ErrSessionNotFound) {
			jsonError(w, http.StatusUnauthorized, "that sign-in attempt has expired; start again")
			return
		}
		var storeErr *passkeyStoreError
		if errors.As(err, &storeErr) {
			jsonError(w, http.StatusInternalServerError, storeErr.Error())
			return
		}
		log.Printf("passkey: login for %s rejected: %v", username, err)
		s.recordAuth(r, AuditLoginFailed, "", username, map[string]any{"reason": "passkey_rejected"})
		jsonError(w, http.StatusUnauthorized, "that passkey was not accepted")
		return
	}
	// A sign counter that goes BACKWARDS is the one thing the counter exists to detect: it means
	// the credential has been cloned. Refuse and tell the operator. authcore surfaces this as a
	// flag on the result rather than deciding for us — see passkeyCredentialStore.UpdateSignCount
	// for how the stored counter baseline is kept from advancing in this same case — so the reject
	// decision itself is made right here, unchanged from before the migration.
	if result.Credential.Authenticator.CloneWarning {
		log.Printf("passkey: CLONE WARNING for %s — the authenticator's sign counter went backwards", username)
		// Its own reason, because this one is not a typo or a dismissed prompt: a counter that went
		// backwards means the credential exists in two places, and an operator has to see it.
		s.recordAuth(r, AuditLoginFailed, "", username, map[string]any{"reason": "cloned_authenticator"})
		jsonError(w, http.StatusUnauthorized, "that passkey was not accepted")
		return
	}
	// The point of no return: the ceremony verified, so the password leg is spent now, single-use,
	// exactly like every other one-shot token in this codebase.
	if _, ok := s.st.ConsumeAuthRequest(pendingTok, time.Now()); !ok {
		jsonError(w, http.StatusUnauthorized, "that sign-in attempt has expired; start again")
		return
	}
	s.setSessionCookie(w, r, *u)
	s.st.TouchLastLogin(username)
	s.recordAuth(r, AuditLogin, username, username, map[string]any{"method": "passkey"})
	log.Printf("login %s (passkey)", username)
	writeJSON(w, s.meJSON(username))
}

// GET /api/me/passkeys — list, so a user can see and revoke what is registered.
func (s *Server) apiPasskeyList(w http.ResponseWriter, r *http.Request, user string) {
	writeJSON(w, map[string]any{"passkeys": s.st.PasskeyList(user)})
}

// DELETE /api/me/passkeys/{id}
func (s *Server) apiPasskeyDelete(w http.ResponseWriter, r *http.Request, user string) {
	// Revoking a credential is a credential change too. Without step-up, a stolen cookie strips the
	// owner's authenticators — locking them out of the very factor that would have evicted the
	// attacker.
	if !s.requireStepUp(w, r, user) {
		return
	}
	if err := s.st.DeletePasskey(user, pathID(r, "id")); err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAuth(r, AuditMFAChange, user, user, map[string]any{
		"factor": "passkey", "op": "remove", "cred_id": pathID(r, "id")})
	writeJSON(w, okJSON)
}

// stashCeremony parks a WebAuthn session in the same single-use table as every other one-shot
// token, so the challenge is server-side, restart-safe and unusable twice. Called both by
// ceremonySessionStore (the production path, via authcore) and directly by
// TestPasskeyCeremonyIsSingleUse.
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

// takeCeremony additionally checks the ceremony belongs to wantUser. Retained for
// TestPasskeyCeremonyIsSingleUse; the production path no longer calls it directly — the
// equivalent cross-user check is now made by go-webauthn itself, inside svc.FinishRegistration /
// svc.FinishLogin, by comparing the handle a handler passes in against the session's own UserID.
func (s *Server) takeCeremony(token, kind, wantUser string) (*webauthn.SessionData, bool) {
	sd, ok := s.takeCeremonyAny(token, kind)
	if !ok || string(sd.UserID) != wantUser {
		return nil, false
	}
	return sd, true
}

// takeCeremonyAny is ceremonySessionStore.Take's implementation, and so is very much still on the
// production path (unlike takeCeremony above).
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

// credentialDescriptors is no longer used by any handler — authcore's BeginRegistration builds its
// own exclusion list internally — but TestPasskeyUserAdapterAndList calls it directly.
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

// PasskeyByCredentialID looks up a single credential by its raw credential id, across every user.
// Nothing on RP's own path calls this yet — RP does not offer discoverable ("usernameless")
// login (ADR 0023) — but authcore's CredentialStore interface requires FindByID unconditionally,
// and this is what backs it. If RP ever adds passwordless login, the lookup already exists.
func (s *Store) PasskeyByCredentialID(credID []byte) (username string, cred *webauthn.Credential, err error) {
	var blob sql.NullString
	var signCount int64
	err = s.queryRow(`SELECT username, credential, COALESCE(sign_count,0) FROM webauthn_credentials WHERE credential_id=?`,
		encodeCredID(credID)).Scan(&username, &blob, &signCount)
	if err != nil {
		return "", nil, err
	}
	if blob.String == "" {
		return "", nil, sql.ErrNoRows
	}
	var c webauthn.Credential
	if err := json.Unmarshal([]byte(blob.String), &c); err != nil {
		return "", nil, err
	}
	c.Authenticator.SignCount = uint32(signCount)
	return username, &c, nil
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
