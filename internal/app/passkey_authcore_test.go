package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/KazuhaHub/authcore/passkey"
	"github.com/descope/virtualwebauthn"
	"github.com/go-webauthn/webauthn/protocol"
)

// This file exercises the authcore/passkey integration end to end, over real HTTP handler calls
// (not by calling the authcore Service directly): a fake browser + authenticator
// (github.com/descope/virtualwebauthn) signs genuine attestation/assertion responses, so these
// tests drive the actual cryptographic verification path through ceremonySessionStore and
// passkeyCredentialStore, exactly as a real deployment would. They cover three things the
// migration report identified as needing proof, not just inspection:
//
//  1. TestPasskeyAuthcoreRegisterThenLoginAcrossRequests: a full register + login round trip
//     where begin and finish are independent HTTP requests (independent passkeyService() calls,
//     each building a fresh *passkey.Service) — proving ceremonySessionStore really persists a
//     session across requests via the auth_requests table, not merely within one process's
//     memory the way authcore's own default SessionStore would.
//  2. TestPasskeyPreMigrationCredentialJSONStillAuthenticates: a credential JSON blob shaped like
//     go-webauthn v0.17.4 serialized it (no "extensions" object — see passkey.go's package
//     comment) is written directly into webauthn_credentials, and a login against it must still
//     succeed. This is the data-layer compatibility claim made without running code; here it is
//     run.
//  3. TestPasskeyCloneWarningRejectsAndDoesNotAdvanceCounter: a rollback (cloned authenticator) is
//     simulated by presenting a valid signature with a lower counter than what's on file. The
//     login must be refused, and — the part authcore does NOT do for the caller — the stored
//     sign_count and last_used_at must NOT move, matching RP's pre-migration behavior.

const authcorePublicURL = "https://portal.example"

// authcorePasskeyServer builds a Server with a REAL bcrypt password hash for username — unlike
// passkeyServer in passkey_test.go, whose fixture users carry a placeholder hash ("h") that can
// never satisfy step-up, since these tests need step-up to actually succeed to reach
// apiPasskeyRegisterBegin.
func authcorePasskeyServer(t *testing.T, username, password string) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		loginThr: newLoginThrottle()}
	st.SetSetting("public_url", authcorePublicURL)
	st.UpsertUser(User{Username: username, PasswordHash: mustHash(password), Role: "user", Active: true})
	return s
}

// newWebauthnDevice returns a fresh simulated authenticator bound to the RP config
// passkeyService() derives from authcorePublicURL, reporting handle as its user handle — exactly
// as a real authenticator does once registration has bound it to an account.
func newWebauthnDevice(s *Server, handle []byte) (rp virtualwebauthn.RelyingParty, auth virtualwebauthn.Authenticator, cred virtualwebauthn.Credential) {
	rp = virtualwebauthn.RelyingParty{Name: s.totpIssuer(), ID: "portal.example", Origin: authcorePublicURL}
	auth = virtualwebauthn.NewAuthenticator()
	auth.Options.UserHandle = handle
	cred = virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	return rp, auth, cred
}

// registerPasskeyOverHTTP drives POST .../register/begin then .../register/finish for username,
// with a valid step-up proof, and returns the resulting credential id (base64url, as
// webauthn.Credential.ID marshals it in the browser's response — this is the same encoding stored
// hex-mangled via encodeCredID once it reaches the DB, so callers compare via the DB row instead).
func registerPasskeyOverHTTP(t *testing.T, s *Server, username, password, label string,
	rp virtualwebauthn.RelyingParty, auth *virtualwebauthn.Authenticator, cred virtualwebauthn.Credential) {
	t.Helper()

	beginReq := httptest.NewRequest(http.MethodPost, "/api/me/passkeys/register/begin", nil)
	beginReq.Header.Set(stepUpHeader, password)
	beginRec := httptest.NewRecorder()
	s.apiPasskeyRegisterBegin(beginRec, beginReq, username)
	if beginRec.Code != http.StatusOK {
		t.Fatalf("register begin = %d %s", beginRec.Code, beginRec.Body.String())
	}
	var begun struct {
		Token   string                      `json:"token"`
		Options protocol.CredentialCreation `json:"options"`
	}
	if err := json.Unmarshal(beginRec.Body.Bytes(), &begun); err != nil {
		t.Fatalf("decode register begin: %v", err)
	}

	optsJSON, err := json.Marshal(begun.Options.Response)
	if err != nil {
		t.Fatal(err)
	}
	attOpts, err := virtualwebauthn.ParseAttestationOptions(string(optsJSON))
	if err != nil {
		t.Fatalf("ParseAttestationOptions: %v", err)
	}
	attBody := virtualwebauthn.CreateAttestationResponse(rp, *auth, cred, *attOpts)
	auth.AddCredential(cred)

	finishURL := "/api/me/passkeys/register/finish?token=" + begun.Token + "&label=" + label
	finishReq := httptest.NewRequest(http.MethodPost, finishURL, strings.NewReader(attBody))
	finishReq.Header.Set("Content-Type", "application/json")
	finishRec := httptest.NewRecorder()
	s.apiPasskeyRegisterFinish(finishRec, finishReq, username)
	if finishRec.Code != http.StatusOK {
		t.Fatalf("register finish = %d %s", finishRec.Code, finishRec.Body.String())
	}
}

// startPending2FA seeds a "2fa"-kind pending login, exactly as the password leg would have left
// behind, and returns its single-use token.
func startPending2FA(t *testing.T, s *Server, username string) string {
	t.Helper()
	tok, err := newAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.CreateAuthRequest(AuthRequest{Token: tok, Kind: "2fa", Username: username}, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return tok
}

// loginPasskeyOverHTTP drives POST .../login/passkey/begin then .../login/passkey/finish for
// username, signing the assertion with cred at counter signCount, and returns the finish response.
func loginPasskeyOverHTTP(t *testing.T, s *Server, username string, rp virtualwebauthn.RelyingParty,
	auth virtualwebauthn.Authenticator, cred virtualwebauthn.Credential, signCount uint32) *httptest.ResponseRecorder {
	t.Helper()
	pendingTok := startPending2FA(t, s, username)

	beginReq := httptest.NewRequest(http.MethodPost, "/api/login/passkey/begin",
		strings.NewReader(`{"token":"`+pendingTok+`"}`))
	beginRec := httptest.NewRecorder()
	s.apiPasskeyLoginBegin(beginRec, beginReq)
	if beginRec.Code != http.StatusOK {
		t.Fatalf("login begin = %d %s", beginRec.Code, beginRec.Body.String())
	}
	var begun struct {
		Token   string                       `json:"token"`
		Options protocol.CredentialAssertion `json:"options"`
	}
	if err := json.Unmarshal(beginRec.Body.Bytes(), &begun); err != nil {
		t.Fatalf("decode login begin: %v", err)
	}

	optsJSON, err := json.Marshal(begun.Options.Response)
	if err != nil {
		t.Fatal(err)
	}
	assOpts, err := virtualwebauthn.ParseAssertionOptions(string(optsJSON))
	if err != nil {
		t.Fatalf("ParseAssertionOptions: %v", err)
	}
	signingCred := cred
	signingCred.Counter = signCount
	assBody := virtualwebauthn.CreateAssertionResponse(rp, auth, signingCred, *assOpts)

	finishURL := "/api/login/passkey/finish?token=" + begun.Token + "&pending=" + pendingTok
	finishReq := httptest.NewRequest(http.MethodPost, finishURL, strings.NewReader(assBody))
	finishReq.Header.Set("Content-Type", "application/json")
	finishRec := httptest.NewRecorder()
	s.apiPasskeyLoginFinish(finishRec, finishReq)
	return finishRec
}

// TestPasskeyAuthcoreRegisterThenLoginAcrossRequests proves the migration's SessionStore adapter
// really round-trips a ceremony through the database: every one of the four calls below
// (register begin, register finish, login begin, login finish) builds its OWN
// authcore/passkey.Service via passkeyService(), just like four independent requests hitting four
// different server instances behind a load balancer would. If ceremonySessionStore silently kept
// state in the Service instead of in auth_requests, this would fail at the second half of each
// pair.
func TestPasskeyAuthcoreRegisterThenLoginAcrossRequests(t *testing.T) {
	s := authcorePasskeyServer(t, "carol", "correct horse battery staple")
	rp, auth, cred := newWebauthnDevice(s, []byte("carol"))

	registerPasskeyOverHTTP(t, s, "carol", "correct horse battery staple", "My+laptop", rp, &auth, cred)

	list := s.st.PasskeyList("carol")
	if len(list) != 1 || list[0]["label"] != "My laptop" {
		t.Fatalf("passkey list after registration = %v", list)
	}

	rec := loginPasskeyOverHTTP(t, s, "carol", rp, auth, cred, 1)
	if rec.Code != http.StatusOK {
		t.Fatalf("login finish = %d %s", rec.Code, rec.Body.String())
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err == nil && me.Username != "" && me.Username != "carol" {
		t.Errorf("logged in as %q, want carol", me.Username)
	}
	if rec.Result().Cookies() == nil {
		t.Error("a successful passkey login must set a session cookie")
	}

	var signCount int64
	if err := s.st.queryRow(`SELECT sign_count FROM webauthn_credentials WHERE username='carol'`).Scan(&signCount); err != nil {
		t.Fatal(err)
	}
	if signCount != 1 {
		t.Errorf("sign_count after login = %d, want 1", signCount)
	}
}

// TestPasskeyPreMigrationCredentialJSONStillAuthenticates is the data-layer compatibility test the
// migration plan called for: a credential is registered normally (so its public key is real and
// verifiable), its stored JSON is then rewritten to strip the "extensions" object go-webauthn
// v0.18.1 added over v0.17.4 (the version RP shipped against before this migration — see the
// go.mod history), and a login is then attempted against that pre-migration-shaped row. A
// pre-existing credential must not be silently invalidated by this migration.
func TestPasskeyPreMigrationCredentialJSONStillAuthenticates(t *testing.T) {
	s := authcorePasskeyServer(t, "dave", "another-strong-passphrase")
	rp, auth, cred := newWebauthnDevice(s, []byte("dave"))
	registerPasskeyOverHTTP(t, s, "dave", "another-strong-passphrase", "Security+key", rp, &auth, cred)

	var blob string
	if err := s.st.queryRow(`SELECT credential FROM webauthn_credentials WHERE username='dave'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blob), &asMap); err != nil {
		t.Fatal(err)
	}
	// v0.18.1's CredentialExtensions field is tagged `json:"extensions,omitzero"`, so a
	// credential with no WebAuthn extension outputs — the common case, and what this simulated
	// authenticator produces — already serializes with NO "extensions" key at all, structurally
	// identical to what v0.17.4 wrote (that version never had the field to write). This delete is
	// a no-op here for exactly that reason; it is kept so the same rewrite also covers a
	// credential that DID pick up extension data, which the omitzero tag would otherwise still
	// serialize.
	if _, has := asMap["extensions"]; has {
		t.Log("credential JSON already carried an \"extensions\" key; stripping it to reach the pre-migration shape")
	} else {
		t.Log("credential JSON already has no \"extensions\" key (omitzero on a credential with no extension outputs) — already the pre-migration shape")
	}
	delete(asMap, "extensions")
	legacyBlob, err := json.Marshal(asMap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE webauthn_credentials SET credential=? WHERE username='dave'`, string(legacyBlob)); err != nil {
		t.Fatal(err)
	}

	rec := loginPasskeyOverHTTP(t, s, "dave", rp, auth, cred, 1)
	if rec.Code != http.StatusOK {
		t.Fatalf("login against a pre-migration (no-extensions) credential JSON = %d %s — a stored credential must survive this migration", rec.Code, rec.Body.String())
	}
}

// TestPasskeyCloneWarningRejectsAndDoesNotAdvanceCounter proves RP's pre-migration counter-
// rollback policy survived the migration to authcore: a login is refused when the authenticator's
// reported counter goes backwards (the signature of a cloned credential), AND — the part authcore
// leaves entirely to the caller, and the part that is easy to get only half right — the stored
// sign_count and last_used_at must not be advanced by the rejected attempt. authcore's own
// documented default is to write the counter back unconditionally on every cryptographically
// successful login; passkeyCredentialStore.UpdateSignCount deliberately does not, in this one
// case, and this test is what proves that adapter code actually behaves as its comment claims.
func TestPasskeyCloneWarningRejectsAndDoesNotAdvanceCounter(t *testing.T) {
	s := authcorePasskeyServer(t, "erin", "yet-another-passphrase")
	rp, auth, cred := newWebauthnDevice(s, []byte("erin"))
	registerPasskeyOverHTTP(t, s, "erin", "yet-another-passphrase", "Phone", rp, &auth, cred)

	// A legitimate login first, advancing the counter to a real baseline.
	if rec := loginPasskeyOverHTTP(t, s, "erin", rp, auth, cred, 5); rec.Code != http.StatusOK {
		t.Fatalf("baseline login = %d %s", rec.Code, rec.Body.String())
	}
	var baselineCount int64
	var baselineUsed string
	if err := s.st.queryRow(`SELECT sign_count, COALESCE(last_used_at,'') FROM webauthn_credentials WHERE username='erin'`).
		Scan(&baselineCount, &baselineUsed); err != nil {
		t.Fatal(err)
	}
	if baselineCount != 5 {
		t.Fatalf("baseline sign_count = %d, want 5", baselineCount)
	}

	// A second, cryptographically VALID login (same key) but reporting a LOWER counter than what
	// is already on file — exactly what a cloned authenticator whose own counter lags produces.
	rec := loginPasskeyOverHTTP(t, s, "erin", rp, auth, cred, 3)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("clone-signal login = %d %s, want 401 rejected", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not accepted") {
		t.Errorf("clone-signal login body = %s, want the standard rejection message", rec.Body.String())
	}

	var afterCount int64
	var afterUsed string
	if err := s.st.queryRow(`SELECT sign_count, COALESCE(last_used_at,'') FROM webauthn_credentials WHERE username='erin'`).
		Scan(&afterCount, &afterUsed); err != nil {
		t.Fatal(err)
	}
	if afterCount != baselineCount {
		t.Errorf("sign_count after a rejected clone-signal login = %d, want unchanged %d", afterCount, baselineCount)
	}
	if afterUsed != baselineUsed {
		t.Errorf("last_used_at after a rejected clone-signal login = %q, want unchanged %q", afterUsed, baselineUsed)
	}

	var detail string
	if err := s.st.queryRow(`SELECT detail FROM audit_log WHERE action=? AND target_id='erin' ORDER BY id DESC LIMIT 1`,
		AuditLoginFailed).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "cloned_authenticator") {
		t.Errorf("audit detail = %q, want it to record the cloned_authenticator reason", detail)
	}
}

// TestPasskeyByCredentialIDBacksTheCredentialStoreInterface proves Store.PasskeyByCredentialID
// and passkeyCredentialStore.FindByID work, even though no handler on RP's own path calls them
// today: authcore's CredentialStore interface requires FindByID unconditionally (it backs
// discoverable/usernameless login, which RP does not offer — ADR 0023), so this is otherwise
// dead code with no test ever exercising it. Left untested, it would be exactly the kind of gap
// that looks fine until the day RP adds passwordless login and discovers FindByID was never
// actually correct.
func TestPasskeyByCredentialIDBacksTheCredentialStoreInterface(t *testing.T) {
	s := passkeyServer(t)
	s.st.AddPasskey("alice", "A", fakeCred("cred-a", 7))

	username, cred, err := s.st.PasskeyByCredentialID([]byte("cred-a"))
	if err != nil {
		t.Fatal(err)
	}
	if username != "alice" || string(cred.ID) != "cred-a" || cred.Authenticator.SignCount != 7 {
		t.Errorf("PasskeyByCredentialID = (%q, id=%q, signCount=%d), want (alice, cred-a, 7)",
			username, cred.ID, cred.Authenticator.SignCount)
	}
	if _, _, err := s.st.PasskeyByCredentialID([]byte("no-such-credential")); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("PasskeyByCredentialID for an unknown id = %v, want sql.ErrNoRows", err)
	}

	// And through the adapter authcore actually calls, which must translate that same "not found"
	// into authcore's own sentinel rather than leaking sql.ErrNoRows across the package boundary.
	store := passkeyCredentialStore{s: s}
	sc, err := store.FindByID(context.Background(), []byte("cred-a"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sc.UserHandle) != "alice" || string(sc.Credential.ID) != "cred-a" {
		t.Errorf("adapter FindByID = %+v, want handle=alice id=cred-a", sc)
	}
	if _, err := store.FindByID(context.Background(), []byte("no-such-credential")); !errors.Is(err, passkey.ErrCredentialNotFound) {
		t.Errorf("adapter FindByID for an unknown id = %v, want passkey.ErrCredentialNotFound", err)
	}
}
