package app

import (
	"fmt"
	"strings"
	"testing"
)

// A principal table is one keyed by "who" — `g:<id>` for an OU, `u:<name>` for an account. There
// are three of them now (report_viewers and version_grants from ADR 0024, announcement_grants from
// ADR 0025) and the same rule holds for all of them: when the thing a principal NAMES is deleted,
// its rows must go too.
//
// The rule is not tidiness, it is disclosure, and it is disclosure in both directions. Usernames
// are reusable — delete an account and the same name can register again — and OU ids are assigned
// by the database, so a deleted one comes back around. A row left behind is silently inherited by
// whoever holds that name or that id next: reports they were never granted, versions they cannot
// see, announcements addressed to somebody who left.
//
// So this test does not check the three tables that exist today. It reads the schema, finds every
// table with a `principal` column, seeds a row in each, and deletes the account and the OU. A
// fourth principal table added later fails here on the day it is added, rather than on the day
// somebody notices the wrong person is reading something.

// principalTables finds them the only way that cannot go stale: from baseSchemaStmts, which is the
// single source of truth for the schema.
func principalTables(s *Store) []string {
	var out []string
	for _, stmt := range s.baseSchemaStmts() {
		table, cols, ok := parseCreateTable(stmt)
		if !ok {
			continue
		}
		for _, c := range cols {
			if c.name == "principal" {
				out = append(out, table)
				break
			}
		}
	}
	return out
}

// seedPrincipalRow inserts one row naming `principal`, filling every other column with something
// valid for its declared type. Generic on purpose: a new principal table must be seedable here
// without anybody remembering to teach this test about it.
func seedPrincipalRow(t *testing.T, s *Store, table, principal string) {
	t.Helper()
	for _, stmt := range s.baseSchemaStmts() {
		name, cols, ok := parseCreateTable(stmt)
		if !ok || name != table {
			continue
		}
		var names []string
		var args []any
		for _, c := range cols {
			names = append(names, c.name)
			switch {
			case c.name == "principal":
				args = append(args, principal)
			case strings.Contains(strings.ToUpper(c.def), "INT"):
				args = append(args, 1)
			default:
				args = append(args, "x")
			}
		}
		q := fmt.Sprintf("INSERT INTO %s(%s) VALUES(%s)",
			table, strings.Join(names, ","), strings.TrimSuffix(strings.Repeat("?,", len(args)), ","))
		if _, err := s.exec(q, args...); err != nil {
			t.Fatalf("seed %s: %v [%s]", table, err, q)
		}
		return
	}
	t.Fatalf("no CREATE TABLE found for %s", table)
}

func principalRows(t *testing.T, s *Store, table, principal string) int {
	t.Helper()
	var n int
	s.queryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE principal=?", table), principal).Scan(&n)
	return n
}

func TestEveryPrincipalTableIsSweptOnDelete(t *testing.T) {
	st := newTestStore(t)
	tables := principalTables(st)
	if len(tables) < 3 {
		t.Fatalf("found %v principal tables; expected at least report_viewers, version_grants and "+
			"announcement_grants — has the schema or parseCreateTable changed?", tables)
	}

	t.Run("deleting an account", func(t *testing.T) {
		if err := st.UpsertUser(User{Username: "leaver", PasswordHash: "h", Role: "user", Active: true}); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		principal := userPrincipal("leaver")
		for _, table := range tables {
			seedPrincipalRow(t, st, table, principal)
		}
		if err := st.DeleteUser("leaver"); err != nil {
			t.Fatalf("DeleteUser: %v", err)
		}
		for _, table := range tables {
			if n := principalRows(t, st, table, principal); n != 0 {
				t.Errorf("%d row(s) in %s outlived the account they name — the next holder of "+
					"that username inherits them. Add the sweep to Store.DeleteUser.", n, table)
			}
		}
	})

	t.Run("deleting an OU", func(t *testing.T) {
		id, err := st.CreateUserGroup("被删的组", "", 0)
		if err != nil {
			t.Fatalf("seed OU: %v", err)
		}
		principal := groupPrincipal(id)
		for _, table := range tables {
			seedPrincipalRow(t, st, table, principal)
		}
		if err := st.DeleteUserGroup(id); err != nil {
			t.Fatalf("DeleteUserGroup: %v", err)
		}
		for _, table := range tables {
			if n := principalRows(t, st, table, principal); n != 0 {
				t.Errorf("%d row(s) in %s outlived the OU they name — ids are reused, so the next "+
					"OU inherits them. Add the sweep to Store.DeleteUserGroup.", n, table)
			}
		}
	})
}
