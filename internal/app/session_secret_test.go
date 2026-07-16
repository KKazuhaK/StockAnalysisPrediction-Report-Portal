package app

import "testing"

func TestValidateSessionSecret(t *testing.T) {
	for _, secret := range []string{"", "short", "replace-with-a-long-random-string"} {
		if err := validateSessionSecret(secret); err == nil {
			t.Errorf("validateSessionSecret(%q) = nil, want error", secret)
		}
	}
	if err := validateSessionSecret("f43f0a1fece3f0901c3e2b56f7d95cb72f919feca3c3eff93be83065e53696cf"); err != nil {
		t.Fatalf("valid random secret rejected: %v", err)
	}
}
