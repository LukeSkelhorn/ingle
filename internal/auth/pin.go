// Package auth handles credentials and access: PINs (an adult's unlocks
// admin on a registered household display; anyone's opens their own My
// money there), passwords (for signing in from
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

// PINLength is the stored length of a valid PIN or code, so the keypad can
// show that many dots and try it once they're all typed.
func PINLength(pin string) *int64 {
	n := int64(len(pin))
	return &n
}

// VerifyPIN reports whether pin matches the encoded hash. Malformed hashes
// never match.
func VerifyPIN(encoded, pin string) bool {
	return verifySecret(encoded, pin)
}
