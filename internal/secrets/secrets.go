// Package secrets encrypts the secrets a household stores in the
// database, such as calendar links with a key in them.
//
// It uses envelope encryption. Each household has its own random data key,
// stored in household_keys wrapped (encrypted) by the master key, which is
// held outside the database (INGLE_MASTER_KEY). A secret is encrypted with
// its household's data key, bound to the household, so a value copied into
// another household won't open. Deleting a household deletes its key, so
// anything encrypted with it, including in old backups, can't be read.
//
// Without a master key (self-hosted, by default) secrets are stored as they
// are and a nil *Keyring is fine to use. Stored values that aren't
// encrypted are read as they are either way, so turning the key on later
// works: they're encrypted the next time they're saved.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// KeySize is the length of the master key and of each data key: AES-256.
const KeySize = 32

// prefix marks an encrypted value: version 1 is AES-256-GCM with the
// household's data key, as base64 of nonce || ciphertext.
const prefix = "enc:v1:"

// wrapVersion is the first byte of a wrapped data key: 1 is AES-256-GCM
// with the master key. A key service (KMS) could wrap them later as 2.
const wrapVersion = 1

// ErrNoKey is returned for an encrypted value when no master key is set.
var ErrNoKey = errors.New("this is encrypted, and INGLE_MASTER_KEY isn't set")

// ParseMasterKey decodes a base64 master key of KeySize bytes, e.g. from
// `openssl rand -base64 32`.
func ParseMasterKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(s); err == nil {
			if len(k) != KeySize {
				return nil, fmt.Errorf("want %d bytes, got %d (make one with: openssl rand -base64 32)", KeySize, len(k))
			}
			return k, nil
		}
	}
	return nil, errors.New("not base64 (make one with: openssl rand -base64 32)")
}

// Keyring seals and opens one household's secrets at a time.
type Keyring struct {
	st     *store.Store
	master cipher.AEAD
	now    func() time.Time

	mu   sync.Mutex
	keys map[int64]cipher.AEAD
}

// New returns a Keyring using master (KeySize bytes). A nil master returns
// nil: secrets are stored as they are.
func New(st *store.Store, master []byte) (*Keyring, error) {
	if master == nil {
		return nil, nil
	}
	aead, err := newGCM(master)
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}
	return &Keyring{st: st, master: aead, now: time.Now, keys: map[int64]cipher.AEAD{}}, nil
}

// Enabled reports whether secrets are encrypted.
func (k *Keyring) Enabled() bool { return k != nil }

// IsSealed reports whether stored is an encrypted value.
func IsSealed(stored string) bool { return strings.HasPrefix(stored, prefix) }

// Seal encrypts plaintext for household hh, for storing. Without a master
// key it returns plaintext.
func (k *Keyring) Seal(ctx context.Context, hh int64, plaintext string) (string, error) {
	if k == nil {
		return plaintext, nil
	}
	aead, err := k.dataKey(ctx, hh, true)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), valueAAD(hh))
	return prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Open returns the plaintext of a value from Seal. A value that isn't
// encrypted (stored before the master key was set) is returned as it is.
func (k *Keyring) Open(ctx context.Context, hh int64, stored string) (string, error) {
	if !IsSealed(stored) {
		return stored, nil
	}
	if k == nil {
		return "", ErrNoKey
	}
	raw, err := base64.RawStdEncoding.DecodeString(stored[len(prefix):])
	if err != nil {
		return "", fmt.Errorf("decode secret: %w", err)
	}
	aead, err := k.dataKey(ctx, hh, false)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("decode secret: too short")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], valueAAD(hh))
	if err != nil {
		return "", errors.New("can't decrypt this secret: it's from another household, or the master key has changed")
	}
	return string(plain), nil
}

// dataKey returns household hh's data key, making it first if create is
// set and it has none.
func (k *Keyring) dataKey(ctx context.Context, hh int64, create bool) (cipher.AEAD, error) {
	k.mu.Lock()
	aead := k.keys[hh]
	k.mu.Unlock()
	if aead != nil {
		return aead, nil
	}

	ctx = store.WithHousehold(ctx, hh)
	wrapped, err := k.st.Q.GetHouseholdKey(ctx, hh)
	if errors.Is(err, sql.ErrNoRows) && create {
		if err := k.createKey(ctx, hh); err != nil {
			return nil, err
		}
		wrapped, err = k.st.Q.GetHouseholdKey(ctx, hh)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("this household has no key, so its secrets can't be read")
	}
	if err != nil {
		return nil, fmt.Errorf("get household key: %w", err)
	}
	key, err := k.unwrap(hh, wrapped)
	if err != nil {
		return nil, err
	}
	if aead, err = newGCM(key); err != nil {
		return nil, err
	}
	k.mu.Lock()
	k.keys[hh] = aead
	k.mu.Unlock()
	return aead, nil
}

func (k *Keyring) createKey(ctx context.Context, hh int64) error {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	nonce := make([]byte, k.master.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	wrapped := append([]byte{wrapVersion}, nonce...)
	wrapped = k.master.Seal(wrapped, nonce, key, keyAAD(hh))
	if err := k.st.Q.CreateHouseholdKey(ctx, dbq.CreateHouseholdKeyParams{
		HouseholdID: hh, WrappedKey: wrapped, CreatedAt: k.now().Unix(),
	}); err != nil {
		return fmt.Errorf("create household key: %w", err)
	}
	return nil
}

func (k *Keyring) unwrap(hh int64, wrapped []byte) ([]byte, error) {
	n := k.master.NonceSize()
	if len(wrapped) < 1+n || wrapped[0] != wrapVersion {
		return nil, errors.New("household key: unknown format")
	}
	key, err := k.master.Open(nil, wrapped[1:1+n], wrapped[1+n:], keyAAD(hh))
	if err != nil {
		return nil, errors.New("household key: can't unwrap it with this master key")
	}
	return key, nil
}

// Forget drops hh's cached data key, e.g. once the household is deleted.
func (k *Keyring) Forget(hh int64) {
	if k == nil {
		return
	}
	k.mu.Lock()
	delete(k.keys, hh)
	k.mu.Unlock()
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("want a %d-byte key, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func valueAAD(hh int64) []byte { return []byte("ingle secret, household " + strconv.FormatInt(hh, 10)) }

func keyAAD(hh int64) []byte { return []byte("ingle data key, household " + strconv.FormatInt(hh, 10)) }
