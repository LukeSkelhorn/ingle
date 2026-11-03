package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (RFC 9106 second recommended option, scaled for a
// small home server).
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

// hashSecret returns an encoded argon2id hash of secret in the PHC string
// format: $argon2id$v=19$m=...,t=...,p=...$salt$key.
func hashSecret(secret string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}
	key := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// verifySecret reports whether secret matches the encoded hash. Malformed
// hashes never match.
func verifySecret(encoded, secret string) bool {
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
	got := argon2.IDKey([]byte(secret), salt, iterations, memory, threads, uint32(len(want))) //nolint:gosec // len(want) is a decoded hash, far below MaxUint32
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is verified against when an account doesn't exist, so a failed
// sign-in takes the same time whether or not the email is known.
var dummyHash = func() string {
	h, err := hashSecret("domus-dummy-password")
	if err != nil {
		panic(err)
	}
	return h
}()
