// Package apikey generates and verifies long-lived API credentials
// in the `fyr_<env>_<prefix>_<secret>` wire format described in
// internal/models/api_key.go.
//
// Why these specific shapes:
//
//   - 14-char prefix: matches GitHub's pat_ format length. Long
//     enough to be effectively unique without truncating the
//     entropy budget of the secret. We use crockford-base32 so the
//     prefix survives copy/paste in URLs and Slack without
//     character-escape surprises.
//   - 32-char secret: ~160 bits of entropy in base32. Comfortably
//     above the 128-bit floor most threat models target.
//   - Argon2id hash at rest: bcrypt's per-hash cost is too low at
//     modern defaults for API-key checking at request-path latency.
//     Argon2id is what OWASP recommends as of 2025; we use the
//     conservative "interactive" cost parameters (1 iteration, 64MB
//     memory, 4 lanes) so a single-pod auth service can verify
//     ~30 keys/second on commodity hardware — well above the
//     traffic this service sees from any one tenant.
package apikey

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// EnvLive and EnvTest are the two environments embedded in the key.
// Live keys grant production access; test keys grant access to a
// sandbox tenant slice (which doesn't yet exist as a separate
// runtime but is reserved here so callers can already segregate
// secrets in their CI / dev tooling).
const (
	EnvLive = "live"
	EnvTest = "test"
)

// PrefixLength is the public, non-secret identifier embedded in the
// wire format. Encoded in 14 chars of base32 (no padding).
const PrefixLength = 14

// SecretLength is the random secret portion. 32 base32 chars ≈ 160
// bits of entropy.
const SecretLength = 32

// Argon2id parameters. Kept as package vars rather than const so
// tests can dial them down — running argon2id with the production
// memory cost in every unit test would burn ~1s per Verify call.
var (
	argonTime    uint32 = 1
	argonMemory  uint32 = 64 * 1024 // 64 MiB
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// Generated bundles the freshly-minted key for storage + display.
//   - Plaintext is shown to the user exactly once at issuance.
//   - Prefix and Hash go to the database. The plaintext is NOT
//     persisted; rotation is the recovery path if it's lost.
type Generated struct {
	Plaintext string // full wire-format token; sensitive
	Prefix    string // public identifier
	Hash      string // argon2id-encoded hash for storage
}

// Generate mints a new API key for the given environment.
//
// errors:
//   - bad env
//   - rand.Read failure (returned verbatim; the caller's error
//     handler decides whether to retry)
func Generate(env string) (*Generated, error) {
	if env != EnvLive && env != EnvTest {
		return nil, fmt.Errorf("apikey: unknown environment %q", env)
	}

	prefixBytes := make([]byte, ceilDiv(PrefixLength*5, 8))
	if _, err := rand.Read(prefixBytes); err != nil {
		return nil, fmt.Errorf("apikey: read random prefix: %w", err)
	}
	prefix := encodeBase32(prefixBytes)[:PrefixLength]

	secretBytes := make([]byte, ceilDiv(SecretLength*5, 8))
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("apikey: read random secret: %w", err)
	}
	secret := encodeBase32(secretBytes)[:SecretLength]

	plaintext := fmt.Sprintf("fyr_%s_%s_%s", env, prefix, secret)
	hash, err := hashSecret(secret)
	if err != nil {
		return nil, fmt.Errorf("apikey: hash secret: %w", err)
	}

	return &Generated{
		Plaintext: plaintext,
		Prefix:    prefix,
		Hash:      hash,
	}, nil
}

// Parsed is the result of [Parse] — the three fields of a wire-
// format key. The caller looks up the row by Prefix, then calls
// [Verify] with the persisted hash + this Secret.
type Parsed struct {
	Env    string
	Prefix string
	Secret string
}

// ErrMalformed is returned by [Parse] for any input that doesn't
// match the `fyr_<env>_<prefix>_<secret>` shape. Callers compare
// with errors.Is so the HTTP layer can map to 401 cleanly.
var ErrMalformed = errors.New("apikey: malformed wire format")

// Parse splits a wire-format token into its three components without
// touching the database. Pure function; safe to call in hot paths
// before the DB lookup to short-circuit obviously-bad credentials.
func Parse(token string) (*Parsed, error) {
	if !strings.HasPrefix(token, "fyr_") {
		return nil, ErrMalformed
	}
	parts := strings.Split(token, "_")
	if len(parts) != 4 {
		return nil, ErrMalformed
	}
	env, prefix, secret := parts[1], parts[2], parts[3]
	if env != EnvLive && env != EnvTest {
		return nil, ErrMalformed
	}
	if len(prefix) != PrefixLength || len(secret) != SecretLength {
		return nil, ErrMalformed
	}
	return &Parsed{Env: env, Prefix: prefix, Secret: secret}, nil
}

// Verify constant-time-compares the candidate secret against the
// stored argon2id-encoded hash. Returns true on match, false on
// mismatch or any decoding error. Errors are swallowed deliberately —
// the verifier should treat "bad hash format" the same as "wrong
// secret" so an attacker can't distinguish them via response timing.
func Verify(secret, encodedHash string) bool {
	want, salt, ok := decodeHash(encodedHash)
	if !ok {
		return false
	}
	got := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// hashSecret produces an argon2id-encoded hash of the secret string
// using the package's default parameters. Format is the canonical
// "$argon2id$v=…$m=…,t=…,p=…$<salt-b64>$<hash-b64>" so future
// param tweaks can co-exist without a migration — the verifier
// reads the params from the encoded string.
func hashSecret(secret string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64Raw(salt), base64Raw(hash),
	), nil
}

// decodeHash splits the canonical argon2id encoding into (hash,
// salt, ok). We deliberately ignore the version + cost params in
// v0 because every hash we produce uses the same constants; future
// rotations land via a `migrations` table, not a backward-compat
// check at every verify.
func decodeHash(encoded string) (hash, salt []byte, ok bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return nil, nil, false
	}
	if parts[1] != "argon2id" {
		return nil, nil, false
	}
	saltDecoded, err := decodeBase64Raw(parts[4])
	if err != nil {
		return nil, nil, false
	}
	hashDecoded, err := decodeBase64Raw(parts[5])
	if err != nil {
		return nil, nil, false
	}
	return hashDecoded, saltDecoded, true
}

// ---- tiny encoding helpers (no padding, lowercase output) ------

var base32Enc = base32.StdEncoding.WithPadding(base32.NoPadding)

func encodeBase32(b []byte) string {
	return strings.ToLower(base32Enc.EncodeToString(b))
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

func base64Raw(b []byte) string {
	return strings.ReplaceAll(
		strings.ReplaceAll(
			base32Enc.EncodeToString(b),
			"=", "",
		),
		"\n", "",
	)
}

// decodeBase64Raw is a tiny wrapper that survives whichever flavour
// of base of base32 the producer used; we standardise on
// no-padding base32 in this package so the round-trip is stable.
func decodeBase64Raw(s string) ([]byte, error) {
	return base32Enc.DecodeString(s)
}
