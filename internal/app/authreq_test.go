package app

import (
	"sync"
	"testing"
	"time"
)

// TestAuthRequestSingleUse is the gate the whole login flow rests on: a pending request is
// consumable exactly once. Two concurrent callbacks carrying the same token must not both mint a
// session, which is why consumption is a conditional DELETE rather than select-then-delete.
func TestAuthRequestSingleUse(t *testing.T) {
	st := newTestStore(t)
	req := AuthRequest{Token: "tok-1", Kind: "oidc", Nonce: "n", Verifier: "v", Target: "/home"}
	if err := st.CreateAuthRequest(req, time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, ok := st.ConsumeAuthRequest("tok-1", time.Now())
	if !ok {
		t.Fatal("the first consume must succeed")
	}
	if got.Nonce != "n" || got.Verifier != "v" || got.Target != "/home" || got.Kind != "oidc" {
		t.Errorf("consumed request = %+v, want the stored values", got)
	}
	if _, ok := st.ConsumeAuthRequest("tok-1", time.Now()); ok {
		t.Error("a pending request must not be consumable twice (replay)")
	}
}

// TestAuthRequestConcurrentConsumeHasOneWinner exercises the race directly rather than trusting the
// SQL to be atomic by inspection.
func TestAuthRequestConcurrentConsumeHasOneWinner(t *testing.T) {
	st := newTestStore(t)
	st.CreateAuthRequest(AuthRequest{Token: "race", Kind: "oidc"}, time.Now().Add(time.Minute))

	const racers = 8
	var wg sync.WaitGroup
	wins := make(chan bool, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok := st.ConsumeAuthRequest("race", time.Now())
			wins <- ok
		}()
	}
	wg.Wait()
	close(wins)
	won := 0
	for ok := range wins {
		if ok {
			won++
		}
	}
	if won != 1 {
		t.Errorf("%d concurrent consumers succeeded, want exactly 1", won)
	}
}

// TestAuthRequestExpiry proves an expired request is not consumable — and that consuming checks
// expiry in the same statement, so a request cannot expire between the check and the delete.
func TestAuthRequestExpiry(t *testing.T) {
	st := newTestStore(t)
	st.CreateAuthRequest(AuthRequest{Token: "old", Kind: "saml"}, time.Now().Add(-time.Minute))
	if _, ok := st.ConsumeAuthRequest("old", time.Now()); ok {
		t.Error("an expired pending request must not be consumable")
	}
	if _, ok := st.ConsumeAuthRequest("never-existed", time.Now()); ok {
		t.Error("an unknown token must not be consumable")
	}
	if _, ok := st.ConsumeAuthRequest("", time.Now()); ok {
		t.Error("an empty token must not be consumable")
	}
}

// TestAssertionReplayGate covers the SAML replay cache: the first sighting of an assertion id wins
// and every later one is refused, atomically.
func TestAssertionReplayGate(t *testing.T) {
	st := newTestStore(t)
	exp := time.Now().Add(5 * time.Minute)

	if !st.MarkAssertionSeen("idp-a", "assertion-1", exp) {
		t.Fatal("the first sighting of an assertion must be accepted")
	}
	if st.MarkAssertionSeen("idp-a", "assertion-1", exp) {
		t.Error("a replayed assertion must be refused")
	}
	// The cache is keyed per IdP, so one provider cannot pre-poison another's id space.
	if !st.MarkAssertionSeen("idp-b", "assertion-1", exp) {
		t.Error("the same assertion id from a different IdP is a different assertion")
	}
}
