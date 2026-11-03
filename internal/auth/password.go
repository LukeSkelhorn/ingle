package auth

import (
	"errors"
	"unicode/utf8"
)

// Password length limits, in characters.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 128
)

// ErrInvalidPassword is returned when a password is too short or too long.
var ErrInvalidPassword = errors.New("password must be 10 to 128 characters")

// ValidatePassword checks the length limits.
func ValidatePassword(pw string) error {
	if !utf8.ValidString(pw) {
		return ErrInvalidPassword
	}
	if n := utf8.RuneCountInString(pw); n < MinPasswordLen || n > MaxPasswordLen {
		return ErrInvalidPassword
	}
	return nil
}

// HashPassword returns an encoded argon2id hash of pw.
func HashPassword(pw string) (string, error) {
	if err := ValidatePassword(pw); err != nil {
		return "", err
	}
	return hashSecret(pw)
}

// VerifyPassword reports whether pw matches the encoded hash.
func VerifyPassword(encoded, pw string) bool {
	return verifySecret(encoded, pw)
}
