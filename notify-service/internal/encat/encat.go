// Package encat ("encryption-at-rest") is notify-service's
// thin policy wrapper around fyredocs/shared/keystore. Used
// today to seal webhook-subscription signing secrets so the
// fanout dispatcher can recover the plaintext to HMAC-sign
// outbound payloads (storing the secret as bcrypt — which the
// initial draft did — would prevent recovery, since bcrypt is
// one-way).
//
// Same shape as editor-service's encat package; intentional
// duplication per CLAUDE.md §1 (no cross-service imports). The
// service-local copy lets each owner pick its own KEK env var
// and rotation policy without inheriting the other's.
//
// What this package does:
//   - KEK lookup (env today; auth-service secret store later
//     when per-tenant KEKs land)
//   - Pass-through when no KEK is configured (dev / staging
//     deploys work without crypto setup)
//   - Two helpers: SealSecret + OpenSecret
//
// What this package does NOT do:
//   - Per-tenant KEK resolution. v0 uses one service-wide
//     master key.
//   - Key rotation. Future work uses keystore.RewrapDEK and
//     a `kek_version` column on the row.
package encat

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"fyredocs/shared/keystore"
)

// kekEnvVar holds the hex-encoded 32-byte master KEK. Empty =
// pass-through (sealed bytes equal plaintext; wrappedDEK is
// nil). The pass-through path keeps the dev experience simple
// — operators don't need a KEK to spin notify-service up
// locally.
const kekEnvVar = "NOTIFY_SECRET_KEK_HEX"

var (
	kekMu       sync.RWMutex
	kekOverride []byte // test injection; nil = env-backed lookup
)

// SetKEKForTest installs a 32-byte KEK as the active master
// key for the rest of the process (or `nil` to restore
// env-based lookup). Production code MUST NOT call this.
func SetKEKForTest(kek []byte) {
	kekMu.Lock()
	defer kekMu.Unlock()
	kekOverride = kek
}

// Enabled reports whether encryption-at-rest is active. Used
// for observability (readyz / metrics surface whether the
// feature is on).
func Enabled() bool {
	_, on, err := resolveKEK()
	return on && err == nil
}

// SealSecret encrypts `plain` with a fresh DEK, then wraps
// the DEK with the master KEK. Returns:
//
//	wrappedDEK — exactly keystore.WrappedDEKSize bytes; goes
//	             in the row's wrapped_dek column
//	sealed     — the bytes to store in place of the plaintext
//
// When encryption is disabled (no KEK configured) returns
// `(nil, plain, nil)` so the caller's persist path stays
// identical — `wrappedDEK == nil` signals "store plaintext"
// and the row records that on the column.
func SealSecret(plain []byte) (wrappedDEK, sealed []byte, err error) {
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
		return nil, nil, fmt.Errorf("encat: seal: %w", err)
	}
	return wrapped, sealedBytes, nil
}

// OpenSecret reverses SealSecret. A nil/empty wrappedDEK
// returns sealed verbatim (legacy/pass-through rows). A
// sealed row with no KEK currently configured surfaces
// ErrEncryptionDisabled — operational misconfiguration, not
// a generic auth failure.
func OpenSecret(wrappedDEK, sealed []byte) ([]byte, error) {
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

// ErrEncryptionDisabled is returned when a row carries a
// wrapped DEK (so it was sealed at write time) but the service
// is currently running without a KEK. Operator missed setting
// the env back after a rollback or restart.
var ErrEncryptionDisabled = errors.New("encat: subscription secret is sealed but " + kekEnvVar + " is unset")

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
