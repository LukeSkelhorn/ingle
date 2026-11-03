package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// NewToken returns a random 256-bit token, base64url encoded for cookies.
func NewToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the sha256 of a token. Only hashes are stored, so a
// leaked database can't be replayed as cookies.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
