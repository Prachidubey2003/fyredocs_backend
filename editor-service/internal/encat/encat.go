// Package encat ("encryption-at-rest") is the editor-service's
// thin policy wrapper around fyredocs/shared/keystore. It owns:
//
//   - KEK lookup (env var today; auth-service secret store later
//     when that lands, per plan §4.4.6)
//   - The on/off gate so dev / staging deploys without a KEK
//     keep working as plain-text writers
//   - The single helpers handlers call: SealSnapshot,
//     OpenSnapshot.
//
// Why a per-service wrapper rather than calling shared/keystore
// directly:
//   - Handlers shouldn't carry KEK plumbing — every call site
//     would otherwise need the same "load env, decode hex, length
//     check" preamble.
//   - The pass-through path (no KEK configured) is a service-level
//     policy, not a library decision — shared/keystore is pure
//     crypto and refuses to operate without a key.
//   - Tests can swap the KEK source via SetKEKForTest without
//     touching env vars across the whole binary.
//
// What this package does NOT do:
//   - Multi-tenant KEK selection. v0 uses a single service-wide
//     master KEK. Per-tenant KEKs (plan §4.4.6) graduate to a
//     KEK-resolution callback when auth-service exposes one.
//   - Key rotation. Re-wrapping snapshots with a new KEK is a
//     follow-up using keystore.RewrapDEK; the column already
//     exists on Revision.
package encat

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	"fyredocs/shared/keystore"
)

// kekEnvVar is the environment variable holding the hex-encoded
// 32-byte master KEK. Unset / empty = encryption-at-rest is OFF
// (handlers fall back to writing plaintext).
const kekEnvVar = "EDITOR_SNAPSHOT_KEK_HEX"

var (
	kekMu       sync.RWMutex
	kekOverride []byte // test injection; nil = use env
)

// SetKEKForTest installs `kek` as the active master KEK for the
// rest of the process (or `nil` to restore env-based lookup).
// Exists for tests; production code MUST NOT call this.
func SetKEKForTest(kek []byte) {
	kekMu.Lock()
	defer kekMu.Unlock()
	kekOverride = kek
}

// resolveKEK returns the active master KEK + true if encryption
// is enabled. Returns `(nil, false, nil)` when no KEK is
// configured (plaintext mode). Returns an error only when a KEK
// IS configured but is malformed (wrong length / non-hex) —
// that's a config bug, fail loud.
func resolveKEK() ([]byte, bool, error) {
	kekMu.RLock()
	override := kekOverride
	kekMu.RUnlock()
	if override != nil {
		if len(override) != keystore.KeySize {
			return nil, false, fmt.Errorf("encat: test KEK is %d bytes, want %d", len(override), keystore.KeySize)
		}
		return override, true, nil
	}
	raw := strings.TrimSpace(os.Getenv(kekEnvVar))
	if raw == "" {
		return nil, false, nil
	}
	kek, err := hex.DecodeString(raw)
	if err != nil {
		return nil, false, fmt.Errorf("encat: %s must be hex-encoded: %w", kekEnvVar, err)
	}
	if len(kek) != keystore.KeySize {
		return nil, false, fmt.Errorf("encat: %s decodes to %d bytes, want %d", kekEnvVar, len(kek), keystore.KeySize)
	}
	return kek, true, nil
}

// Enabled reports whether encryption-at-rest is configured.
// Used by handlers to decide whether to skip the wrap/seal path
// AND for observability (so the readyz / metrics surface can
// tell operators the feature is on).
func Enabled() bool {
	_, on, err := resolveKEK()
	return on && err == nil
}

// SealSnapshot generates a fresh DEK, wraps it with the master
// KEK, and seals `plain` with the DEK. Returns:
//
//	wrappedDEK — exactly keystore.WrappedDEKSize bytes; goes in
//	             Revision.WrappedDEK
//	sealed     — the bytes to write to disk in place of `plain`
//
// When encryption is disabled the function returns
// `(nil, plain, nil)` so the caller's write-to-disk loop stays
// identical — wrappedDEK == nil signals "store plaintext" and
// the handler skips the DB column.
func SealSnapshot(plain []byte) (wrappedDEK, sealed []byte, err error) {
	kek, on, err := resolveKEK()
	if err != nil {
		return nil, nil, err
	}
	if !on {
		return nil, plain, nil
	}
	dek, err := keystore.GenerateDEK()
	if err != nil {
		return nil, nil, fmt.Errorf("encat: generate DEK: %w", err)
	}
	wrapped, err := keystore.WrapDEK(kek, dek, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("encat: wrap DEK: %w", err)
	}
	sealedBytes, err := keystore.SealWithDEK(dek, plain, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("encat: seal snapshot: %w", err)
	}
	return wrapped, sealedBytes, nil
}

// OpenSnapshot is the inverse of SealSnapshot. When wrappedDEK
// is nil/empty the bytes are assumed plaintext and returned
// verbatim — preserves read access to pre-keystore snapshots.
//
// When wrappedDEK is present the caller MUST have encryption
// enabled (KEK configured). A mismatch returns
// ErrEncryptionDisabled so the operator gets a clear "you turned
// off the KEK but old rows still need it" signal rather than
// a generic auth failure.
func OpenSnapshot(wrappedDEK, sealed []byte) ([]byte, error) {
	if len(wrappedDEK) == 0 {
		return sealed, nil
	}
	kek, on, err := resolveKEK()
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, ErrEncryptionDisabled
	}
	dek, err := keystore.UnwrapDEK(kek, wrappedDEK)
	if err != nil {
		return nil, err
	}
	return keystore.OpenWithDEK(dek, sealed)
}

// ErrEncryptionDisabled is returned by OpenSnapshot when the row
// has a wrapped DEK (so it was sealed at write time) but the
// service is running without a KEK configured. Surfaces an
// operational misconfiguration cleanly.
var ErrEncryptionDisabled = fmt.Errorf("encat: snapshot is sealed but %s is unset", kekEnvVar)
