// Package auth handles adult credentials and access: PINs (which unlock
// admin on a registered household display), passwords (for signing in from
// personal devices), household display registration and sessions.
package auth

import "errors"

// ErrInvalidPIN is returned when a PIN is not 4–6 digits.
var ErrInvalidPIN = errors.New("PIN must be 4 to 6 digits")

// ValidatePIN reports whether pin is 4–6 ASCII digits.
func ValidatePIN(pin string) error {
	if len(pin) < 4 || len(pin) > 6 {
		return ErrInvalidPIN
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return ErrInvalidPIN
		}
	}
	return nil
}

// HashPIN returns an encoded argon2id hash of pin.
func HashPIN(pin string) (string, error) {
	if err := ValidatePIN(pin); err != nil {
		return "", err
	}
	return hashSecret(pin)
}

// VerifyPIN reports whether pin matches the encoded hash. Malformed hashes
// never match.
func VerifyPIN(encoded, pin string) bool {
	return verifySecret(encoded, pin)
}
