package app

import (
	"errors"
	"unicode/utf8"
)

const minPasswordRunes = 12

func validateNewPassword(password string) error {
	if utf8.RuneCountInString(password) < minPasswordRunes {
		return errors.New("password must be at least 12 characters")
	}
	// bcrypt rejects inputs over 72 bytes. Return a stable validation error instead of
	// relying on individual callers to notice a hashing failure.
	if len(password) > 72 {
		return errors.New("password must be at most 72 bytes")
	}
	return nil
}
