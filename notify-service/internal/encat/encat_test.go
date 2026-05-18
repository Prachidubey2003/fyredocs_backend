package encat

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"fyredocs/shared/keystore"
)

// makeKEK returns a 32-byte deterministic KEK from a seed byte
// so each test gets a distinct master key without sharing
// state via crypto/rand.
func makeKEK(seed byte) []byte {
	k := make([]byte, keystore.KeySize)
	for i := range k {
		k[i] = seed
	}
	return k
}

func TestEnabled_FalseWhenNoKEK(t *testing.T) {
	SetKEKForTest(nil)
	t.Setenv(kekEnvVar, "")
	if Enabled() {
		t.Fatal("Enabled() must be false when no KEK is set")
	}
}

func TestEnabled_TrueWhenTestKEKInstalled(t *testing.T) {
	SetKEKForTest(makeKEK(0x11))
	defer SetKEKForTest(nil)
	if !Enabled() {
		t.Fatal("Enabled() must be true with a test KEK")
	}
}

func TestEnabled_TrueWhenEnvKEKSet(t *testing.T) {
	SetKEKForTest(nil)
	t.Setenv(kekEnvVar, hex.EncodeToString(makeKEK(0x22)))
	if !Enabled() {
		t.Fatal("Enabled() must be true with an env KEK")
	}
}

func TestResolveKEK_RejectsBadHex(t *testing.T) {
	SetKEKForTest(nil)
	t.Setenv(kekEnvVar, "not-hex")
	_, on, err := resolveKEK()
	if err == nil {
		t.Fatal("expected error on bad hex")
	}
	if on {
		t.Error("on must be false when decode fails")
	}
}

func TestResolveKEK_RejectsWrongLength(t *testing.T) {
	SetKEKForTest(nil)
	t.Setenv(kekEnvVar, strings.Repeat("ab", 16)) // 16 bytes
	_, _, err := resolveKEK()
	if err == nil {
		t.Fatal("expected error on too-short KEK")
	}
}

// ---- SealSecret / OpenSecret ----

func TestSealOpen_RoundTripsWithKEK(t *testing.T) {
	SetKEKForTest(makeKEK(0x33))
	defer SetKEKForTest(nil)

	plain := []byte("super-secret-webhook-key-deadbeef")
	wrapped, sealed, err := SealSecret(plain)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	if len(wrapped) != keystore.WrappedDEKSize {
		t.Errorf("wrappedDEK = %d bytes; want %d", len(wrapped), keystore.WrappedDEKSize)
	}
	if bytes.Equal(sealed, plain) {
		t.Error("sealed bytes must not equal plaintext")
	}
	got, err := OpenSecret(wrapped, sealed)
	if err != nil {
		t.Fatalf("OpenSecret: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, plain)
	}
}

func TestSealSecret_PassThroughWhenKEKUnset(t *testing.T) {
	// No KEK → wrappedDEK is nil and sealed IS plaintext.
	SetKEKForTest(nil)
	t.Setenv(kekEnvVar, "")

	plain := []byte("plaintext-passthrough")
	wrapped, sealed, err := SealSecret(plain)
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	if wrapped != nil {
		t.Errorf("wrappedDEK must be nil in pass-through; got %d bytes", len(wrapped))
	}
	if !bytes.Equal(sealed, plain) {
		t.Error("pass-through sealed bytes must equal plaintext")
	}
}

func TestOpenSecret_BypassesWhenNoWrappedDEK(t *testing.T) {
	// Legacy/pass-through rows: empty wrappedDEK + plaintext
	// bytes in ciphertext column. Reader returns verbatim
	// regardless of whether a KEK is currently set.
	SetKEKForTest(makeKEK(0x44))
	defer SetKEKForTest(nil)

	plain := []byte("legacy-plaintext")
	got, err := OpenSecret(nil, plain)
	if err != nil {
		t.Fatalf("OpenSecret: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("legacy row read must be verbatim; got %q", got)
	}
}

func TestOpenSecret_FailsWhenKEKMissingForSealedRow(t *testing.T) {
	// Seal under one KEK...
	SetKEKForTest(makeKEK(0x55))
	wrapped, sealed, err := SealSecret([]byte("payload"))
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	// ...then unset and try to read.
	SetKEKForTest(nil)
	t.Setenv(kekEnvVar, "")

	_, err = OpenSecret(wrapped, sealed)
	if !errors.Is(err, ErrEncryptionDisabled) {
		t.Errorf("expected ErrEncryptionDisabled; got %v", err)
	}
}

func TestOpenSecret_FailsWithWrongKEK(t *testing.T) {
	SetKEKForTest(makeKEK(0x66))
	wrapped, sealed, err := SealSecret([]byte("secret"))
	if err != nil {
		t.Fatalf("SealSecret: %v", err)
	}
	SetKEKForTest(makeKEK(0x77)) // rotated to a different KEK
	defer SetKEKForTest(nil)

	_, err = OpenSecret(wrapped, sealed)
	if !errors.Is(err, keystore.ErrAuthFailed) {
		t.Errorf("expected keystore.ErrAuthFailed; got %v", err)
	}
}

func TestSealSecret_FreshDEKPerCall(t *testing.T) {
	// Two seals of identical plaintext under the same KEK
	// MUST produce distinct ciphertexts AND distinct wrapped
	// DEKs — defends against a regression where the DEK is
	// reused across rows (would leak plaintext correlation
	// via ciphertext equality).
	SetKEKForTest(makeKEK(0x88))
	defer SetKEKForTest(nil)

	plain := []byte("same input twice")
	w1, s1, err := SealSecret(plain)
	if err != nil {
		t.Fatalf("SealSecret #1: %v", err)
	}
	w2, s2, err := SealSecret(plain)
	if err != nil {
		t.Fatalf("SealSecret #2: %v", err)
	}
	if bytes.Equal(w1, w2) {
		t.Error("wrappedDEK must differ across calls (DEK should be fresh per secret)")
	}
	if bytes.Equal(s1, s2) {
		t.Error("sealed ciphertext must differ across calls (nonce should be fresh)")
	}
}
