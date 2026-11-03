package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "correct horse battery") || VerifyPassword(h, "correct horse batterY") {
		t.Error("password verification wrong")
	}
	for _, pw := range []string{"short", strings.Repeat("x", MaxPasswordLen+1), "\xff\xfe invalid utf8"} {
		if _, err := HashPassword(pw); !errors.Is(err, ErrInvalidPassword) {
			t.Errorf("HashPassword(%q) = %v, want ErrInvalidPassword", pw, err)
		}
	}
	// Length is in characters, not bytes.
	if err := ValidatePassword("ééééééééé"); err == nil {
		t.Error("9 characters accepted")
	}
}

func TestTokens(t *testing.T) {
	a, b := NewToken(), NewToken()
	if a == b || len(a) < 40 {
		t.Errorf("tokens %q %q", a, b)
	}
	ha1, ha2, hb := HashToken(a), HashToken(a), HashToken(b)
	if string(ha1) != string(ha2) || string(ha1) == string(hb) {
		t.Error("HashToken not deterministic/distinct")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	l := NewLimiter(3, 5*time.Minute)
	l.now = func() time.Time { return now }

	for range 2 {
		if l.Fail("k") {
			t.Fatal("blocked too early")
		}
	}
	if !l.Fail("k") {
		t.Fatal("not blocked after 3 failures")
	}
	if blocked, _ := l.Blocked("k"); !blocked {
		t.Fatal("Blocked = false")
	}
	if blocked, _ := l.Blocked("other"); blocked {
		t.Fatal("other key blocked")
	}
	now = now.Add(5 * time.Minute)
	if blocked, _ := l.Blocked("k"); blocked {
		t.Fatal("still blocked after window")
	}
	// The count starts again after a block expires.
	if l.Fail("k") {
		t.Fatal("blocked on first failure after expiry")
	}
	// Reset clears failures.
	l.Fail("k")
	l.Reset("k")
	for range 2 {
		if l.Fail("k") {
			t.Fatal("Reset didn't clear failures")
		}
	}
	// Failures spread wider than the window don't accumulate.
	l.Reset("k")
	l.Fail("k")
	now = now.Add(6 * time.Minute)
	l.Fail("k")
	now = now.Add(6 * time.Minute)
	if l.Fail("k") {
		t.Fatal("spread-out failures accumulated")
	}
}
