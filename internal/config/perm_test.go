package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The config file holds secret_key — which signs every session cookie and, through HKDF, wraps the
// SSO keyring — and during a rotation it holds the previous key beside it, so a readable config is
// two live keys on disk. The generated file is 0600, but one copied from the example, restored from
// an archive or checked out of a repository arrives with whatever mode it had.
func TestWarnIfWorldReadable(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("secret_key: \"x\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil { // WriteFile's mode is masked by umask
			t.Fatal(err)
		}
		return p
	}

	for _, c := range []struct {
		mode os.FileMode
		warn bool
	}{
		{0o600, false},
		{0o400, false},
		{0o640, true}, // the group can read the signing key
		{0o604, true}, // so can everyone
		{0o666, true},
		{0o777, true},
	} {
		p := write(c.mode.String(), c.mode)
		msg := WarnIfWorldReadable(p)
		if (msg != "") != c.warn {
			t.Errorf("mode %04o: warned=%v, want %v (%q)", c.mode, msg != "", c.warn, msg)
		}
		if c.warn {
			// The remedy has to be in it. An operator who has to work out the command is an
			// operator who leaves it as it is.
			if !strings.Contains(msg, "chmod 600") || !strings.Contains(msg, p) {
				t.Errorf("mode %04o: the warning must name the file and the fix; got %q", c.mode, msg)
			}
		}
	}

	// A path that is not there, or is a directory, is not this function's problem to report — the
	// config loader already fails loudly on a missing file.
	if msg := WarnIfWorldReadable(filepath.Join(dir, "nope.yaml")); msg != "" {
		t.Errorf("a missing file must not warn; got %q", msg)
	}
	if msg := WarnIfWorldReadable(dir); msg != "" {
		t.Errorf("a directory must not warn; got %q", msg)
	}
}
