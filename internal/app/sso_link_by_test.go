package app

import (
	"strings"
	"testing"
)

// Matching an incoming SSO login onto an EXISTING account (ADR 0023).
//
// The default has always been, and stays, "subject": the account is found by the identity link
// (issuer + subject), and an unlinked login that collides with a local account is refused, because
// auto-linking would let anyone who can make their IdP assert a matching name take over a password
// account.
//
// link_by is how an admin says the IdP IS authoritative for this portal's accounts. That is a real
// configuration — it is how an internal deployment behind one company IdP is supposed to work — but
// it is a decision with consequences, so it is off unless chosen, per provider, and never inferred.

func linkByServer(t *testing.T) *Server {
	t.Helper()
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "kazuha", PasswordHash: "local-password", Role: "user"})
	s.st.SetUserProfile("kazuha", "Kazuha Mo", "kazuha@corp.example")
	return s
}

func TestLinkBySubjectIsStillTheDefaultAndStillRefuses(t *testing.T) {
	s := linkByServer(t)
	p := SSOProvider{Kind: "saml", Slug: "saml", Provisioning: "jit"} // LinkBy unset
	_, created, err := s.resolveSSOAccount(p, ssoIdentity{Provider: "saml", Issuer: "https://idp", Subject: "kazuha"})
	if err == nil {
		t.Fatal("with link_by unset, an unlinked login must not adopt a local password account")
	}
	if created {
		t.Error("a refused resolution left an account behind")
	}
}

func TestLinkByUsernameAdoptsTheMatchingAccount(t *testing.T) {
	s := linkByServer(t)
	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByUsername}
	name, created, err := s.resolveSSOAccount(p, ssoIdentity{Provider: "saml", Issuer: "https://idp", Subject: "Kazuha"})
	if err != nil {
		t.Fatalf("link_by=username did not adopt: %v", err)
	}
	if created {
		t.Error("it created an account instead of adopting the existing one")
	}
	if name != "kazuha" {
		t.Errorf("adopted %q, want kazuha — the match folds case, like every other username path", name)
	}
	// And it is LINKED, so the next login takes the identity path rather than matching again.
	if u, ok := s.st.FindIdentity("saml", "https://idp", "Kazuha"); !ok || u != "kazuha" {
		t.Errorf("FindIdentity after adoption = %q,%v; the binding was not written", u, ok)
	}
}

func TestLinkByEmailAdoptsOnTheProfileEmail(t *testing.T) {
	s := linkByServer(t)
	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByEmail, AttrEmail: "email"}
	id := ssoIdentity{Provider: "saml", Issuer: "https://idp", Subject: "some-opaque-id",
		Claims: map[string]any{"email": "KAZUHA@CORP.EXAMPLE"}}
	name, created, err := s.resolveSSOAccount(p, id)
	if err != nil {
		t.Fatalf("link_by=email did not adopt: %v", err)
	}
	if created || name != "kazuha" {
		t.Errorf("adopted %q created=%v, want kazuha/false (case-insensitive)", name, created)
	}
}

// email has no unique index on users, so two accounts CAN share one. Picking either would be
// authenticating someone as an account chosen by row order. Refusing is the only defensible answer.
func TestLinkByEmailRefusesWhenTwoAccountsShareIt(t *testing.T) {
	s := linkByServer(t)
	s.st.UpsertUser(User{Username: "kazuha2", PasswordHash: "x", Role: "user"})
	s.st.SetUserProfile("kazuha2", "Kazuha Again", "kazuha@corp.example")

	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByEmail, AttrEmail: "email"}
	id := ssoIdentity{Provider: "saml", Issuer: "https://idp", Subject: "opaque",
		Claims: map[string]any{"email": "kazuha@corp.example"}}
	_, _, err := s.resolveSSOAccount(p, id)
	if err == nil {
		t.Fatal("an ambiguous email adopted an account; it must refuse instead of choosing one")
	}
	if !strings.Contains(err.Error(), "more than one") {
		t.Errorf("error %q should say the email is ambiguous, so an operator can fix the data", err)
	}
}

// An SSO login must never fall onto an account that is not this provider's to claim just because a
// claim was empty. No email in the assertion means no email match.
func TestLinkByEmailIgnoresAnEmptyClaim(t *testing.T) {
	s := linkByServer(t)
	s.st.UpsertUser(User{Username: "noemail", PasswordHash: "x", Role: "user"}) // email stays ""
	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByEmail, AttrEmail: "email"}
	_, _, err := s.resolveSSOAccount(p, ssoIdentity{Provider: "saml", Issuer: "https://idp", Subject: "opaque"})
	if err == nil {
		t.Fatal("an assertion with no email matched an account with no email")
	}
}

// Matching adopts; it never creates. Provisioning is a separate decision and stays off unless jit.
func TestLinkByDoesNotProvisionWhenNothingMatches(t *testing.T) {
	s := linkByServer(t)
	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByUsername} // provisioning off
	_, created, err := s.resolveSSOAccount(p, ssoIdentity{Provider: "saml", Issuer: "https://idp", Subject: "stranger"})
	if err == nil {
		t.Fatal("a name nobody holds must not sign in when provisioning is off")
	}
	if created {
		t.Error("link_by created an account; adoption and provisioning are different decisions")
	}
}

// A disabled account is not a login, however it was matched.
func TestLinkByRefusesADisabledAccount(t *testing.T) {
	s := linkByServer(t)
	s.st.SetUserActive("kazuha", false)
	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByUsername}
	if _, _, err := s.resolveSSOAccount(p, ssoIdentity{Provider: "saml", Issuer: "https://idp", Subject: "kazuha"}); err == nil {
		t.Fatal("a disabled account was adopted")
	}
}

// Matching by email with no email claim mapped is a CONFIGURATION failure, and the login page has
// to say so. It used to arrive as not_provisioned, whose message asserts a cause that is not the
// real one — "this provider does not create accounts" — so an admin who had set everything up
// correctly except one blank field was told to look somewhere else entirely.
//
// Safe to name because it is decided before anything is looked up: it says nothing about whether
// the person has an account. Every refusal that DOES depend on that stays indistinguishable.
func TestNoEmailClaimIsReportedAsConfigurationNotAsMissingAccount(t *testing.T) {
	s := linkByServer(t)
	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByEmail} // AttrEmail deliberately unset
	_, _, err := s.resolveSSOAccount(p, ssoIdentity{Provider: "saml", Issuer: "https://idp", Subject: "opaque",
		Claims: map[string]any{"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress": "kazuha@corp.example"}})
	if err == nil {
		t.Fatal("an unmapped email claim silently matched something")
	}
	if got := resolveFailCode(err); got != "sso_no_email_claim" {
		t.Errorf("login page told %q, want sso_no_email_claim — the admin needs the real cause", got)
	}
}

// And everything that depends on WHO is asking stays one indistinguishable answer.
func TestUserDependentRefusalsStayIndistinguishable(t *testing.T) {
	s := linkByServer(t)
	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByEmail, AttrEmail: "email"}

	// Ambiguous email: naming it would reveal that two accounts share an address.
	s.st.UpsertUser(User{Username: "kazuha2", PasswordHash: "x", Role: "user"})
	s.st.SetUserProfile("kazuha2", "Dup", "kazuha@corp.example")
	_, _, ambiguous := s.resolveSSOAccount(p, ssoIdentity{Provider: "saml", Issuer: "https://idp", Subject: "o",
		Claims: map[string]any{"email": "kazuha@corp.example"}})

	// No such account at all.
	_, _, missing := s.resolveSSOAccount(p, ssoIdentity{Provider: "saml", Issuer: "https://idp", Subject: "o",
		Claims: map[string]any{"email": "stranger@corp.example"}})

	if ambiguous == nil || missing == nil {
		t.Fatal("both of these must refuse")
	}
	if resolveFailCode(ambiguous) != "not_provisioned" || resolveFailCode(missing) != "not_provisioned" {
		t.Errorf("the login page can tell them apart: %q vs %q",
			resolveFailCode(ambiguous), resolveFailCode(missing))
	}
}

// An emailAddress-format NameID IS the email. Entra sends the configured unique identifier as the
// subject — for a normal tenant that is the UPN, which is an address — so an admin who chose email
// matching and left the claim unmapped had the value sitting right there and was refused anyway.
//
// The sibling branch already works this way: username matching falls back to the subject when the
// UPN claim is unmapped. Email matching not doing the same was an inconsistency, not a safeguard.
func TestLinkByEmailFallsBackToAnEmailSubject(t *testing.T) {
	s := linkByServer(t)
	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByEmail} // no AttrEmail mapped
	name, created, err := s.resolveSSOAccount(p, ssoIdentity{
		Provider: "saml", Issuer: "https://idp", Subject: "KAZUHA@CORP.EXAMPLE"})
	if err != nil {
		t.Fatalf("an email-shaped subject was not used: %v", err)
	}
	if created || name != "kazuha" {
		t.Errorf("adopted %q created=%v, want kazuha/false", name, created)
	}
}

// A mapped claim still wins: the fallback is for the unmapped case, not an override.
func TestLinkByEmailPrefersTheMappedClaimOverTheSubject(t *testing.T) {
	s := linkByServer(t)
	s.st.UpsertUser(User{Username: "other", PasswordHash: "x", Role: "user"})
	s.st.SetUserProfile("other", "Other", "other@corp.example")
	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByEmail, AttrEmail: "email"}
	name, _, err := s.resolveSSOAccount(p, ssoIdentity{Provider: "saml", Issuer: "https://idp",
		Subject: "kazuha@corp.example", Claims: map[string]any{"email": "other@corp.example"}})
	if err != nil || name != "other" {
		t.Errorf("adopted %q (%v), want other — the mapped claim decides", name, err)
	}
}

// An opaque subject is not an address, so it cannot silently become one. This is what keeps the
// fallback from matching anything by accident.
func TestLinkByEmailIgnoresASubjectThatIsNotAnAddress(t *testing.T) {
	s := linkByServer(t)
	p := SSOProvider{Kind: "saml", Slug: "saml", LinkBy: LinkByEmail}
	_, _, err := s.resolveSSOAccount(p, ssoIdentity{Provider: "saml", Issuer: "https://idp",
		Subject: "AAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"})
	if err == nil {
		t.Fatal("an opaque subject was treated as an email address")
	}
	if got := resolveFailCode(err); got != "sso_no_email_claim" {
		t.Errorf("code %q, want sso_no_email_claim — there is still no email to match on", got)
	}
}
