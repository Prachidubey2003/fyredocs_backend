package keystore

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"
)

// fixedReader is a deterministic io.Reader: it returns the
// supplied bytes in order then errors on exhaustion. Used to
// pin nonces in tests so wrap/seal outputs are reproducible.
type fixedReader struct{ buf []byte }

func (f *fixedReader) Read(p []byte) (int, error) {
	if len(f.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

// genKey returns a deterministic KeySize-byte key derived from
// a label. Saves typing the full 32-byte literal in every test.
func genKey(label byte) []byte {
	k := make([]byte, KeySize)
	for i := range k {
		k[i] = label
	}
	return k
}

// ---- DEK generation ----------------------------------------------------

func TestGenerateDEK_Returns32RandomBytes(t *testing.T) {
	dek, err := GenerateDEK()
	if err != nil {
		t.Fatalf("GenerateDEK: %v", err)
	}
	if len(dek) != KeySize {
		t.Errorf("len(dek) = %d, want %d", len(dek), KeySize)
	}
	// Two calls should differ overwhelmingly likely. We can't
	// assert never-equal at probability 1, but at KeySize=32
	// the chance of collision is 2^-256 — effectively never.
	dek2, _ := GenerateDEK()
	if bytes.Equal(dek, dek2) {
		t.Error("two GenerateDEK calls returned identical bytes (CSPRNG broken?)")
	}
}

func TestGenerateDEKFrom_DeterministicWithFixedReader(t *testing.T) {
	src := &fixedReader{buf: genKey(0xAA)}
	dek, err := GenerateDEKFrom(src)
	if err != nil {
		t.Fatalf("GenerateDEKFrom: %v", err)
	}
	for _, b := range dek {
		if b != 0xAA {
			t.Errorf("dek[*] = %x, want 0xAA (deterministic input)", b)
			break
		}
	}
}

func TestGenerateDEKFrom_NilReaderRejected(t *testing.T) {
	if _, err := GenerateDEKFrom(nil); err == nil {
		t.Error("nil reader should be rejected")
	}
}

func TestGenerateDEKFrom_ShortReaderErrors(t *testing.T) {
	src := &fixedReader{buf: []byte{0x01, 0x02}} // not enough bytes
	if _, err := GenerateDEKFrom(src); err == nil {
		t.Error("short reader should produce an error")
	}
}

// ---- Wrap / unwrap round-trip -----------------------------------------

func TestWrapDEK_OutputShapeAndRoundTrip(t *testing.T) {
	kek := genKey(0x10)
	dek := genKey(0x20)
	wrapped, err := WrapDEK(kek, dek, rand.Reader)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	if len(wrapped) != WrappedDEKSize {
		t.Errorf("len(wrapped) = %d, want %d", len(wrapped), WrappedDEKSize)
	}
	got, err := UnwrapDEK(kek, wrapped)
	if err != nil {
		t.Fatalf("UnwrapDEK: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Errorf("round-trip dek mismatch")
	}
}

func TestWrapDEK_TwoCallsProduceDifferentCiphertext(t *testing.T) {
	// Same KEK + same DEK + crypto/rand source must NOT produce
	// the same wrapped output — the nonce is fresh per call, so
	// ciphertext + tag differ. Guards against a nonce-reuse
	// regression that would break GCM's security model.
	kek := genKey(0x10)
	dek := genKey(0x20)
	a, _ := WrapDEK(kek, dek, rand.Reader)
	b, _ := WrapDEK(kek, dek, rand.Reader)
	if bytes.Equal(a, b) {
		t.Error("two WrapDEK calls produced identical ciphertext — nonce reuse!")
	}
	// Both should still round-trip.
	for _, w := range [][]byte{a, b} {
		got, err := UnwrapDEK(kek, w)
		if err != nil {
			t.Fatalf("UnwrapDEK: %v", err)
		}
		if !bytes.Equal(got, dek) {
			t.Error("round-trip mismatch")
		}
	}
}

// ---- Authentication invariants ----------------------------------------

func TestUnwrapDEK_WrongKEKFailsWithAuthFailed(t *testing.T) {
	wrapped, _ := WrapDEK(genKey(0x10), genKey(0x20), rand.Reader)
	_, err := UnwrapDEK(genKey(0x11), wrapped) // KEK differs by one byte
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("err = %v, want ErrAuthFailed (wrong KEK)", err)
	}
}

func TestUnwrapDEK_TamperedCiphertextFailsWithAuthFailed(t *testing.T) {
	kek := genKey(0x10)
	wrapped, _ := WrapDEK(kek, genKey(0x20), rand.Reader)
	// Flip a bit in the middle (the DEK ciphertext payload).
	tampered := append([]byte{}, wrapped...)
	tampered[NonceSize+5] ^= 0x01
	if _, err := UnwrapDEK(kek, tampered); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("err = %v, want ErrAuthFailed (tampered ciphertext)", err)
	}
}

func TestUnwrapDEK_TamperedNonceFailsWithAuthFailed(t *testing.T) {
	kek := genKey(0x10)
	wrapped, _ := WrapDEK(kek, genKey(0x20), rand.Reader)
	tampered := append([]byte{}, wrapped...)
	tampered[3] ^= 0xFF // poison the nonce
	if _, err := UnwrapDEK(kek, tampered); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("err = %v, want ErrAuthFailed (nonce poisoned)", err)
	}
}

func TestUnwrapDEK_TruncatedFailsWithSizeError(t *testing.T) {
	wrapped, _ := WrapDEK(genKey(0x10), genKey(0x20), rand.Reader)
	if _, err := UnwrapDEK(genKey(0x10), wrapped[:WrappedDEKSize-1]); !errors.Is(err, ErrWrappedDEKSize) {
		t.Errorf("err = %v, want ErrWrappedDEKSize", err)
	}
	if _, err := UnwrapDEK(genKey(0x10), nil); !errors.Is(err, ErrWrappedDEKSize) {
		t.Errorf("nil wrapped: err = %v, want ErrWrappedDEKSize", err)
	}
}

func TestWrapDEK_RejectsWrongKeySize(t *testing.T) {
	cases := []struct{ kek, dek []byte }{
		{[]byte("short"), genKey(0x20)},
		{genKey(0x10), []byte("short")},
		{nil, genKey(0x20)},
		{genKey(0x10), nil},
		{make([]byte, KeySize-1), genKey(0x20)}, // off by one
	}
	for i, c := range cases {
		if _, err := WrapDEK(c.kek, c.dek, rand.Reader); !errors.Is(err, ErrKeySize) {
			t.Errorf("case %d: err = %v, want ErrKeySize", i, err)
		}
	}
}

// ---- Rewrap (key rotation) --------------------------------------------

func TestRewrapDEK_RotatesWithoutTouchingPayload(t *testing.T) {
	oldKEK := genKey(0x10)
	newKEK := genKey(0x11)
	dek := genKey(0x20)
	wrapped, _ := WrapDEK(oldKEK, dek, rand.Reader)

	rewrapped, err := RewrapDEK(oldKEK, newKEK, wrapped, rand.Reader)
	if err != nil {
		t.Fatalf("RewrapDEK: %v", err)
	}
	// The new wrapping should unwrap with the new KEK back to
	// the original DEK.
	got, err := UnwrapDEK(newKEK, rewrapped)
	if err != nil {
		t.Fatalf("Unwrap with new KEK: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Error("rewrapped DEK differs from original")
	}
	// And it must NOT unwrap with the old KEK.
	if _, err := UnwrapDEK(oldKEK, rewrapped); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("rewrapped DEK should not unwrap with old KEK; err = %v", err)
	}
}

func TestRewrapDEK_OldKEKMismatchSurfaces(t *testing.T) {
	wrapped, _ := WrapDEK(genKey(0x10), genKey(0x20), rand.Reader)
	_, err := RewrapDEK(genKey(0x11), genKey(0x12), wrapped, rand.Reader)
	if err == nil {
		t.Fatal("expected error on wrong oldKEK")
	}
	if !strings.Contains(err.Error(), "unwrap with old KEK") {
		t.Errorf("err = %v, should mention unwrap-with-old-kek", err)
	}
}

// ---- Document body sealing -------------------------------------------

func TestSealWithDEK_OpenRoundTrip(t *testing.T) {
	dek := genKey(0x20)
	plaintext := []byte("the quick brown fox jumps over the lazy document body")
	sealed, err := SealWithDEK(dek, plaintext, rand.Reader)
	if err != nil {
		t.Fatalf("SealWithDEK: %v", err)
	}
	if len(sealed) != len(plaintext)+NonceSize+GCMTagSize {
		t.Errorf("len(sealed) = %d, want %d (nonce + plaintext + tag)",
			len(sealed), len(plaintext)+NonceSize+GCMTagSize)
	}
	got, err := OpenWithDEK(dek, sealed)
	if err != nil {
		t.Fatalf("OpenWithDEK: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip mismatch:\n  got:  %q\n  want: %q", got, plaintext)
	}
}

func TestSealWithDEK_EmptyPlaintextRoundTrip(t *testing.T) {
	// Edge: an empty document body still produces a
	// nonce+tag and round-trips to zero-length bytes. Useful
	// for tombstone rows where the body is gone but the
	// ciphertext envelope is kept for audit.
	dek := genKey(0x20)
	sealed, err := SealWithDEK(dek, nil, rand.Reader)
	if err != nil {
		t.Fatalf("SealWithDEK: %v", err)
	}
	if len(sealed) != NonceSize+GCMTagSize {
		t.Errorf("len(sealed) = %d, want %d", len(sealed), NonceSize+GCMTagSize)
	}
	got, err := OpenWithDEK(dek, sealed)
	if err != nil {
		t.Fatalf("OpenWithDEK: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty round-trip = %q, want empty", got)
	}
}

func TestOpenWithDEK_WrongDEKFailsWithAuthFailed(t *testing.T) {
	sealed, _ := SealWithDEK(genKey(0x20), []byte("hello"), rand.Reader)
	if _, err := OpenWithDEK(genKey(0x21), sealed); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("err = %v, want ErrAuthFailed", err)
	}
}

func TestOpenWithDEK_TamperedPayloadFailsWithAuthFailed(t *testing.T) {
	dek := genKey(0x20)
	sealed, _ := SealWithDEK(dek, []byte("hello world"), rand.Reader)
	tampered := append([]byte{}, sealed...)
	tampered[NonceSize+3] ^= 0x80
	if _, err := OpenWithDEK(dek, tampered); !errors.Is(err, ErrAuthFailed) {
		t.Errorf("err = %v, want ErrAuthFailed", err)
	}
}

func TestOpenWithDEK_TooShortPayloadRejected(t *testing.T) {
	dek := genKey(0x20)
	for _, n := range []int{0, NonceSize, NonceSize + GCMTagSize - 1} {
		if _, err := OpenWithDEK(dek, make([]byte, n)); err == nil {
			t.Errorf("len(%d): expected too-short error, got nil", n)
		}
	}
}

// ---- End-to-end envelope encryption -----------------------------------

func TestEndToEnd_EnvelopeEncryption(t *testing.T) {
	// The realistic flow:
	//   1. Tenant KEK held by auth-service (Vault).
	//   2. Per-document: GenerateDEK + SealWithDEK on the body.
	//   3. WrapDEK(KEK, DEK) → store wrapped DEK + sealed
	//      body in Postgres.
	//   4. On read: UnwrapDEK with the same KEK → OpenWithDEK
	//      on the body.
	// Confirm the round-trip works for a realistic-sized payload.
	kek := genKey(0x10)
	dek, err := GenerateDEK()
	if err != nil {
		t.Fatalf("GenerateDEK: %v", err)
	}
	body := bytes.Repeat([]byte("Fyredocs PDF byte content. "), 1000) // ~27KB
	sealedBody, err := SealWithDEK(dek, body, rand.Reader)
	if err != nil {
		t.Fatalf("SealWithDEK: %v", err)
	}
	wrappedDEK, err := WrapDEK(kek, dek, rand.Reader)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}

	// === storage round-trip ===
	storedDEK := append([]byte{}, wrappedDEK...)
	storedBody := append([]byte{}, sealedBody...)

	recoveredDEK, err := UnwrapDEK(kek, storedDEK)
	if err != nil {
		t.Fatalf("UnwrapDEK on retrieval: %v", err)
	}
	recoveredBody, err := OpenWithDEK(recoveredDEK, storedBody)
	if err != nil {
		t.Fatalf("OpenWithDEK on retrieval: %v", err)
	}
	if !bytes.Equal(recoveredBody, body) {
		t.Error("end-to-end body mismatch")
	}
}
