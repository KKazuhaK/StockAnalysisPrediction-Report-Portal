package app

import "testing"

// One account, one external identity — the same rule the Passwall panel enforces.
//
// It used to live in a side table that allowed several links per account, on the theory that an IdP
// migration needs both live during the overlap. That overlap is not a case this portal has, and
// paying a table plus an index for it is the wrong trade: the identity now sits on the users row,
// where "one per account" is what the shape says rather than what a comment claims.
//
// The KEY is still (issuer, subject) and never email — the nOAuth account-takeover class. The
// issuer participates because a subject is unique only within one, so keying on the provider slug
// alone would let an admin repointing "corp" at a different IdP silently match a stranger's subject
// onto an existing account.

func TestOneIdentityPerAccount(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})

	first := Identity{Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-1",
		Username: "alice", ProviderSlug: "corp"}
	if err := st.LinkIdentity(first); err != nil {
		t.Fatal(err)
	}
	if u, ok := st.FindIdentity("oidc", "https://idp.example", "sub-1"); !ok || u != "alice" {
		t.Fatalf("lookup = %q %v, want alice", u, ok)
	}
	// A repeat login refreshes rather than failing.
	if err := st.LinkIdentity(first); err != nil {
		t.Errorf("a repeat login must be idempotent: %v", err)
	}

	// Binding a SECOND identity to the same account replaces the first: one account, one identity.
	second := Identity{Provider: "saml", Issuer: "https://other.example", Subject: "sub-2",
		Username: "alice", ProviderSlug: "legacy"}
	if err := st.LinkIdentity(second); err != nil {
		t.Fatal(err)
	}
	if u, ok := st.FindIdentity("saml", "https://other.example", "sub-2"); !ok || u != "alice" {
		t.Error("the new identity must resolve to the account")
	}
	if _, ok := st.FindIdentity("oidc", "https://idp.example", "sub-1"); ok {
		t.Error("the previous identity must no longer resolve — one account holds one identity")
	}
	if got := st.IdentitiesOf("alice"); len(got) != 1 || got[0].Subject != "sub-2" {
		t.Errorf("IdentitiesOf = %+v, want exactly the current one", got)
	}
}

// TestIdentityIsNotSharedBetweenAccounts proves the database refuses to let two accounts claim one
// external identity. Without it, whoever logged in second would quietly take the link, and the
// first account would be locked out of SSO with nothing recorded.
func TestIdentityIsNotSharedBetweenAccounts(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	st.UpsertUser(User{Username: "mallory", PasswordHash: "h", Role: "user"})

	id := Identity{Provider: "oidc", Issuer: "https://idp.example", Subject: "sub-1",
		Username: "alice", ProviderSlug: "corp"}
	if err := st.LinkIdentity(id); err != nil {
		t.Fatal(err)
	}
	stolen := id
	stolen.Username = "mallory"
	if err := st.LinkIdentity(stolen); err == nil {
		t.Error("a second account claiming the same external identity must be refused")
	}
	if u, _ := st.FindIdentity("oidc", "https://idp.example", "sub-1"); u != "alice" {
		t.Errorf("the identity now resolves to %q — the original binding must stand", u)
	}
}

// TestIdentityKeyIncludesTheIssuer proves the same subject at a different issuer is a different
// person. A provider slug alone is not the key: repointing a provider at a new IdP would otherwise
// hand an existing account to whoever holds that subject there.
func TestIdentityKeyIncludesTheIssuer(t *testing.T) {
	s := tenancyServer(t)
	st := s.st
	st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	st.LinkIdentity(Identity{Provider: "oidc", Issuer: "https://idp-a.example", Subject: "shared",
		Username: "alice", ProviderSlug: "corp"})

	if _, ok := st.FindIdentity("oidc", "https://idp-b.example", "shared"); ok {
		t.Error("the same subject at a different issuer must not resolve to that account")
	}
	// And unlinking is by the same key.
	if err := st.UnlinkIdentity("oidc", "https://idp-a.example", "shared"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.FindIdentity("oidc", "https://idp-a.example", "shared"); ok {
		t.Error("an unlinked identity must stop resolving")
	}
	if got := st.IdentitiesOf("alice"); len(got) != 0 {
		t.Errorf("IdentitiesOf after unlink = %+v, want empty", got)
	}
}
