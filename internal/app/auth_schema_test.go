package app

import (
	"testing"
)

// TestAuthSchemaBaseline locks the schema for ADR 0023 (SSO + TOTP 2FA + passkeys). Every object is
// declared once in baseSchemaStmts, which is now also the acceptance contract for an existing
// database: an older one is refused rather than reconciled (ADR 0034).
func TestAuthSchemaBaseline(t *testing.T) {
	st := newTestStore(t)

	cols := []struct{ table, col string }{
		// Identity source + the SSO↔SCIM join key (ADR 0023). The SCIM-only columns are
		// deliberately deferred to the SCIM change; only external_id must ship with SSO.
		{"users", "external_id"},
		{"users", "source"},
		{"users", "source_ref"},
		{"users", "created_at"},
		{"users", "updated_at"},
		// TOTP two-factor.
		{"users", "totp_secret_enc"},
		{"users", "totp_enabled"},
		{"users", "totp_confirmed_at"},
		{"users", "recovery_codes"},
		// Account linking.
		// The external identity lives on the users row, one per account (ADR 0023, revised to match
		// the Passwall panel). It was a side table that allowed several links per account, for an
		// IdP-migration overlap this portal does not have.
		{"users", "sso_provider"},
		{"users", "sso_issuer"},
		{"users", "sso_subject"},
		// Provider config.
		{"sso_providers", "kind"},
		{"sso_providers", "slug"},
		{"sso_providers", "enabled"},
		{"sso_providers", "provisioning"},
		{"sso_providers", "default_group"},
		{"sso_providers", "client_secret_enc"},
		// Ordered group → role + OU rules.
		// Short-lived single-use state, shared by SAML / OIDC / 2FA / WebAuthn.
		{"auth_requests", "token"},
		{"auth_requests", "kind"},
		{"auth_requests", "expires_at"},
		// SAML replay cache.
		{"sso_assertion_seen", "seen_key"},
		// Passkeys.
		{"webauthn_credentials", "credential_id"},
		{"webauthn_credentials", "username"},
		{"webauthn_credentials", "sign_count"},
	}
	for _, c := range cols {
		if !st.columnExists(c.table, c.col) {
			t.Errorf("missing column %s.%s", c.table, c.col)
		}
	}
	// The keyring is deliberately absent: one salt and one wrapped key are two rows in `meta`, not a
	// single-row table. adoptLegacyKeyring moves one out of the old table on upgrade, because losing
	// it makes every stored secret permanently unreadable.
	if st.tableExists("sso_keyring") {
		t.Error("the single-row keyring table must not come back")
	}
	// The group rules are deliberately absent too: one ordered list that is always replaced whole,
	// so it is one JSON setting rather than a table plus an index to keep its order stable.
	if st.tableExists("sso_group_rules") {
		t.Error("the group-rules table must not come back")
	}
	for _, tbl := range []string{"sso_providers",
		"auth_requests", "sso_assertion_seen", "webauthn_credentials"} {
		if !st.tableExists(tbl) {
			t.Errorf("missing table %s", tbl)
		}
	}
	for _, idx := range []string{"idx_users_external_id", "idx_sso_providers_slug", "idx_webauthn_cred_id"} {
		var n int
		st.queryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&n)
		if n == 0 {
			t.Errorf("missing index %s", idx)
		}
	}
}
