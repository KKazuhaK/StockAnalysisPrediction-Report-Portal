package captcha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestImageChallengeRoundTrip proves the self-hosted provider works end to end without touching the
// network — the property that makes it the default for a portal that may sit where Google and
// Cloudflare are unreachable.
func TestImageChallengeRoundTrip(t *testing.T) {
	s := New()
	set := Settings{Provider: ProviderImage}

	ch, err := s.Issue(set)
	if err != nil || ch == nil {
		t.Fatalf("issue = %v, %v", ch, err)
	}
	if ch.ID == "" || !strings.HasPrefix(ch.Image, "data:image/") {
		t.Fatalf("challenge = %+v, want an id and a data URL", ch)
	}
	// The answer is not recoverable from the API, so drive verification through the store the way a
	// wrong answer would arrive: it must be refused.
	if ok, err := s.Verify(context.Background(), set, Response{ID: ch.ID, Answer: "00000"}); err != nil {
		t.Errorf("a wrong answer is not an error, got %v", err)
	} else if ok {
		t.Error("a wrong answer must not verify")
	}
	// Empty input is a plain failure, never a pass.
	for _, r := range []Response{{}, {ID: ch.ID}, {Answer: "12345"}} {
		if ok, _ := s.Verify(context.Background(), set, r); ok {
			t.Errorf("empty response %+v must not verify", r)
		}
	}
}

// TestImageChallengeIsSingleUse proves a correct answer is consumed. Without it one solved captcha
// could be replayed across a burst of parallel attempts, which is exactly the automation the gate
// exists to stop.
func TestImageChallengeIsSingleUse(t *testing.T) {
	s := New()
	set := Settings{Provider: ProviderImage}
	ch, _ := s.Issue(set)

	// Brute-force the 5-digit answer against the store to obtain a genuinely correct one. Cheap
	// here, and it lets the test assert consumption rather than assume it.
	answer := ""
	for i := 0; i < 100000; i++ {
		guess := pad5(i)
		if s.store.Verify(ch.ID, guess, false) { // clear=false: do not consume while searching
			answer = guess
			break
		}
	}
	if answer == "" {
		t.Fatal("could not recover the issued answer")
	}
	if ok, _ := s.Verify(context.Background(), set, Response{ID: ch.ID, Answer: answer}); !ok {
		t.Fatal("the correct answer must verify")
	}
	if ok, _ := s.Verify(context.Background(), set, Response{ID: ch.ID, Answer: answer}); ok {
		t.Error("a consumed challenge must not verify a second time")
	}
}

func pad5(n int) string {
	s := "00000" + itoa(n)
	return s[len(s)-5:]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// TestTokenProviderVerifies covers the three hosted providers, which share one siteverify contract.
func TestTokenProviderVerifies(t *testing.T) {
	var gotSecret, gotToken, gotIP string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotSecret, gotToken, gotIP = r.PostForm.Get("secret"), r.PostForm.Get("response"), r.PostForm.Get("remoteip")
		w.Write([]byte(`{"success":true,"hostname":"portal.example"}`))
	}))
	defer srv.Close()

	s := New()
	s.endpoints[ProviderTurnstile] = srv.URL
	set := Settings{Provider: ProviderTurnstile, SecretKey: "sekret", ExpectedHost: "portal.example"}

	ok, err := s.Verify(context.Background(), set, Response{Token: "tok", RemoteIP: "203.0.113.7"})
	if err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	if gotSecret != "sekret" || gotToken != "tok" || gotIP != "203.0.113.7" {
		t.Errorf("siteverify got secret=%q token=%q ip=%q", gotSecret, gotToken, gotIP)
	}
	// An empty token is a plain failure and must not reach the provider.
	if ok, _ := s.Verify(context.Background(), set, Response{}); ok {
		t.Error("an empty token must not verify")
	}
}

// TestTokenProviderRejectsCrossSiteReplay proves the hostname pinning: a token solved on another
// site that happens to share this site key must not be usable here.
func TestTokenProviderRejectsCrossSiteReplay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"hostname":"attacker.example"}`))
	}))
	defer srv.Close()
	s := New()
	s.endpoints[ProviderTurnstile] = srv.URL

	set := Settings{Provider: ProviderTurnstile, SecretKey: "k", ExpectedHost: "portal.example"}
	if ok, _ := s.Verify(context.Background(), set, Response{Token: "tok"}); ok {
		t.Error("a token solved on another host must be rejected")
	}
	// Lenient where it could lock someone out: no configured host means no check.
	set.ExpectedHost = ""
	if ok, err := s.Verify(context.Background(), set, Response{Token: "tok"}); err != nil || !ok {
		t.Errorf("with no expected host the check must be skipped: %v %v", ok, err)
	}
}

// TestVerifyFailsClosedOnMisconfiguration proves the gate does not open when its verifier cannot
// work. Each of these returns an error, and the caller's contract is to refuse on error.
func TestVerifyFailsClosedOnMisconfiguration(t *testing.T) {
	s := New()
	for _, tc := range []struct {
		name string
		set  Settings
	}{
		{"unknown provider", Settings{Provider: "nope"}},
		{"token provider with no secret", Settings{Provider: ProviderTurnstile}},
	} {
		ok, err := s.Verify(context.Background(), tc.set, Response{Token: "tok", ID: "x", Answer: "y"})
		if ok {
			t.Errorf("%s: verified", tc.name)
		}
		if err == nil {
			t.Errorf("%s: must report an error so the caller fails closed", tc.name)
		}
	}
	// A siteverify outage is also an error, not a silent pass.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	s.endpoints[ProviderHCaptcha] = srv.URL
	if ok, err := s.Verify(context.Background(),
		Settings{Provider: ProviderHCaptcha, SecretKey: "k"}, Response{Token: "t"}); ok || err == nil {
		t.Errorf("an unusable siteverify reply must be an error, got ok=%v err=%v", ok, err)
	}
}

// TestProviderDefaultsToImage proves an unset provider resolves to the one that needs no external
// service — the safe default for a self-hosted portal.
func TestProviderDefaultsToImage(t *testing.T) {
	s := New()
	ch, err := s.Issue(Settings{})
	if err != nil || ch == nil {
		t.Fatalf("an unset provider must issue an image challenge: %v %v", ch, err)
	}
	// A token provider issues nothing server-side; its widget is rendered from the site key.
	if ch, err := s.Issue(Settings{Provider: ProviderRecaptcha}); ch != nil || err != nil {
		t.Errorf("a token provider must not issue server-side: %v %v", ch, err)
	}
}

func TestValidProviderAndHostOf(t *testing.T) {
	for _, p := range []string{ProviderImage, ProviderTurnstile, ProviderRecaptcha, ProviderHCaptcha} {
		if !ValidProvider(p) {
			t.Errorf("%q must be valid", p)
		}
	}
	for _, p := range []string{"", "none", "Image"} {
		if ValidProvider(p) {
			t.Errorf("%q must not be accepted by the settings validation", p)
		}
	}
	for in, want := range map[string]string{
		"https://portal.example.com:8443/": "portal.example.com",
		"http://Portal.Example":            "portal.example",
		"":                                 "",
		"::::":                             "",
	} {
		if got := HostOf(in); got != want {
			t.Errorf("HostOf(%q) = %q, want %q", in, got, want)
		}
	}
}
