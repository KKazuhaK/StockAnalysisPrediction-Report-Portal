package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/descope/virtualwebauthn"
	"github.com/go-webauthn/webauthn/protocol"
)

// This file is an INDEPENDENT verification pass over the authcore/passkey migration, written
// separately from passkey_authcore_test.go and deliberately not sharing its helpers, so a bug
// hidden by a shared assumption in the implementer's own harness would not also hide here.
//
// It checks two things the migration report either claimed without an independent witness, or
// did not call out as a distinct capability at all:
//
//  1. A pre-migration (go-webauthn v0.17.4-shaped) credential JSON, built field-by-field from the
//     documented v0.17.4 schema rather than by stripping a key out of a v0.18.1 credential, still
//     authenticates after the migration.
//  2. A REJECTED passkey login (bad assertion) does not burn the password-leg ("pending") token —
//     a behavior difference from pre-migration main this reviewer found while diffing passkey.go,
//     not something either the recon report or the migration report's capability table called out
//     as a checked line item.

func verifierServer(t *testing.T, username, password string) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		loginThr: newLoginThrottle()}
	st.SetSetting("public_url", "https://verify.example")
	st.UpsertUser(User{Username: username, PasswordHash: mustHash(password), Role: "user", Active: true})
	return s
}

func verifierPending2FA(t *testing.T, s *Server, username string) string {
	t.Helper()
	tok, err := newAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.CreateAuthRequest(AuthRequest{Token: tok, Kind: "2fa", Username: username},
		time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	return tok
}

// verifierRegister drives a real registration ceremony over HTTP and returns the row's raw
// stored JSON plus the simulated device, so the caller can later sign a login assertion against
// the same key material.
func verifierRegister(t *testing.T, s *Server, username, password string) (rawJSON string, rp virtualwebauthn.RelyingParty, auth *virtualwebauthn.Authenticator, cred virtualwebauthn.Credential) {
	t.Helper()

	rp = virtualwebauthn.RelyingParty{Name: s.totpIssuer(), ID: "verify.example", Origin: "https://verify.example"}
	a := virtualwebauthn.NewAuthenticator()
	auth = &a
	auth.Options.UserHandle = []byte(username)
	cred = virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)

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

	finishURL := "/api/me/passkeys/register/finish?token=" + begun.Token + "&label=verifier"
	finishReq := httptest.NewRequest(http.MethodPost, finishURL, strings.NewReader(attBody))
	finishReq.Header.Set("Content-Type", "application/json")
	finishRec := httptest.NewRecorder()
	s.apiPasskeyRegisterFinish(finishRec, finishReq, username)
	if finishRec.Code != http.StatusOK {
		t.Fatalf("register finish = %d %s", finishRec.Code, finishRec.Body.String())
	}

	if err := s.st.queryRow(`SELECT credential FROM webauthn_credentials WHERE username=?`, username).Scan(&rawJSON); err != nil {
		t.Fatal(err)
	}
	return rawJSON, rp, auth, cred
}

// verifierLegacyShapeJSON re-derives a go-webauthn v0.17.4-vintage credential record from a
// live (v0.18.1) one, by explicitly naming every field v0.17.4 actually had — see
// credential.go in both module versions — rather than by unmarshalling into the current struct
// and deleting whatever "extensions" key happens to be present. v0.17.4 never had an Extensions
// field at all, so a record it wrote could not contain that key under any circumstance; building
// the JSON from an explicit allow-list reproduces that fact directly instead of relying on the
// current struct's omitzero behavior to make deletion a no-op.
func verifierLegacyShapeJSON(t *testing.T, liveJSON string) string {
	t.Helper()
	var full map[string]json.RawMessage
	if err := json.Unmarshal([]byte(liveJSON), &full); err != nil {
		t.Fatalf("decode live credential JSON: %v", err)
	}
	// The exact field set webauthn.Credential carried in v0.17.4 (json tags from credential.go):
	// id, publicKey, attestationType, attestationFormat, transport, flags, authenticator,
	// attestation. No "extensions" key can appear here because that field did not exist yet.
	legacyFields := []string{
		"id", "publicKey", "attestationType", "attestationFormat",
		"transport", "flags", "authenticator", "attestation",
	}
	out := make(map[string]json.RawMessage, len(legacyFields))
	for _, f := range legacyFields {
		if v, ok := full[f]; ok {
			out[f] = v
		}
	}
	if _, ok := out["id"]; !ok {
		t.Fatal("live credential JSON had no \"id\" field — verifierLegacyShapeJSON's field list is stale")
	}
	if _, ok := full["extensions"]; ok {
		t.Log("note: the live v0.18.1 record DID carry an \"extensions\" key; the legacy reconstruction below omits it entirely, as a v0.17.4 writer would have")
	}
	legacyBlob, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyBlob), "extensions") {
		t.Fatal("legacy reconstruction unexpectedly contains an \"extensions\" key")
	}
	return string(legacyBlob)
}

func verifierLoginAttempt(t *testing.T, s *Server, username, pendingTok string, body string) *httptest.ResponseRecorder {
	t.Helper()
	beginReq := httptest.NewRequest(http.MethodPost, "/api/login/passkey/begin",
		strings.NewReader(`{"token":"`+pendingTok+`"}`))
	beginRec := httptest.NewRecorder()
	s.apiPasskeyLoginBegin(beginRec, beginReq)
	if beginRec.Code != http.StatusOK {
		t.Fatalf("login begin = %d %s", beginRec.Code, beginRec.Body.String())
	}
	var begun struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(beginRec.Body.Bytes(), &begun); err != nil {
		t.Fatalf("decode login begin: %v", err)
	}

	finishURL := "/api/login/passkey/finish?token=" + begun.Token + "&pending=" + pendingTok
	finishReq := httptest.NewRequest(http.MethodPost, finishURL, strings.NewReader(body))
	finishReq.Header.Set("Content-Type", "application/json")
	finishRec := httptest.NewRecorder()
	s.apiPasskeyLoginFinish(finishRec, finishReq)
	return finishRec
}

// TestVerifierLegacyCredentialJSONAuthenticates is this reviewer's own, independently-constructed
// stale-credential compatibility check (task instruction: "自己再写一个独立的测试，不复用实现者那个").
// It rebuilds a v0.17.4-shaped record by explicit field allow-list rather than by deleting a key
// from a v0.18.1 record, and drives the login entirely through this file's own HTTP helpers.
func TestVerifierLegacyCredentialJSONAuthenticates(t *testing.T) {
	s := verifierServer(t, "frank", "verifier-passphrase-one")
	liveJSON, rp, auth, cred := verifierRegister(t, s, "frank", "verifier-passphrase-one")

	legacyJSON := verifierLegacyShapeJSON(t, liveJSON)
	if _, err := s.st.exec(`UPDATE webauthn_credentials SET credential=? WHERE username='frank'`, legacyJSON); err != nil {
		t.Fatal(err)
	}

	// Confirm the row really is in the legacy shape before trusting a login against it.
	var stored string
	if err := s.st.queryRow(`SELECT credential FROM webauthn_credentials WHERE username='frank'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "extensions") {
		t.Fatalf("stored credential JSON unexpectedly contains \"extensions\": %s", stored)
	}
	if !strings.Contains(stored, "\"id\"") || !strings.Contains(stored, "\"publicKey\"") {
		t.Fatalf("stored credential JSON missing expected legacy fields: %s", stored)
	}

	pendingTok := verifierPending2FA(t, s, "frank")
	beginReq := httptest.NewRequest(http.MethodPost, "/api/login/passkey/begin",
		strings.NewReader(`{"token":"`+pendingTok+`"}`))
	beginRec := httptest.NewRecorder()
	s.apiPasskeyLoginBegin(beginRec, beginReq)
	if beginRec.Code != http.StatusOK {
		t.Fatalf("login begin against a legacy-shaped stored credential = %d %s", beginRec.Code, beginRec.Body.String())
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
	// Sign with an advanced counter: the credential's registration-time counter is 0, and go-
	// webauthn never flags an all-zero counter as a clone signal (a real authenticator without a
	// counter legitimately reports 0 forever) — advancing it here is what makes "sign_count moved"
	// a meaningful assertion below, rather than trivially true at the starting value.
	signingCred := cred
	signingCred.Counter = 4
	assBody := virtualwebauthn.CreateAssertionResponse(rp, *auth, signingCred, *assOpts)

	finishURL := "/api/login/passkey/finish?token=" + begun.Token + "&pending=" + pendingTok
	finishReq := httptest.NewRequest(http.MethodPost, finishURL, strings.NewReader(assBody))
	finishReq.Header.Set("Content-Type", "application/json")
	finishRec := httptest.NewRecorder()
	s.apiPasskeyLoginFinish(finishRec, finishReq)
	if finishRec.Code != http.StatusOK {
		t.Fatalf("login finish against a legacy-shaped stored credential = %d %s — a pre-migration credential must still authenticate",
			finishRec.Code, finishRec.Body.String())
	}
	if finishRec.Result().Cookies() == nil {
		t.Error("a successful legacy-credential passkey login must set a session cookie")
	}

	var signCount int64
	if err := s.st.queryRow(`SELECT sign_count FROM webauthn_credentials WHERE username='frank'`).Scan(&signCount); err != nil {
		t.Fatal(err)
	}
	if signCount != 4 {
		t.Errorf("sign_count after a successful login against a legacy-shaped stored credential = %d, want 4", signCount)
	}
}

// TestVerifierRejectedLoginDoesNotBurnPendingToken is a behavior-DIFFERENCE check, not a
// regurgitation of what the migration report already claimed. Pre-migration main's
// apiPasskeyLoginFinish called s.st.ConsumeAuthRequest on the password-leg "pending" token
// immediately after taking the webauthn ceremony session and BEFORE calling wa.FinishLogin — so
// even a bad/failed assertion burned the password leg, forcing the user back to the password
// screen. The migrated code (see the "point of no return" comment in apiPasskeyLoginFinish) only
// peeks the pending token before FinishLogin and consumes it after a successful ceremony. Neither
// the recon report nor the migration report's capability table names this as a checked line item;
// this test exists because diffing passkey.go against main surfaced it independently, and it must
// be verified rather than assumed benign.
func TestVerifierRejectedLoginDoesNotBurnPendingToken(t *testing.T) {
	s := verifierServer(t, "grace", "verifier-passphrase-two")
	// Register a real passkey so BeginLogin has something to build an allow-list from — otherwise
	// the ceremony never starts and this test would not actually exercise FinishLogin at all.
	verifierRegister(t, s, "grace", "verifier-passphrase-two")

	pendingTok := verifierPending2FA(t, s, "grace")

	// A garbage assertion body: passes login/begin (which only needs a valid pending + enrolled
	// credentials), but FinishLogin must reject it — it is neither valid CBOR/JSON nor signed by
	// any enrolled key.
	garbage := `{"id":"` + base64.RawURLEncoding.EncodeToString([]byte("not-a-real-assertion")) +
		`","rawId":"AA","type":"public-key","response":{"clientDataJSON":"AA","authenticatorData":"AA","signature":"AA"}}`

	rec := verifierLoginAttempt(t, s, "grace", pendingTok, garbage)
	if rec.Code == http.StatusOK {
		t.Fatalf("a garbage assertion must not be accepted as a successful login: %d %s", rec.Code, rec.Body.String())
	}

	// The pre-migration behavior this test guards against: pending gets consumed by the failed
	// attempt above, so a retry — even with a perfectly valid signature — would find "no such
	// pending login" and force the user back to re-entering their password. Assert the opposite:
	// pending must still be there, unconsumed, exactly once.
	if _, ok := s.st.PeekAuthRequest(pendingTok, time.Now()); !ok {
		t.Fatal("a rejected passkey attempt consumed the password-leg pending token — " +
			"this is a regression from pre-migration main, which only consumed it on a " +
			"successful FinishLogin (see apiPasskeyLoginFinish's \"point of no return\" comment)")
	}

	// And prove it is not merely present but functionally alive: a second, GENUINE login attempt
	// against the SAME pending token must still be able to complete and consume it exactly once.
	_, rp, auth, cred := verifierRegisterSecondDevice(t, s, "grace")
	beginReq := httptest.NewRequest(http.MethodPost, "/api/login/passkey/begin",
		strings.NewReader(`{"token":"`+pendingTok+`"}`))
	beginRec := httptest.NewRecorder()
	s.apiPasskeyLoginBegin(beginRec, beginReq)
	if beginRec.Code != http.StatusOK {
		t.Fatalf("second login begin on the surviving pending token = %d %s", beginRec.Code, beginRec.Body.String())
	}
	var begun struct {
		Token   string                       `json:"token"`
		Options protocol.CredentialAssertion `json:"options"`
	}
	if err := json.Unmarshal(beginRec.Body.Bytes(), &begun); err != nil {
		t.Fatalf("decode second login begin: %v", err)
	}
	optsJSON, err := json.Marshal(begun.Options.Response)
	if err != nil {
		t.Fatal(err)
	}
	assOpts, err := virtualwebauthn.ParseAssertionOptions(string(optsJSON))
	if err != nil {
		t.Fatalf("ParseAssertionOptions: %v", err)
	}
	assBody := virtualwebauthn.CreateAssertionResponse(rp, auth, cred, *assOpts)
	finishURL := "/api/login/passkey/finish?token=" + begun.Token + "&pending=" + pendingTok
	finishReq := httptest.NewRequest(http.MethodPost, finishURL, strings.NewReader(assBody))
	finishReq.Header.Set("Content-Type", "application/json")
	finishRec := httptest.NewRecorder()
	s.apiPasskeyLoginFinish(finishRec, finishReq)
	if finishRec.Code != http.StatusOK {
		t.Fatalf("genuine retry on the surviving pending token = %d %s, want 200", finishRec.Code, finishRec.Body.String())
	}

	// Now it really must be gone — single-use is still enforced, just moved to success time.
	if _, ok := s.st.PeekAuthRequest(pendingTok, time.Now()); ok {
		t.Error("pending token must be consumed exactly once, after the successful retry")
	}
}

// verifierRegisterSecondDevice registers a second passkey for username and returns the device
// used, so TestVerifierRejectedLoginDoesNotBurnPendingToken can sign a genuine follow-up assertion
// without touching the first (already-consumed-ceremony) device.
func verifierRegisterSecondDevice(t *testing.T, s *Server, username string) (rawJSON string, rp virtualwebauthn.RelyingParty, auth virtualwebauthn.Authenticator, cred virtualwebauthn.Credential) {
	t.Helper()
	rp = virtualwebauthn.RelyingParty{Name: s.totpIssuer(), ID: "verify.example", Origin: "https://verify.example"}
	auth = virtualwebauthn.NewAuthenticator()
	auth.Options.UserHandle = []byte(username)
	cred = virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)

	beginReq := httptest.NewRequest(http.MethodPost, "/api/me/passkeys/register/begin", nil)
	// Any registered user in this test suite is created with a real bcrypt hash via mustHash, and
	// step-up in these tests checks the SAME password used at UpsertUser time; the caller already
	// knows it (it is "verifier-passphrase-two" for "grace"), so require it explicitly here too.
	beginReq.Header.Set(stepUpHeader, "verifier-passphrase-two")
	beginRec := httptest.NewRecorder()
	s.apiPasskeyRegisterBegin(beginRec, beginReq, username)
	if beginRec.Code != http.StatusOK {
		t.Fatalf("second-device register begin = %d %s", beginRec.Code, beginRec.Body.String())
	}
	var begun struct {
		Token   string                      `json:"token"`
		Options protocol.CredentialCreation `json:"options"`
	}
	if err := json.Unmarshal(beginRec.Body.Bytes(), &begun); err != nil {
		t.Fatalf("decode second-device register begin: %v", err)
	}
	optsJSON, err := json.Marshal(begun.Options.Response)
	if err != nil {
		t.Fatal(err)
	}
	attOpts, err := virtualwebauthn.ParseAttestationOptions(string(optsJSON))
	if err != nil {
		t.Fatalf("ParseAttestationOptions: %v", err)
	}
	attBody := virtualwebauthn.CreateAttestationResponse(rp, auth, cred, *attOpts)
	auth.AddCredential(cred)

	finishURL := fmt.Sprintf("/api/me/passkeys/register/finish?token=%s&label=second-device", begun.Token)
	finishReq := httptest.NewRequest(http.MethodPost, finishURL, strings.NewReader(attBody))
	finishReq.Header.Set("Content-Type", "application/json")
	finishRec := httptest.NewRecorder()
	s.apiPasskeyRegisterFinish(finishRec, finishReq, username)
	if finishRec.Code != http.StatusOK {
		t.Fatalf("second-device register finish = %d %s", finishRec.Code, finishRec.Body.String())
	}
	if err := s.st.queryRow(`SELECT credential FROM webauthn_credentials WHERE username=? AND label='second-device'`, username).Scan(&rawJSON); err != nil {
		t.Fatal(err)
	}
	return rawJSON, rp, auth, cred
}

// TestVerifierPasskeyServiceRejectsUnconfiguredPublicURL independently exercises the PRODUCTION
// path's own origin/RP-ID guard. TestPasskeyRelyingPartyComesFromPublicURL in passkey_test.go only
// calls webAuthn() — a helper the migration report itself says is "no longer on the production
// path", kept solely because that pre-existing test calls it directly. Every real handler goes
// through passkeyService() instead, which re-implements the same guard rather than delegating to
// webAuthn(). The two are textually identical today, but nothing enforces that they stay that way,
// so this checks passkeyService() itself, through an actual handler, not the retained-for-tests
// twin.
func TestVerifierPasskeyServiceRejectsUnconfiguredPublicURL(t *testing.T) {
	s := verifierServer(t, "henry", "verifier-passphrase-three")
	s.st.SetSetting("public_url", "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/me/passkeys/register/begin", nil)
	req.Header.Set(stepUpHeader, "verifier-passphrase-three")
	s.apiPasskeyRegisterBegin(rec, req, "henry")
	if rec.Code == http.StatusOK {
		t.Fatal("apiPasskeyRegisterBegin must refuse to start a ceremony without a configured Public URL, not silently guess an origin")
	}

	pendingTok := verifierPending2FA(t, s, "henry")
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/login/passkey/begin", strings.NewReader(`{"token":"`+pendingTok+`"}`))
	s.apiPasskeyLoginBegin(rec2, req2)
	if rec2.Code == http.StatusOK {
		t.Fatal("apiPasskeyLoginBegin must refuse to start a ceremony without a configured Public URL, not silently guess an origin")
	}

	// And confirm it is not just Begin: reconfigure and confirm normal operation resumes on the
	// very next call, with no restart — the whole point of rebuilding passkeyService() fresh every
	// request instead of caching one at startup.
	s.st.SetSetting("public_url", "https://verify.example")
	svc, err := s.passkeyService("webauthn-reg")
	if err != nil {
		t.Fatalf("passkeyService must recover immediately once the Public URL is set again: %v", err)
	}
	if svc == nil {
		t.Fatal("passkeyService returned a nil Service with a nil error")
	}
}
