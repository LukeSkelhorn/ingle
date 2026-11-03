// Package auth handles household PINs used to gate adult actions such as
// approving chores and unlocking the board.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrInvalidPIN is returned when a PIN is not 4–6 digits.
var ErrInvalidPIN = errors.New("PIN must be 4 to 6 digits")

// argon2id parameters (RFC 9106 second recommended option, scaled for a PIN
// check on a small home server).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16

	maxMemory = 256 * 1024
	maxTime   = 10
)

var b64 = base64.RawStdEncoding

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

// HashPIN returns an encoded argon2id hash of pin in the PHC string format.
func HashPIN(pin string) (string, error) {
	if err := ValidatePIN(pin); err != nil {
		return "", err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}
	key := argon2.IDKey([]byte(pin), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPIN reports whether pin matches the encoded hash. Malformed hashes
// never match.
func VerifyPIN(encoded, pin string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	// Refuse absurd parameters so a tampered hash can't exhaust the server.
	if memory == 0 || memory > maxMemory || iterations == 0 || iterations > maxTime || threads == 0 {
		return false
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pin), salt, iterations, memory, threads, uint32(len(want))) //nolint:gosec // len(want) is a decoded hash, far below MaxUint32
	return subtle.ConstantTimeCompare(got, want) == 1
}
