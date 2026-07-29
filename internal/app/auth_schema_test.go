package app

import (
	"database/sql"
	"testing"
)

// TestAuthSchemaBaseline locks the additive schema for ADR 0023 (SSO + TOTP 2FA + passkeys). Every
// object is declared once in baseSchemaStmts, so it exists on a fresh store here and — because
// ensureColumns reads the same statements — is auto-added to an older database with no migration.
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
		{"sso_group_rules", "provider_id"},
		{"sso_group_rules", "ord"},
		{"sso_group_rules", "target_role"},
		{"sso_group_rules", "target_group"},
		{"sso_group_rules", "keep_on_miss"},
		// Short-lived single-use state, shared by SAML / OIDC / 2FA / WebAuthn.
		{"sso_auth_requests", "token"},
		{"sso_auth_requests", "kind"},
		{"sso_auth_requests", "expires_at"},
		// SAML replay cache.
		{"sso_assertion_seen", "seen_key"},
		// Envelope-encryption keyring.
		{"sso_keyring", "wrapped_dek"},
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
	for _, tbl := range []string{"sso_providers", "sso_group_rules",
		"sso_auth_requests", "sso_assertion_seen", "sso_keyring", "webauthn_credentials"} {
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

// TestAuthSchemaReconcilesOnOldUsers proves an existing users row survives the new columns:
// ensureColumns adds them with no backfill, the account keeps working, and the defaults are the
// safe ones (source 'local', 2FA off) so nobody is locked out or silently treated as federated.
func TestAuthSchemaReconcilesOnOldUsers(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	// users in the pre-ADR-0023 shape, already holding an account.
	if _, err := db.Exec(`CREATE TABLE users(username TEXT PRIMARY KEY, password_hash TEXT,
		role TEXT DEFAULT 'user', display_name TEXT, email TEXT, active INTEGER DEFAULT 1,
		last_login TEXT, group_id BIGINT, session_rev BIGINT DEFAULT 0, expires_at TEXT)`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE meta(k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta(k,v) VALUES('schema_version','2')`); err != nil {
		t.Fatalf("seed baseline: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(username,password_hash,role) VALUES('legacy','h','admin')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	st := &Store{db: db, driver: "sqlite"}
	if err := st.init(); err != nil {
		t.Fatalf("init (should auto-reconcile the auth columns): %v", err)
	}
	u := st.GetUser("legacy")
	if u == nil {
		t.Fatal("the pre-existing account disappeared after reconcile")
	}
	if u.Role != "admin" || !u.Active {
		t.Errorf("row not preserved: %+v", u)
	}
	if u.Source != "local" {
		t.Errorf("source = %q, want 'local' — a pre-existing account must never read as federated", u.Source)
	}
	if u.TOTPEnabled {
		t.Error("2FA must default to off for a pre-existing account")
	}
}
