// Package keystore is the envelope-encryption primitive for the
// Fyredocs platform. Per plan §4.4.6, sensitive-tier tenants
// get per-document envelope encryption: each document body is
// encrypted with a freshly-generated DEK (Data Encryption Key),
// and that DEK is wrapped with the tenant's KEK (Key Encryption
// Key) before being stored alongside the ciphertext.
//
// This package owns the cryptographic primitive — pure, no IO,
// no global state. The KEK supply (Vault / SOPS / per-tenant
// LUKS-mount secret store) lives in auth-service, which holds
// `[]byte` keys and calls into here.
//
// Cipher choice: AES-256-GCM. Standard, FIPS-validated, fast on
// every modern CPU (AES-NI), produces a small overhead per
// payload (16-byte tag + nonce). The package fixes the keysize
// at 256 bits so callers can't accidentally downgrade.
//
// Wire format of the wrapped DEK: `nonce || dekCiphertext ||
// gcmTag`, fixed-position. Length is exactly:
//
//	NonceSize + DEKSize + GCMTagSize = 12 + 32 + 16 = 60 bytes
//
// The document ciphertext is its own AES-256-GCM envelope
// produced the same way — caller stores it next to the wrapped
// DEK.
//
// What this package does NOT do:
//   - Fetch KEKs. Callers hand them in as []byte. The KEK
//     custody contract — Vault adapter, rotation cadence,
//     per-tenant isolation — lives outside this library.
//   - Key versioning. Wrapping always uses the supplied KEK
//     verbatim; rotation means re-wrapping with a new KEK
//     supplied by the caller (see RewrapDEK below). This
//     keeps the package's surface small — version metadata
//     lives in the caller's storage row.
//   - Streaming. v0 is `[]byte` in / `[]byte` out. Large-file
//     streaming via cipher.StreamReader is a tracked
//     follow-up.
package keystore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

const (
	// KeySize is the required length of both KEK and DEK in
	// bytes. AES-256.
	KeySize = 32

	// NonceSize is the AES-GCM nonce length. 12 bytes is the
	// standard choice — short enough to keep overhead low,
	// long enough to make random-nonce collisions vanishingly
	// rare at the scales Fyredocs operates at.
	NonceSize = 12

	// GCMTagSize is the AES-GCM authentication tag length —
	// 16 bytes (128 bits).
	GCMTagSize = 16

	// WrappedDEKSize is the exact byte length of the wrapped
	// DEK as written by WrapDEK: nonce + ciphertext + tag.
	WrappedDEKSize = NonceSize + KeySize + GCMTagSize
)

// ErrKeySize is returned when a key argument is the wrong
// length. Always indicates a programming bug at the caller —
// the package only accepts AES-256 keys.
var ErrKeySize = fmt.Errorf("keystore: KEK / DEK must be exactly %d bytes", KeySize)

// ErrAuthFailed is returned by any decrypt / unwrap operation
// whose AES-GCM tag check fails. Indicates either (a) the
// supplied KEK is wrong, (b) the supplied ciphertext is wrong,
// or (c) the bytes were tampered with in storage. Treat all
// three as the same fatal error — never log which case it
// might be, never attempt a partial decrypt.
var ErrAuthFailed = errors.New("keystore: GCM authentication failed (wrong key or tampered ciphertext)")

// ErrWrappedDEKSize is returned by UnwrapDEK when the wrapped
// blob isn't exactly WrappedDEKSize bytes — a corruption /
// truncation signal that won't be repaired by a retry.
var ErrWrappedDEKSize = fmt.Errorf("keystore: wrapped DEK must be exactly %d bytes", WrappedDEKSize)

// GenerateDEK returns a fresh 32-byte data encryption key from
// crypto/rand. Caller supplies the random source for tests via
// GenerateDEKFrom. Production paths use this convenience form.
func GenerateDEK() ([]byte, error) {
	return GenerateDEKFrom(rand.Reader)
}

// GenerateDEKFrom reads KeySize bytes from `r` and returns them
// as a DEK. Exposed so tests can inject a deterministic source.
// Callers that don't need that should use GenerateDEK.
func GenerateDEKFrom(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, errors.New("keystore: GenerateDEKFrom requires a non-nil reader")
	}
	dek := make([]byte, KeySize)
	if _, err := io.ReadFull(r, dek); err != nil {
		return nil, fmt.Errorf("keystore: read DEK from source: %w", err)
	}
	return dek, nil
}

// WrapDEK encrypts `dek` with `kek` using AES-256-GCM. Returns
// a `WrappedDEKSize`-byte blob: nonce || ciphertext || tag.
// Stores the random nonce inline so UnwrapDEK doesn't need a
// side-channel.
//
// Caller supplies the random source via the rand argument so
// tests can be deterministic. Production paths pass
// crypto/rand.Reader.
func WrapDEK(kek, dek []byte, randSrc io.Reader) ([]byte, error) {
	if len(kek) != KeySize || len(dek) != KeySize {
		return nil, ErrKeySize
	}
	if randSrc == nil {
		randSrc = rand.Reader
	}
	gcm, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(randSrc, nonce); err != nil {
		return nil, fmt.Errorf("keystore: read nonce: %w", err)
	}
	// Seal allocates and returns `nonce || ciphertext || tag`
	// shape we want when we feed nonce as the dst prefix.
	out := gcm.Seal(nonce, nonce, dek, nil)
	return out, nil
}

// UnwrapDEK reverses WrapDEK. Returns the original DEK on
// successful auth-tag check, ErrAuthFailed otherwise.
func UnwrapDEK(kek, wrapped []byte) ([]byte, error) {
	if len(kek) != KeySize {
		return nil, ErrKeySize
	}
	if len(wrapped) != WrappedDEKSize {
		return nil, ErrWrappedDEKSize
	}
	gcm, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	nonce, ciphertext := wrapped[:NonceSize], wrapped[NonceSize:]
	dek, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrAuthFailed
	}
	return dek, nil
}

// RewrapDEK is the key-rotation primitive. Unwraps `wrapped`
// with the old KEK + rewraps the same DEK with the new KEK.
// Useful when rotating tenant KEKs without re-encrypting the
// (potentially large) document body.
//
// On success returns the new wrapped DEK; on failure returns
// the underlying unwrap / wrap error. Caller writes the new
// wrapped DEK back to storage.
func RewrapDEK(oldKEK, newKEK, wrapped []byte, randSrc io.Reader) ([]byte, error) {
	dek, err := UnwrapDEK(oldKEK, wrapped)
	if err != nil {
		return nil, fmt.Errorf("keystore: unwrap with old KEK: %w", err)
	}
	out, err := WrapDEK(newKEK, dek, randSrc)
	// Zero out the DEK in memory once we don't need it. Doesn't
	// fully scrub (Go's GC may have copied it), but signals
	// intent and reduces the window.
	for i := range dek {
		dek[i] = 0
	}
	if err != nil {
		return nil, fmt.Errorf("keystore: wrap with new KEK: %w", err)
	}
	return out, nil
}

// SealWithDEK encrypts `plaintext` with `dek` using AES-256-GCM.
// Output shape mirrors WrapDEK: `nonce || ciphertext || tag`,
// where the ciphertext is the same length as the plaintext (GCM
// is a stream cipher).
//
// Use this for document bodies once you have a DEK from
// UnwrapDEK or GenerateDEK.
func SealWithDEK(dek, plaintext []byte, randSrc io.Reader) ([]byte, error) {
	if len(dek) != KeySize {
		return nil, ErrKeySize
	}
	if randSrc == nil {
		randSrc = rand.Reader
	}
	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(randSrc, nonce); err != nil {
		return nil, fmt.Errorf("keystore: read nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// OpenWithDEK reverses SealWithDEK. Auth-tag failure surfaces
// as ErrAuthFailed.
func OpenWithDEK(dek, sealed []byte) ([]byte, error) {
	if len(dek) != KeySize {
		return nil, ErrKeySize
	}
	if len(sealed) < NonceSize+GCMTagSize {
		return nil, fmt.Errorf("keystore: sealed payload too short (got %d, need at least %d)",
			len(sealed), NonceSize+GCMTagSize)
	}
	gcm, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	nonce, ciphertext := sealed[:NonceSize], sealed[NonceSize:]
	out, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrAuthFailed
	}
	return out, nil
}

// newGCM is the shared AES-256-GCM factory. Pulled out so the
// keysize check + cipher construction live in one place.
func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("keystore: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("keystore: cipher.NewGCM: %w", err)
	}
	return gcm, nil
}
