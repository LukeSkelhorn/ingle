package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerifyPIN(t *testing.T) {
	h, err := HashPIN("1234")
	if err != nil {
		t.Fatalf("HashPIN: %v", err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Errorf("unexpected encoding %q", h)
	}
	if !VerifyPIN(h, "1234") {
		t.Error("correct PIN rejected")
	}
	if VerifyPIN(h, "4321") {
		t.Error("wrong PIN accepted")
	}

	h2, err := HashPIN("1234")
	if err != nil {
		t.Fatal(err)
	}
	if h == h2 {
		t.Error("hashes should be salted")
	}
}

func TestValidatePIN(t *testing.T) {
	for _, pin := range []string{"123", "1234567", "12a4", "", "١٢٣٤"} {
		if err := ValidatePIN(pin); !errors.Is(err, ErrInvalidPIN) {
			t.Errorf("ValidatePIN(%q) = %v, want ErrInvalidPIN", pin, err)
		}
		if _, err := HashPIN(pin); err == nil {
			t.Errorf("HashPIN(%q) succeeded", pin)
		}
	}
	for _, pin := range []string{"0000", "12345", "999999"} {
		if err := ValidatePIN(pin); err != nil {
			t.Errorf("ValidatePIN(%q) = %v", pin, err)
		}
	}
}

func TestVerifyPINMalformed(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$AA$AA", "$argon2id$v=19$m=x$AA$AA", "$argon2id$v=19$m=1,t=1,p=1$!!$AA"} {
		if VerifyPIN(h, "1234") {
			t.Errorf("VerifyPIN(%q) matched", h)
		}
	}
}
