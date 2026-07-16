package app

import "testing"

func TestValidateNewPassword(t *testing.T) {
	for _, password := range []string{"", "123456", "short-pass"} {
		if validateNewPassword(password) == nil {
			t.Errorf("accepted weak password %q", password)
		}
	}
	if err := validateNewPassword("correct horse battery staple"); err != nil {
		t.Fatalf("rejected strong password: %v", err)
	}
	if err := validateNewPassword(string(make([]byte, 73))); err == nil {
		t.Fatal("accepted a password bcrypt cannot represent")
	}
}
