package secrets_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/secrets"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/store/storetest"
)

func masterKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, secrets.KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func households(t *testing.T, st *store.Store, n int) []int64 {
	t.Helper()
	var ids []int64
	for i := range n {
		h, err := st.Q.CreateHousehold(store.AsSystem(t.Context()), dbq.CreateHouseholdParams{Name: "H" + string(rune('A'+i)), CreatedAt: time.Now().Unix()})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, h.ID)
	}
	return ids
}

func TestSealAndOpen(t *testing.T) {
	st := store.New(storetest.OpenScoped(t, ""))
	hh := households(t, st, 2)
	a, b := hh[0], hh[1]
	ctx := t.Context()
	master := masterKey(t)
	k, err := secrets.New(st, master)
	if err != nil {
		t.Fatal(err)
	}

	const url = "https://calendar.example/private-abc123/basic.ics"
	sealed, err := k.Seal(ctx, a, url)
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.IsSealed(sealed) || strings.Contains(sealed, "abc123") {
		t.Fatalf("sealed = %q", sealed)
	}
	if again, _ := k.Seal(ctx, a, url); again == sealed {
		t.Error("sealing twice gave the same value; want a fresh nonce")
	}
	if got, err := k.Open(ctx, a, sealed); err != nil || got != url {
		t.Errorf("Open = %q, %v", got, err)
	}

	// Bound to its household: copied into B, it won't open.
	if _, err := k.Seal(ctx, b, "b's own"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Open(ctx, b, sealed); err == nil {
		t.Error("A's secret opened as household B")
	}

	// The data key is stored wrapped, not as it is.
	wrapped, err := st.Q.GetHouseholdKey(store.WithHousehold(ctx, a), a)
	if err != nil || len(wrapped) < secrets.KeySize+16 {
		t.Fatalf("wrapped key = %d bytes, %v", len(wrapped), err)
	}

	// A new Keyring (a restart) with the same master key opens it.
	k2, _ := secrets.New(st, master)
	if got, err := k2.Open(ctx, a, sealed); err != nil || got != url {
		t.Errorf("after restart: Open = %q, %v", got, err)
	}
	// Another master key can't.
	k3, _ := secrets.New(st, masterKey(t))
	if _, err := k3.Open(ctx, a, sealed); err == nil {
		t.Error("opened with the wrong master key")
	}
	// Without a key at all, it says why.
	var none *secrets.Keyring
	if _, err := none.Open(ctx, a, sealed); !errors.Is(err, secrets.ErrNoKey) {
		t.Errorf("no key: err = %v, want ErrNoKey", err)
	}
}

func TestNoMasterKeyStoresAsIs(t *testing.T) {
	k, err := secrets.New(nil, nil)
	if err != nil || k.Enabled() {
		t.Fatalf("New(nil) = %v, %v; want disabled", k, err)
	}
	got, err := k.Seal(t.Context(), 1, "https://x/y.ics")
	if err != nil || got != "https://x/y.ics" {
		t.Errorf("Seal = %q, %v", got, err)
	}
	if got, err := k.Open(t.Context(), 1, "https://x/y.ics"); err != nil || got != "https://x/y.ics" {
		t.Errorf("Open = %q, %v", got, err)
	}
}

func TestParseMasterKey(t *testing.T) {
	k := masterKey(t)
	for _, s := range []string{base64.StdEncoding.EncodeToString(k), base64.RawURLEncoding.EncodeToString(k) + "\n"} {
		if got, err := secrets.ParseMasterKey(s); err != nil || !bytes.Equal(got, k) {
			t.Errorf("ParseMasterKey(%q) = %v", s, err)
		}
	}
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(k[:16])} {
		if _, err := secrets.ParseMasterKey(bad); err == nil {
			t.Errorf("ParseMasterKey(%q) accepted", bad)
		}
	}
}
