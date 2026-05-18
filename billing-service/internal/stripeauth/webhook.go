// Package stripeauth is the Stripe-webhook signature verifier
// for billing-service. Verify is the single entry point — every
// Stripe-receiving handler (subscription updates, payment
// success/failure, marketplace charge.succeeded, customer
// portal events) MUST call it before trusting the payload.
//
// The verification follows Stripe's published scheme exactly
// (https://stripe.com/docs/webhooks/signatures):
//
//   1. The `Stripe-Signature` header is a comma-separated list
//      of key=value pairs. `t=<unix-seconds>` is the timestamp
//      Stripe signed with; `v1=<hex>` are the HMAC-SHA256
//      signatures (multiple `v1=` entries appear during a
//      secret rotation, when Stripe signs with both the old
//      and new keys for a window). `v0=` is legacy and is
//      explicitly ignored.
//
//   2. The signed payload is `<timestamp>.<rawBody>` — caller
//      supplies the verbatim request body (must NOT be
//      re-marshalled or normalised).
//
//   3. The HMAC is constant-time compared against every `v1=`
//      candidate. ANY match accepts; none match rejects.
//
//   4. The timestamp is checked against now() with a tolerance
//      window (default 5 minutes per Stripe's own
//      recommendation) — prevents replay of intercepted
//      payloads outside the window.
//
// The library is pure: caller supplies the current time + the
// raw body + the secret. No HTTP, no clock dependency. That
// keeps every Stripe-handler test trivially deterministic.
package stripeauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultTolerance is Stripe's recommended replay window. A
// signature with t= older than this (or further in the future
// than this) is rejected.
const DefaultTolerance = 5 * time.Minute

// ErrSignatureMissing — the request has no Stripe-Signature
// header. Caller responds 400.
var ErrSignatureMissing = errors.New("stripeauth: Stripe-Signature header is missing")

// ErrSignatureMalformed — the header is present but doesn't
// contain the required t= and v1= pairs. Caller responds 400.
var ErrSignatureMalformed = errors.New("stripeauth: Stripe-Signature header is malformed")

// ErrTimestampTooOld — the t= value sits outside the tolerance
// window. Caller responds 400. We use the same error for "too
// old" and "too far in the future" because both indicate a
// replay or clock-skew situation the handler must treat the
// same way (refuse + log).
var ErrTimestampTooOld = errors.New("stripeauth: signature timestamp outside tolerance window")

// ErrSignatureMismatch — every v1= candidate failed
// constant-time comparison against the computed HMAC. Caller
// responds 400 (NOT 401 — Stripe's docs are explicit that
// signature failures are body-related, not auth-related).
var ErrSignatureMismatch = errors.New("stripeauth: no v1 signature matched the computed HMAC")

// ErrEmptySecret — the supplied secret was empty. Programming
// bug at the caller; fail loud so dev environments don't
// silently accept every Stripe-shaped payload.
var ErrEmptySecret = errors.New("stripeauth: webhook signing secret is empty")

// Verify checks `header` against `body` using the supplied
// `secret` and `now`. Returns nil iff: at least one v1=
// signature matches the HMAC-SHA256 of "<t>.<body>", AND the
// t= value is within `tolerance` of `now` (zero tolerance
// falls back to DefaultTolerance).
//
// Caller plugs in time.Now() in production and a fixed time
// in tests — keeps the library deterministic.
//
// Production usage in a Gin handler:
//
//	body, _ := io.ReadAll(c.Request.Body)
//	if err := stripeauth.Verify(
//	    c.GetHeader("Stripe-Signature"), body,
//	    os.Getenv("STRIPE_WEBHOOK_SECRET"),
//	    time.Now(), 0,
//	); err != nil {
//	    response.BadRequest(c, "BAD_SIGNATURE", err.Error())
//	    return
//	}
func Verify(header string, body []byte, secret string, now time.Time, tolerance time.Duration) error {
	if strings.TrimSpace(secret) == "" {
		return ErrEmptySecret
	}
	if strings.TrimSpace(header) == "" {
		return ErrSignatureMissing
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}

	timestamp, v1Sigs, err := parseHeader(header)
	if err != nil {
		return err
	}

	// 1. Timestamp tolerance check (bidirectional — too old
	// AND too far in the future are both replay risks).
	signedAt := time.Unix(timestamp, 0)
	delta := now.Sub(signedAt)
	if delta < 0 {
		delta = -delta
	}
	if delta > tolerance {
		return fmt.Errorf("%w: signed=%s now=%s tolerance=%s",
			ErrTimestampTooOld, signedAt.UTC(), now.UTC(), tolerance)
	}

	// 2. Compute the expected HMAC over `<t>.<body>`.
	expected := computeHMAC([]byte(secret), timestamp, body)

	// 3. Constant-time compare against every v1= candidate.
	// hmac.Equal is constant-time over the byte slices.
	for _, sig := range v1Sigs {
		raw, err := hex.DecodeString(sig)
		if err != nil {
			// Malformed v1= value — skip it but keep
			// checking the rest. Stripe occasionally
			// publishes whitespace in headers under unusual
			// load.
			continue
		}
		if hmac.Equal(expected, raw) {
			return nil
		}
	}
	return ErrSignatureMismatch
}

// computeHMAC produces the canonical Stripe signature for one
// `<t>.<body>` pair. Exported as ComputeHMAC for unit tests
// that want to construct valid headers without re-implementing
// the scheme.
func computeHMAC(secret []byte, timestamp int64, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	// Write in three parts so we don't have to allocate a
	// fully-built buffer. mac.Write doesn't return errors per
	// the hash.Hash contract; we deliberately don't check.
	_, _ = mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	_, _ = mac.Write([]byte{'.'})
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

// ComputeHMAC is the test-friendly wrapper that hex-encodes
// the result — matches the shape Stripe puts in v1=.
func ComputeHMAC(secret string, timestamp int64, body []byte) string {
	return hex.EncodeToString(computeHMAC([]byte(secret), timestamp, body))
}

// parseHeader splits a Stripe-Signature header into its
// timestamp + v1 signatures. Order-independent within the
// header; whitespace around `=` is tolerated; v0= entries are
// dropped (legacy SHA-1 scheme — Stripe explicitly says don't
// use them); unknown keys are dropped silently.
//
// Returns ErrSignatureMalformed if the header has no t= or
// no v1= pairs.
func parseHeader(header string) (int64, []string, error) {
	var (
		timestamp int64 = -1
		v1Sigs    []string
	)
	for _, pair := range strings.Split(header, ",") {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		val := strings.TrimSpace(pair[eq+1:])
		switch key {
		case "t":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n <= 0 {
				return 0, nil, fmt.Errorf("%w: invalid t= value %q", ErrSignatureMalformed, val)
			}
			timestamp = n
		case "v1":
			if val != "" {
				v1Sigs = append(v1Sigs, val)
			}
		}
	}
	if timestamp < 0 {
		return 0, nil, fmt.Errorf("%w: missing t= timestamp", ErrSignatureMalformed)
	}
	if len(v1Sigs) == 0 {
		return 0, nil, fmt.Errorf("%w: missing v1= signature", ErrSignatureMalformed)
	}
	return timestamp, v1Sigs, nil
}
