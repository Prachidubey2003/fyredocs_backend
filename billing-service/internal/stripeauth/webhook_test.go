package stripeauth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// validHeader returns a freshly-signed Stripe-Signature header
// for `(body, secret, timestamp)`. Used by every happy-path
// test so they share one builder.
func validHeader(secret string, ts int64, body []byte) string {
	return fmt.Sprintf("t=%d,v1=%s", ts, ComputeHMAC(secret, ts, body))
}

// ---- Happy path -------------------------------------------------------

func TestVerify_AcceptsFreshlySignedHeader(t *testing.T) {
	const secret = "whsec_test_123456"
	body := []byte(`{"id":"evt_123","type":"charge.succeeded"}`)
	now := time.Unix(1_700_000_100, 0)

	header := validHeader(secret, now.Unix(), body)
	if err := Verify(header, body, secret, now, 0); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestVerify_DefaultToleranceUsedWhenZero(t *testing.T) {
	const secret = "whsec_test"
	body := []byte("ok")
	now := time.Unix(1_700_000_000, 0)

	// Signed 4 minutes ago — inside the 5-minute default.
	signed := now.Add(-4 * time.Minute)
	header := validHeader(secret, signed.Unix(), body)
	if err := Verify(header, body, secret, now, 0); err != nil {
		t.Errorf("4-minute-old signature inside default tolerance should pass: %v", err)
	}
}

func TestVerify_AcceptsMultipleV1DuringRotation(t *testing.T) {
	// Stripe sends both old + new signatures during a key
	// rotation window. Either one matching should accept.
	const oldSecret = "whsec_old"
	const newSecret = "whsec_new"
	body := []byte(`{"id":"evt_456"}`)
	now := time.Unix(1_700_000_200, 0)
	ts := now.Unix()

	header := fmt.Sprintf("t=%d,v1=%s,v1=%s",
		ts, ComputeHMAC(oldSecret, ts, body), ComputeHMAC(newSecret, ts, body))

	if err := Verify(header, body, oldSecret, now, 0); err != nil {
		t.Errorf("old secret should match: %v", err)
	}
	if err := Verify(header, body, newSecret, now, 0); err != nil {
		t.Errorf("new secret should match: %v", err)
	}
}

func TestVerify_IgnoresUnknownAndLegacyV0Entries(t *testing.T) {
	const secret = "whsec_test"
	body := []byte("ok")
	now := time.Unix(1_700_000_300, 0)
	ts := now.Unix()
	valid := ComputeHMAC(secret, ts, body)

	// Real Stripe header may carry v0=, unknown keys, and
	// stray whitespace. All should be tolerated as long as
	// at least one v1= matches.
	header := fmt.Sprintf(" t = %d , v0=legacy-sha1 , unknown=ignored , v1=%s , extra= ", ts, valid)
	if err := Verify(header, body, secret, now, 0); err != nil {
		t.Errorf("Verify should tolerate v0 + unknown keys + whitespace: %v", err)
	}
}

// ---- Header parsing failures ------------------------------------------

func TestVerify_MissingHeaderRejected(t *testing.T) {
	if err := Verify("", []byte("body"), "whsec", time.Now(), 0); !errors.Is(err, ErrSignatureMissing) {
		t.Errorf("err = %v, want ErrSignatureMissing", err)
	}
	if err := Verify("   ", []byte("body"), "whsec", time.Now(), 0); !errors.Is(err, ErrSignatureMissing) {
		t.Errorf("whitespace-only header: err = %v, want ErrSignatureMissing", err)
	}
}

func TestVerify_MissingTimestampRejected(t *testing.T) {
	header := "v1=" + ComputeHMAC("whsec", 1_700_000_000, []byte("x"))
	err := Verify(header, []byte("x"), "whsec", time.Unix(1_700_000_000, 0), 0)
	if !errors.Is(err, ErrSignatureMalformed) {
		t.Errorf("err = %v, want ErrSignatureMalformed", err)
	}
}

func TestVerify_MissingV1Rejected(t *testing.T) {
	header := "t=1700000000"
	err := Verify(header, []byte("x"), "whsec", time.Unix(1_700_000_000, 0), 0)
	if !errors.Is(err, ErrSignatureMalformed) {
		t.Errorf("err = %v, want ErrSignatureMalformed", err)
	}
}

func TestVerify_NonNumericTimestampRejected(t *testing.T) {
	header := "t=not-a-number,v1=abc"
	err := Verify(header, []byte("x"), "whsec", time.Now(), 0)
	if !errors.Is(err, ErrSignatureMalformed) {
		t.Errorf("err = %v, want ErrSignatureMalformed", err)
	}
}

func TestVerify_ZeroOrNegativeTimestampRejected(t *testing.T) {
	for _, ts := range []string{"0", "-1", "-1700000000"} {
		header := "t=" + ts + ",v1=ff"
		if err := Verify(header, []byte("x"), "whsec", time.Now(), 0); !errors.Is(err, ErrSignatureMalformed) {
			t.Errorf("t=%s: err = %v, want ErrSignatureMalformed", ts, err)
		}
	}
}

// ---- Signature mismatch ----------------------------------------------

func TestVerify_WrongSecretRejected(t *testing.T) {
	body := []byte("payload")
	now := time.Unix(1_700_000_400, 0)
	header := validHeader("whsec_right", now.Unix(), body)

	err := Verify(header, body, "whsec_wrong", now, 0)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("err = %v, want ErrSignatureMismatch", err)
	}
}

func TestVerify_TamperedBodyRejected(t *testing.T) {
	const secret = "whsec_test"
	body := []byte(`{"amount":100}`)
	tampered := []byte(`{"amount":999}`)
	now := time.Unix(1_700_000_500, 0)
	header := validHeader(secret, now.Unix(), body)

	err := Verify(header, tampered, secret, now, 0)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("err = %v, want ErrSignatureMismatch", err)
	}
}

func TestVerify_BadHexInV1IsSkippedNotPanic(t *testing.T) {
	// A v1= with non-hex chars (Stripe rarely does this but
	// be defensive) must be skipped, not crash. The remaining
	// v1= entries get tried.
	const secret = "whsec_test"
	body := []byte("x")
	now := time.Unix(1_700_000_600, 0)
	ts := now.Unix()
	good := ComputeHMAC(secret, ts, body)
	header := fmt.Sprintf("t=%d,v1=zzz-not-hex,v1=%s", ts, good)

	if err := Verify(header, body, secret, now, 0); err != nil {
		t.Errorf("non-hex v1= should be skipped, valid one should match: %v", err)
	}
}

func TestVerify_AllV1NonHexFails(t *testing.T) {
	// If EVERY v1= is malformed, we should land at
	// ErrSignatureMismatch (not panic).
	now := time.Unix(1_700_000_700, 0)
	header := fmt.Sprintf("t=%d,v1=zzz,v1=---", now.Unix())
	err := Verify(header, []byte("x"), "whsec", now, 0)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("err = %v, want ErrSignatureMismatch", err)
	}
}

// ---- Tolerance window -------------------------------------------------

func TestVerify_TooOldRejected(t *testing.T) {
	const secret = "whsec_test"
	body := []byte("x")
	now := time.Unix(1_700_000_800, 0)
	signed := now.Add(-6 * time.Minute)
	header := validHeader(secret, signed.Unix(), body)

	err := Verify(header, body, secret, now, 0)
	if !errors.Is(err, ErrTimestampTooOld) {
		t.Errorf("err = %v, want ErrTimestampTooOld (6min > 5min default)", err)
	}
}

func TestVerify_TooFarInFutureRejected(t *testing.T) {
	// Clock-skew + replay protection both want symmetric
	// window rejection.
	const secret = "whsec_test"
	body := []byte("x")
	now := time.Unix(1_700_000_900, 0)
	signed := now.Add(10 * time.Minute)
	header := validHeader(secret, signed.Unix(), body)

	err := Verify(header, body, secret, now, 0)
	if !errors.Is(err, ErrTimestampTooOld) {
		t.Errorf("err = %v, want ErrTimestampTooOld for future timestamp", err)
	}
}

func TestVerify_CustomTolerance(t *testing.T) {
	// 1-minute tolerance — a signature 90 seconds old gets
	// rejected even though the default would accept it.
	const secret = "whsec_test"
	body := []byte("x")
	now := time.Unix(1_700_001_000, 0)
	signed := now.Add(-90 * time.Second)
	header := validHeader(secret, signed.Unix(), body)

	if err := Verify(header, body, secret, now, time.Minute); !errors.Is(err, ErrTimestampTooOld) {
		t.Errorf("err = %v, want ErrTimestampTooOld (90s > 60s tolerance)", err)
	}
	// And the same signature passes when given a generous
	// 5-minute window.
	if err := Verify(header, body, secret, now, 5*time.Minute); err != nil {
		t.Errorf("Verify with 5min tolerance: %v", err)
	}
}

func TestVerify_ToleranceMessageIncludesContext(t *testing.T) {
	// Operators reading log lines about replay rejections
	// want both the signed-at time and the now-time without
	// digging through other fields. Pin the substring.
	now := time.Unix(1_700_001_100, 0)
	header := validHeader("whsec", now.Add(-10*time.Minute).Unix(), []byte("x"))
	err := Verify(header, []byte("x"), "whsec", now, 0)
	if err == nil || !strings.Contains(err.Error(), "tolerance=") {
		t.Errorf("err = %v, should mention tolerance window", err)
	}
}

// ---- Secret guard ----------------------------------------------------

func TestVerify_EmptySecretRejected(t *testing.T) {
	now := time.Unix(1_700_001_200, 0)
	header := validHeader("anything", now.Unix(), []byte("x"))
	for _, s := range []string{"", "  "} {
		if err := Verify(header, []byte("x"), s, now, 0); !errors.Is(err, ErrEmptySecret) {
			t.Errorf("secret=%q: err = %v, want ErrEmptySecret", s, err)
		}
	}
}

// ---- ComputeHMAC determinism -----------------------------------------

func TestComputeHMAC_DeterministicForSameInputs(t *testing.T) {
	a := ComputeHMAC("whsec", 1_700_000_000, []byte("body"))
	b := ComputeHMAC("whsec", 1_700_000_000, []byte("body"))
	if a != b {
		t.Errorf("ComputeHMAC non-deterministic: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("ComputeHMAC output length = %d, want 64 (SHA-256 hex)", len(a))
	}
}

func TestComputeHMAC_BodyAndTimestampBothContributeToOutput(t *testing.T) {
	// Sanity: change either input and the output must change.
	// Guards against an "oops, we only signed the body"
	// regression that would let an attacker replay forever.
	base := ComputeHMAC("whsec", 1_700_000_000, []byte("body"))
	if changed := ComputeHMAC("whsec", 1_700_000_001, []byte("body")); changed == base {
		t.Error("timestamp change did not affect HMAC output — body-only signing regression?")
	}
	if changed := ComputeHMAC("whsec", 1_700_000_000, []byte("BODY")); changed == base {
		t.Error("body change did not affect HMAC output")
	}
	if changed := ComputeHMAC("whsec-2", 1_700_000_000, []byte("body")); changed == base {
		t.Error("secret change did not affect HMAC output")
	}
}
