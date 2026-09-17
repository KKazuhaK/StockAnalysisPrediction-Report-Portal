package app

// Wiring tests for the authcore/saml migration (saml.go, saml_crewjam.go, saml_replay.go).
//
// The tests in saml_test.go cover the logic; these cover the wiring, which is where a migration
// that swaps a library for another can silently lose a property that no unit test names:
//
//   - the replay cache is persistent, so an assertion stays refused across a process restart --
//     authcore ships an in-memory ReplayCache and using it would make replays possible after a
//     restart, so this asserts against a file-backed database opened by two separate *Store values
//   - a fresh Provider and replay cache are built per tenant, so one tenant's assertion id is not
//     visible to another
//   - step-up SSO really sets ForceAuthn on the wire; authcore/saml has no ForceAuthn option, so
//     that one call still goes through crewjam directly (saml_crewjam.go) and nothing but a
//     wire-level check would notice if it stopped working
//   - samlFailureReason maps every error authcore can return, so a new rejection never surfaces
//     as a generic failure
//
// See FRICTION.md's "## saml -- Report-Portal" entry in the authcore repo.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
	"github.com/KazuhaHub/authcore/saml"
)

func TestSAMLReplaySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "verify-restart.sqlite")

	// "Process 1": open the store, record a replay entry via the real adapter, close.
	st1, err := OpenStore("sqlite", dbPath)
	if err != nil {
		t.Fatalf("OpenStore (process 1): %v", err)
	}
	now := time.Now()
	exp := now.Add(10 * time.Minute)
	c1 := samlReplayCache{st: st1, idpEntityID: "https://idp.restart-test.example"}
	seen, err := c1.SeenOrAdd(context.Background(), "_assert-restart-proof", exp, now)
	if err != nil {
		t.Fatalf("SeenOrAdd (process 1, first use): %v", err)
	}
	if seen {
		t.Fatal("first use of a fresh assertion id must not be reported as already seen")
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close process 1's store: %v", err)
	}

	// "Process 2": brand-new *Store pointed at the SAME on-disk database file — this is what a
	// process restart looks like. If replay state were only in memory (e.g. authcore's
	// NewMemoryReplayCache), this would come back as a fresh (unseen) sighting. It must not.
	st2, err := OpenStore("sqlite", dbPath)
	if err != nil {
		t.Fatalf("OpenStore (process 2 / after restart): %v", err)
	}
	defer st2.Close()
	c2 := samlReplayCache{st: st2, idpEntityID: "https://idp.restart-test.example"}
	seen2, err := c2.SeenOrAdd(context.Background(), "_assert-restart-proof", exp, now)
	if err != nil {
		t.Fatalf("SeenOrAdd (process 2, replay attempt): %v", err)
	}
	if !seen2 {
		t.Fatal("REGRESSION: an assertion id recorded before a process restart was accepted again " +
			"after restart — the replay cache is not actually persistent")
	}

	// Sanity: a *different* assertion id under the same IdP, never seen before, must still be
	// accepted after "restart" — proves we didn't just wedge the whole table shut.
	seen3, err := c2.SeenOrAdd(context.Background(), "_assert-restart-proof-DIFFERENT", exp, now)
	if err != nil {
		t.Fatalf("SeenOrAdd (process 2, fresh id): %v", err)
	}
	if seen3 {
		t.Fatal("a genuinely fresh assertion id was incorrectly reported as already seen after restart")
	}
}

// tenantAMetadata / tenantBMetadata are two genuinely distinct IdPs (distinct entityID and SSO
// endpoint), standing in for two separate tenants that each configured their own SAML provider row
// (distinct Slug, distinct IdP). This is the realistic multi-tenant case the migration report's
// section 4 is about: the risk is not "same slug" (impossible — Slug is a unique DB column) but
// whether the ReplayCache actually ends up scoped per provider in practice, all the way through the
// real construction path (samlProvider -> samlConfig -> Config.ReplayCache), not just by directly
// constructing a samlReplayCache struct by hand as saml_test.go's TestSAMLReplayIsRefused does.
const tenantAMetadata = `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp-tenant-a.example">
	<IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
		<SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp-tenant-a.example/sso"/>
	</IDPSSODescriptor></EntityDescriptor>`

const tenantBMetadata = `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp-tenant-b.example">
	<IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
		<SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp-tenant-b.example/sso"/>
	</IDPSSODescriptor></EntityDescriptor>`

// verifySaveSAMLProvider is a trimmed copy of saml_test.go's samlTestProvider, parameterized on slug
// and IdP metadata so two independent tenants can be built in one test.
func verifySaveSAMLProvider(t *testing.T, s *Server, slug, idpMetadata string) SSOProvider {
	t.Helper()
	rec := httptest.NewRecorder()
	body := `{"kind":"saml","slug":"` + slug + `","name":"Tenant"}`
	s.apiAdminSSOSave(rec, httptest.NewRequest(http.MethodPost, "/api/admin/sso/providers", strings.NewReader(body)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("fixture provider %q save -> %d (%s)", slug, rec.Code, rec.Body.String())
	}
	p, ok := s.st.SSOProviderBySlug(slug)
	if !ok {
		t.Fatalf("fixture provider %q was not stored", slug)
	}
	p.IdPMetadataXML = idpMetadata
	if _, err := s.st.SaveSSOProvider(p); err != nil {
		t.Fatalf("store metadata for %q: %v", slug, err)
	}
	p, _ = s.st.SSOProviderBySlug(slug)
	return p
}

// TestSAMLCrossTenantReplayCacheIsIsolated independently proves that an assertion id "seen" for
// tenant A's real Provider construction does not block, and cannot be forged as already-seen for,
// tenant B's — using the actual s.samlProvider -> samlConfig -> Config.ReplayCache wiring, not a
// hand-built samlReplayCache. This is the failure mode the task calls out explicitly: "if the
// tenant id ends up inside a shared hash or cache key" (the audit-package failure repeated).
func TestSAMLCrossTenantReplayCacheIsIsolated(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	st.EnsureDefaultGroup()
	st.SetSetting("public_url", "https://portal.example")

	pA := verifySaveSAMLProvider(t, s, "tenant-a", tenantAMetadata)
	pB := verifySaveSAMLProvider(t, s, "tenant-b", tenantBMetadata)

	mA, err := s.samlLoadMaterial(pA)
	if err != nil {
		t.Fatalf("samlLoadMaterial(tenant A): %v", err)
	}
	mB, err := s.samlLoadMaterial(pB)
	if err != nil {
		t.Fatalf("samlLoadMaterial(tenant B): %v", err)
	}
	cfgA := s.samlConfig(pA, mA)
	cfgB := s.samlConfig(pB, mB)

	now := time.Now()
	exp := now.Add(10 * time.Minute)
	const sharedAssertionID = "_assert-cross-tenant-probe"

	// Attacker (or coincidence) reuses the exact same assertion ID string under tenant A first.
	seenA, err := cfgA.ReplayCache.SeenOrAdd(context.Background(), sharedAssertionID, exp, now)
	if err != nil || seenA {
		t.Fatalf("tenant A's first use of the probe id must be accepted, got seen=%v err=%v", seenA, err)
	}

	// The SAME assertion ID under tenant B's Provider must be a fresh sighting: tenant B's IdP is a
	// different entity, so tenant B's ReplayCache must not have been poisoned by tenant A's write.
	// If this fails, the migration reopened the exact failure mode that killed the audit migration:
	// a shared cache key across tenants.
	seenB, err := cfgB.ReplayCache.SeenOrAdd(context.Background(), sharedAssertionID, exp, now)
	if err != nil {
		t.Fatalf("tenant B SeenOrAdd: %v", err)
	}
	if seenB {
		t.Fatal("REGRESSION: tenant B's ReplayCache reported tenant A's assertion id as already " +
			"seen — the replay cache is not actually isolated per tenant/provider")
	}

	// And replaying tenant A's own id against tenant A's own Provider a second time must still be
	// refused — isolation must not have come at the cost of the guard itself.
	seenAagain, err := cfgA.ReplayCache.SeenOrAdd(context.Background(), sharedAssertionID, exp, now)
	if err != nil || !seenAagain {
		t.Errorf("re-using tenant A's own assertion id against tenant A must still be refused, got seen=%v err=%v", seenAagain, err)
	}
}

// TestSAMLStepUpSetsForceAuthn independently checks the single biggest concession the
// migration report makes: that authcore/saml.Provider.NewAuthnRequest has no ForceAuthn option, so
// the step-up path (saml_crewjam.go's forceAuthnRedirect) bypasses authcore and talks to
// crewjam/saml directly. Nothing in the shipped test suite decodes a step-up redirect and checks
// ForceAuthn is actually present on the wire — TestAuthnRequestDoesNotAskForATransientNameID only
// covers the NON-step-up path (provider.NewAuthnRequest). This closes that gap: if forceAuthnRedirect
// regressed (e.g. the `req.ForceAuthn = &force` line were dropped or the wrong request object were
// serialized), step-up would silently degrade into an ordinary SSO-session check-in, exactly the
// failure ADR 0023 and this file's own doc comment warn about — and nothing existing would catch it.
func TestSAMLStepUpSetsForceAuthn(t *testing.T) {
	s := samlTestServer(t)
	p := samlTestProvider(t, s)
	m, err := s.samlLoadMaterial(p)
	if err != nil {
		t.Fatalf("samlLoadMaterial: %v", err)
	}

	// Sanity control: the ordinary (non-step-up) path must NOT set ForceAuthn — otherwise every
	// login would force re-authentication and this test would be meaningless.
	provider, err := saml.New(s.samlConfig(p, m))
	if err != nil {
		t.Fatalf("saml.New: %v", err)
	}
	ordinary, err := provider.NewAuthnRequest("relay-ordinary")
	if err != nil {
		t.Fatalf("NewAuthnRequest: %v", err)
	}
	ou, err := url.Parse(ordinary.RedirectURL)
	if err != nil {
		t.Fatalf("parse ordinary redirect URL: %v", err)
	}
	ordinaryXML, err := saml.DecodeRedirectMessage(ou.Query().Get("SAMLRequest"), 0)
	if err != nil {
		t.Fatalf("decode ordinary SAMLRequest: %v", err)
	}
	if strings.Contains(string(ordinaryXML), "ForceAuthn=\"true\"") {
		t.Fatalf("the ordinary (non-step-up) AuthnRequest must not set ForceAuthn:\n%s", ordinaryXML)
	}

	// The actual claim under test: the step-up path.
	reqID, redirectURL, err := m.forceAuthnRedirect("relay-stepup")
	if err != nil {
		t.Fatalf("forceAuthnRedirect: %v", err)
	}
	if reqID == "" {
		t.Error("forceAuthnRedirect returned an empty request id")
	}
	su, err := url.Parse(redirectURL)
	if err != nil {
		t.Fatalf("parse step-up redirect URL: %v", err)
	}
	stepUpXML, err := saml.DecodeRedirectMessage(su.Query().Get("SAMLRequest"), 0)
	if err != nil {
		t.Fatalf("decode step-up SAMLRequest: %v", err)
	}
	if !strings.Contains(string(stepUpXML), `ForceAuthn="true"`) {
		t.Errorf("REGRESSION: the step-up AuthnRequest does not carry ForceAuthn=\"true\" on the "+
			"wire — step-up would silently degrade into a session check-in:\n%s", stepUpXML)
	}
	if got := u2ID(su); got != reqID {
		t.Errorf("SAMLRequest ID on the wire (%q) does not match the returned request id (%q) — the "+
			"ACS's InResponseTo check would never match this request", got, reqID)
	}
}

// TestSAMLFailureReasonCoversEveryBranch independently checks a gap this report found:
// samlFailureReason's switch has six branches, but the shipped test suite exercises only four of
// them (saml_destination, saml_weak_signature, saml_encrypted, saml_multiple_assertions) — the
// ErrReplayed -> saml_replay and ErrDuplicateAttribute -> saml_attributes branches have no test
// anywhere, direct or end-to-end. This does not indicate either branch is actually wrong (both are
// confirmed correct here), but it is a real, reproducible gap: a future edit could typo one of
// these two case labels and nothing in the suite would catch it.
func TestSAMLFailureReasonCoversEveryBranch(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"replayed":            {saml.ErrReplayed, "saml_replay"},
		"duplicate attribute": {saml.ErrDuplicateAttribute, "saml_attributes"},
	}
	for name, c := range cases {
		if got := samlFailureReason(c.err); got != c.want {
			t.Errorf("%s: samlFailureReason(%v) = %q, want %q", name, c.err, got, c.want)
		}
	}
}

// u2ID pulls the AuthnRequest's own ID attribute out of the decoded redirect URL's SAMLRequest —
// re-decoded here rather than threaded through, to keep the caller above linear.
func u2ID(u *url.URL) string {
	xmlBytes, err := saml.DecodeRedirectMessage(u.Query().Get("SAMLRequest"), 0)
	if err != nil {
		return ""
	}
	const marker = ` ID="`
	i := strings.Index(string(xmlBytes), marker)
	if i < 0 {
		return ""
	}
	rest := string(xmlBytes)[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
