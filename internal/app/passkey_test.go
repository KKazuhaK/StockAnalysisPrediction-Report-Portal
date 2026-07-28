package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/go-webauthn/webauthn/webauthn"
)

func passkeyServer(t *testing.T) *Server {
	t.Helper()
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	st.SetSetting("public_url", "https://portal.example")
	st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	st.UpsertUser(User{Username: "bob", PasswordHash: "h", Role: "user"})
	return s
}

func fakeCred(id string, signCount uint32) *webauthn.Credential {
	c := &webauthn.Credential{ID: []byte(id), PublicKey: []byte("pk")}
	c.Authenticator.SignCount = signCount
	return c
}

// TestPasskeyStoreRoundTrip proves credentials persist, list back with their labels, and are
// scoped to their owner.
func TestPasskeyStoreRoundTrip(t *testing.T) {
	s := passkeyServer(t)
	if err := s.st.AddPasskey("alice", "YubiKey", fakeCred("cred-a", 3)); err != nil {
		t.Fatal(err)
	}
	if err := s.st.AddPasskey("alice", "Phone", fakeCred("cred-b", 0)); err != nil {
		t.Fatal(err)
	}
	if err := s.st.AddPasskey("bob", "Bob's key", fakeCred("cred-c", 0)); err != nil {
		t.Fatal(err)
	}

	creds, err := s.st.PasskeyCredentials("alice")
	if err != nil || len(creds) != 2 {
		t.Fatalf("alice has %d credentials, want 2 (err %v)", len(creds), err)
	}
	list := s.st.PasskeyList("alice")
	if len(list) != 2 || list[0]["label"] != "YubiKey" {
		t.Errorf("list = %v, want both keys with labels", list)
	}
	// The list is for recognising and revoking a key — it must not carry the credential itself.
	for _, e := range list {
		if _, leaked := e["credential"]; leaked {
			t.Error("the credential blob must not be exposed in the list")
		}
	}
	// A user with none is empty, not an error.
	if got, err := s.st.PasskeyCredentials("nobody"); err != nil || len(got) != 0 {
		t.Errorf("unknown user = %v, %v; want empty", got, err)
	}
}

// TestPasskeyDeleteIsOwnerScoped proves one account cannot revoke another's key by guessing an id.
func TestPasskeyDeleteIsOwnerScoped(t *testing.T) {
	s := passkeyServer(t)
	s.st.AddPasskey("alice", "A", fakeCred("cred-a", 0))
	s.st.AddPasskey("bob", "B", fakeCred("cred-b", 0))
	bobID := int64(s.st.PasskeyList("bob")[0]["id"].(int64))

	if err := s.st.DeletePasskey("alice", bobID); err != nil {
		t.Fatal(err)
	}
	if len(s.st.PasskeyList("bob")) != 1 {
		t.Error("one user must not be able to revoke another's passkey")
	}
	aliceID := int64(s.st.PasskeyList("alice")[0]["id"].(int64))
	s.st.DeletePasskey("alice", aliceID)
	if len(s.st.PasskeyList("alice")) != 0 {
		t.Error("a user must be able to revoke their own passkey")
	}
}

// TestPasskeySignCounterIsPersisted proves the counter is recorded on use — without that, a
// rollback (the signature of a cloned authenticator) could never be detected.
func TestPasskeySignCounterIsPersisted(t *testing.T) {
	s := passkeyServer(t)
	s.st.AddPasskey("alice", "A", fakeCred("cred-a", 5))
	s.st.TouchPasskey([]byte("cred-a"), 9)

	var count int64
	var lastUsed string
	s.st.queryRow(`SELECT sign_count, COALESCE(last_used_at,'') FROM webauthn_credentials WHERE username='alice'`).
		Scan(&count, &lastUsed)
	if count != 9 {
		t.Errorf("sign_count = %d, want the updated 9", count)
	}
	if lastUsed == "" {
		t.Error("last_used_at must be recorded so a user can spot an unfamiliar key")
	}
}

// TestPasskeyRelyingPartyComesFromPublicURL proves the RP ID is derived from the one configured
// origin. It matters more than it looks: an RP ID that changes invalidates every credential
// already registered, which users experience as their passkeys silently breaking.
func TestPasskeyRelyingPartyComesFromPublicURL(t *testing.T) {
	s := passkeyServer(t)
	wa, err := s.webAuthn()
	if err != nil {
		t.Fatal(err)
	}
	if wa.Config.RPID != "portal.example" {
		t.Errorf("RPID = %q, want the public URL's host", wa.Config.RPID)
	}
	// With no public URL configured, passkeys must refuse rather than guess from the request.
	s.st.SetSetting("public_url", "")
	if _, err := s.webAuthn(); err == nil {
		t.Error("passkeys must refuse to operate without a configured public URL")
	}
}

// TestPasskeyLoginBeginHidesUnknownAccounts proves the begin endpoint cannot be used to learn
// which accounts exist or which have passkeys.
func TestPasskeyLoginBeginHidesUnknownAccounts(t *testing.T) {
	s := passkeyServer(t)
	call := func(name string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiPasskeyLoginBegin(rec, httptest.NewRequest(http.MethodPost, "/api/login/passkey/begin",
			strings.NewReader(`{"username":"`+name+`"}`)))
		return rec
	}
	noSuchUser := call("ghost")
	realUserNoKey := call("alice")
	if noSuchUser.Code != realUserNoKey.Code || noSuchUser.Body.String() != realUserNoKey.Body.String() {
		t.Errorf("an unknown account (%d %s) and a real one with no passkey (%d %s) must be indistinguishable",
			noSuchUser.Code, noSuchUser.Body.String(), realUserNoKey.Code, realUserNoKey.Body.String())
	}
}

// TestPasskeyCeremonyIsSingleUse proves a challenge cannot be replayed: the parked ceremony is
// consumed on first use, like every other one-shot token in this codebase.
func TestPasskeyCeremonyIsSingleUse(t *testing.T) {
	s := passkeyServer(t)
	token, err := s.stashCeremony("alice", "webauthn-reg", &webauthn.SessionData{UserID: []byte("alice")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.takeCeremony(token, "webauthn-reg", "alice"); !ok {
		t.Fatal("the first use of a ceremony must succeed")
	}
	if _, ok := s.takeCeremony(token, "webauthn-reg", "alice"); ok {
		t.Error("a ceremony token must not be reusable")
	}
	// A ceremony belonging to someone else must not be claimable, and neither must the wrong kind.
	token, _ = s.stashCeremony("alice", "webauthn-reg", &webauthn.SessionData{UserID: []byte("alice")})
	if _, ok := s.takeCeremony(token, "webauthn-reg", "bob"); ok {
		t.Error("another user must not be able to claim a ceremony")
	}
	token, _ = s.stashCeremony("alice", "webauthn-reg", &webauthn.SessionData{UserID: []byte("alice")})
	if _, ok := s.takeCeremonyAny(token, "webauthn-login"); ok {
		t.Error("a registration ceremony must not be usable as a login ceremony")
	}
}
