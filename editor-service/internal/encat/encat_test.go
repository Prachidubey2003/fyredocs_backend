package encat

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"fyredocs/shared/keystore"
)

// makeKEK returns a 32-byte deterministic KEK from a seed byte.
// Pure helper so each test can use a distinct key.
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
		t.Fatal("Enabled() must be false when KEK is unset")
	}
}

func TestEnabled_TrueWhenTestKEKInstalled(t *testing.T) {
	SetKEKForTest(makeKEK(0x11))
	defer SetKEKForTest(nil)
	if !Enabled() {
		t.Fatal("Enabled() must be true when a test KEK is installed")
	}
}

func TestEnabled_TrueWhenEnvKEKSet(t *testing.T) {
	SetKEKForTest(nil)
	t.Setenv(kekEnvVar, hex.EncodeToString(makeKEK(0x22)))
	if !Enabled() {
		t.Fatal("Enabled() must be true when env KEK is set")
	}
}

func TestResolveKEK_RejectsMalformedEnvHex(t *testing.T) {
	SetKEKForTest(nil)
	t.Setenv(kekEnvVar, "not-hex-at-all")
	_, on, err := resolveKEK()
	if err == nil {
		t.Fatal("expected error on non-hex KEK")
	}
	if on {
		t.Error("on must be false when KEK fails to decode")
	}
}

func TestResolveKEK_RejectsWrongLength(t *testing.T) {
	SetKEKForTest(nil)
	// 16 bytes hex-encoded — half the expected length.
	t.Setenv(kekEnvVar, strings.Repeat("ab", 16))
	_, _, err := resolveKEK()
	if err == nil {
		t.Fatal("expected error on too-short KEK")
	}
}

// ---- SealSnapshot / OpenSnapshot ----

func TestSealOpen_RoundTripsWithKEK(t *testing.T) {
	SetKEKForTest(makeKEK(0x33))
	defer SetKEKForTest(nil)

	plain := []byte("yjs update bytes — pretend this is a CRDT blob")
	wrapped, sealed, err := SealSnapshot(plain)
	if err != nil {
		t.Fatalf("SealSnapshot: %v", err)
	}
	if len(wrapped) != keystore.WrappedDEKSize {
		t.Errorf("wrappedDEK = %d bytes; want %d", len(wrapped), keystore.WrappedDEKSize)
	}
	if bytes.Equal(sealed, plain) {
		t.Error("sealed bytes must not match plaintext (encryption was a no-op?)")
	}
	got, err := OpenSnapshot(wrapped, sealed)
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, plain)
	}
}

func TestSealSnapshot_PassthroughWhenKEKUnset(t *testing.T) {
	// Pass-through mode: handlers don't have to care about
	// whether encryption is configured. When KEK is off,
	// `wrappedDEK` is nil and `sealed` is the input verbatim.
	SetKEKForTest(nil)
	t.Setenv(kekEnvVar, "")

	plain := []byte("plaintext snapshot bytes")
	wrapped, sealed, err := SealSnapshot(plain)
	if err != nil {
		t.Fatalf("SealSnapshot: %v", err)
	}
	if wrapped != nil {
		t.Errorf("wrappedDEK must be nil in pass-through; got %d bytes", len(wrapped))
	}
	if !bytes.Equal(sealed, plain) {
		t.Errorf("pass-through sealed bytes must equal plaintext")
	}
}

func TestOpenSnapshot_BypassesWhenNoWrappedDEK(t *testing.T) {
	// Legacy / pre-keystore rows have empty WrappedDEK and the
	// file on disk is plaintext. The reader must surface those
	// verbatim regardless of whether a KEK is currently set.
	SetKEKForTest(makeKEK(0x44)) // KEK on — should NOT engage on legacy rows
	defer SetKEKForTest(nil)

	plain := []byte("legacy plaintext")
	got, err := OpenSnapshot(nil, plain)
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("legacy-row read must be verbatim; got %q", got)
	}
}

func TestOpenSnapshot_FailsWhenKEKMissingForSealedRow(t *testing.T) {
	// Seal under one KEK...
	SetKEKForTest(makeKEK(0x55))
	wrapped, sealed, err := SealSnapshot([]byte("payload"))
	if err != nil {
		t.Fatalf("SealSnapshot: %v", err)
	}
	// ...then turn encryption off and try to read.
	SetKEKForTest(nil)
	t.Setenv(kekEnvVar, "")
	_, err = OpenSnapshot(wrapped, sealed)
	if !errors.Is(err, ErrEncryptionDisabled) {
		t.Errorf("expected ErrEncryptionDisabled, got %v", err)
	}
}

func TestOpenSnapshot_FailsWithWrongKEK(t *testing.T) {
	// Seal with one KEK, attempt to open with another. Must
	// surface as ErrAuthFailed from the keystore layer — never
	// a partial decrypt, never a generic error.
	SetKEKForTest(makeKEK(0x66))
	wrapped, sealed, err := SealSnapshot([]byte("secret"))
	if err != nil {
		t.Fatalf("SealSnapshot: %v", err)
	}
	SetKEKForTest(makeKEK(0x77)) // different KEK
	defer SetKEKForTest(nil)
	_, err = OpenSnapshot(wrapped, sealed)
	if !errors.Is(err, keystore.ErrAuthFailed) {
		t.Errorf("expected keystore.ErrAuthFailed; got %v", err)
	}
}

func TestSealSnapshot_FreshDEKPerCall(t *testing.T) {
	// Two seals of identical plaintext under the same KEK
	// MUST produce distinct ciphertexts AND distinct wrapped
	// DEKs — a regression where the DEK is reused across
	// snapshots would silently leak plaintext correlation
	// through ciphertext equality.
	SetKEKForTest(makeKEK(0x88))
	defer SetKEKForTest(nil)
	plain := []byte("same input twice")
	w1, s1, err := SealSnapshot(plain)
	if err != nil {
		t.Fatalf("SealSnapshot #1: %v", err)
	}
	w2, s2, err := SealSnapshot(plain)
	if err != nil {
		t.Fatalf("SealSnapshot #2: %v", err)
	}
	if bytes.Equal(w1, w2) {
		t.Error("wrappedDEK must differ across calls (DEK should be fresh per snapshot)")
	}
	if bytes.Equal(s1, s2) {
		t.Error("sealed ciphertext must differ across calls (nonce should be fresh)")
	}
}
