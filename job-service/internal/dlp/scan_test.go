package dlp

import (
	"strings"
	"testing"
)

// hasCategory returns true iff `findings` contains at least one
// Finding whose Match equals `want` and Category equals `cat`.
// Used in positive-case assertions so a test reads as "I expect
// this exact substring to be flagged as this category".
func hasCategory(t *testing.T, findings []Finding, cat Category, want string) {
	t.Helper()
	for _, f := range findings {
		if f.Category == cat && f.Match == want {
			return
		}
	}
	t.Errorf("expected %s finding %q in %+v", cat, want, findings)
}

// firstOf returns the first Finding for the given category,
// nil if none. Used for asserting on derived fields like
// Detail (card brand) or offsets.
func firstOf(findings []Finding, cat Category) *Finding {
	for i := range findings {
		if findings[i].Category == cat {
			return &findings[i]
		}
	}
	return nil
}

func TestScan_Empty(t *testing.T) {
	if got := Scan(""); got != nil {
		t.Errorf("Scan(\"\") = %+v, want nil", got)
	}
}

func TestScan_SSN_Positive(t *testing.T) {
	got := Scan("DOB: 1980-01-01\nSSN: 123-45-6789\nName: Alice")
	hasCategory(t, got, CategorySSN, "123-45-6789")
}

func TestScan_SSN_NotMatchedInsideLongerDigitRun(t *testing.T) {
	// `123-45-67891` would only match if the regex were
	// missing word-boundaries; verify it doesn't.
	if got := Scan("ref# 123-45-67891"); len(got) != 0 {
		t.Errorf("Scan = %+v, want no SSN match in a longer digit run", got)
	}
}

func TestScan_CreditCard_LuhnValidIsCaught(t *testing.T) {
	// 4111 1111 1111 1111 is the classic Visa test card —
	// passes Luhn, valid Visa prefix.
	got := Scan("Card: 4111 1111 1111 1111 exp 12/26")
	f := firstOf(got, CategoryCreditCard)
	if f == nil {
		t.Fatalf("expected credit-card finding; got %+v", got)
	}
	if f.Detail != "visa" {
		t.Errorf("Detail = %q, want \"visa\"", f.Detail)
	}
	if !strings.Contains(f.Match, "4111") {
		t.Errorf("Match = %q, should include 4111", f.Match)
	}
}

func TestScan_CreditCard_LuhnInvalidIsRejected(t *testing.T) {
	// Same digits, one swapped — Luhn-invalid. The regex
	// matches; the Luhn filter drops it.
	got := Scan("4111 1111 1111 1112")
	if firstOf(got, CategoryCreditCard) != nil {
		t.Errorf("Luhn-invalid digit run should not be a credit-card finding: %+v", got)
	}
}

func TestScan_CreditCard_DetectsAmex(t *testing.T) {
	// 378282246310005 — Amex test card (Luhn-valid, 15
	// digits, prefix 37).
	got := Scan("Charge $99 to 378282246310005")
	f := firstOf(got, CategoryCreditCard)
	if f == nil || f.Detail != "amex" {
		t.Errorf("expected amex detail; got %+v", got)
	}
}

func TestScan_CreditCard_TooShortNotMatched(t *testing.T) {
	// 12 digits is below the credit-card minimum (13).
	if got := Scan("Order# 1234 5678 9012"); firstOf(got, CategoryCreditCard) != nil {
		t.Errorf("12-digit run should not match credit-card: %+v", got)
	}
}

func TestScan_AWSAccessKey_PositiveAndPrefixes(t *testing.T) {
	got := Scan(
		"Key: AKIAIOSFODNN7EXAMPLE\nTemp: ASIA1234567890ABCDEF",
	)
	hasCategory(t, got, CategoryAWSAccessKey, "AKIAIOSFODNN7EXAMPLE")
	hasCategory(t, got, CategoryAWSAccessKey, "ASIA1234567890ABCDEF")
}

func TestScan_AWSAccessKey_RejectsLowercase(t *testing.T) {
	// AWS access keys are upper-case only. A lowercase string
	// starting with `akia` is not an access key.
	if got := Scan("akiaiosfodnn7example"); firstOf(got, CategoryAWSAccessKey) != nil {
		t.Errorf("lowercase akia prefix should not match: %+v", got)
	}
}

func TestScan_GitHubToken_AllPublicPrefixes(t *testing.T) {
	// All five GitHub public token prefixes — fine-grained,
	// classic, OAuth, user, refresh.
	cases := []string{
		"ghp_" + strings.Repeat("a", 36),
		"ghs_" + strings.Repeat("b", 36),
		"gho_" + strings.Repeat("c", 36),
		"ghu_" + strings.Repeat("d", 36),
		"ghr_" + strings.Repeat("e", 36),
	}
	for _, tok := range cases {
		got := Scan("token=" + tok)
		hasCategory(t, got, CategoryGitHubToken, tok)
	}
}

func TestScan_GitHubToken_RejectsShortBody(t *testing.T) {
	// 35 chars — below GitHub's documented 36-char floor.
	short := "ghp_" + strings.Repeat("a", 35)
	if got := Scan(short); firstOf(got, CategoryGitHubToken) != nil {
		t.Errorf("35-char body should not match GitHub token: %+v", got)
	}
}

func TestScan_JWT_Positive(t *testing.T) {
	// Minimal well-shaped JWT: header.payload.signature, each
	// segment starting with eyJ (the base64 of `{"`).
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature_part_here"
	got := Scan("Authorization: Bearer " + jwt)
	hasCategory(t, got, CategoryJWT, jwt)
}

func TestScan_JWT_RejectsPlainBase64(t *testing.T) {
	// Three dots' worth of garbage shouldn't match — the
	// header anchor (`eyJ`) is the discriminator.
	if got := Scan("aaaa.bbbb.cccc"); firstOf(got, CategoryJWT) != nil {
		t.Errorf("non-JWT-shaped string should not match: %+v", got)
	}
}

func TestScan_Email_Positive(t *testing.T) {
	got := Scan("Send to alice@example.com and bob.tester+filter@sub.example.co.uk")
	hasCategory(t, got, CategoryEmail, "alice@example.com")
	hasCategory(t, got, CategoryEmail, "bob.tester+filter@sub.example.co.uk")
}

func TestScan_Email_RejectsSingleDomainSegment(t *testing.T) {
	// `user@localhost` has no TLD-shaped suffix; intentionally
	// not matched (rare in real PII contexts and would
	// false-positive on internal config).
	if got := Scan("dev: user@localhost"); firstOf(got, CategoryEmail) != nil {
		t.Errorf("single-segment domain should not match email: %+v", got)
	}
}

func TestScan_OffsetsArePreserved(t *testing.T) {
	text := "the SSN 123-45-6789 is sensitive"
	got := Scan(text)
	if len(got) != 1 {
		t.Fatalf("findings = %+v, want one", got)
	}
	if got[0].Start != 8 || got[0].End != 19 {
		t.Errorf("Start/End = %d/%d, want 8/19", got[0].Start, got[0].End)
	}
	if text[got[0].Start:got[0].End] != got[0].Match {
		t.Errorf("offsets don't reconstruct Match: %q vs %q",
			text[got[0].Start:got[0].End], got[0].Match)
	}
}

func TestScan_SortedByStartOffset(t *testing.T) {
	text := "leak: AKIAIOSFODNN7EXAMPLE; then 4111 1111 1111 1111; later alice@x.com"
	got := Scan(text)
	if len(got) < 3 {
		t.Fatalf("expected >=3 findings; got %+v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Start > got[i].Start {
			t.Errorf("findings out of order at %d/%d: %+v", i-1, i, got)
		}
	}
}

func TestScan_MultipleCategoriesInOneText(t *testing.T) {
	text := strings.Join([]string{
		"To: ops@example.com",
		"SSN: 987-65-4320",
		"Card: 5500 0000 0000 0004", // mastercard test PAN
		"AWS:  AKIAIOSFODNN7EXAMPLE",
		"GH:   ghp_" + strings.Repeat("z", 36),
	}, "\n")
	got := Scan(text)
	// Should see one of each category.
	want := []Category{
		CategoryEmail,
		CategorySSN,
		CategoryCreditCard,
		CategoryAWSAccessKey,
		CategoryGitHubToken,
	}
	for _, c := range want {
		if firstOf(got, c) == nil {
			t.Errorf("missing %s in %+v", c, got)
		}
	}
}

func TestScan_NegativeCleanProse(t *testing.T) {
	// Real-prose smoke — must not flag normal English.
	text := "Hello world. Please review the attached document " +
		"and let me know if you have feedback. Thanks!"
	if got := Scan(text); len(got) != 0 {
		t.Errorf("clean prose flagged: %+v", got)
	}
}

func TestLuhnValid_KnownAnswers(t *testing.T) {
	cases := []struct {
		digits string
		want   bool
	}{
		{"4111111111111111", true},  // visa test
		{"4111111111111112", false}, // last digit off
		{"378282246310005", true},   // amex test
		{"5555555555554444", true},  // mastercard test
		{"6011111111111117", true},  // discover test
		{"12345678901234", false},   // 14 chars, fails luhn
		{"42", false},               // too short
		{"abcd1234567890123", false}, // non-digit
	}
	for _, c := range cases {
		if got := luhnValid(c.digits); got != c.want {
			t.Errorf("luhnValid(%q) = %v, want %v", c.digits, got, c.want)
		}
	}
}

func TestStripSeparators(t *testing.T) {
	cases := []struct{ in, want string }{
		{"4111 1111 1111 1111", "4111111111111111"},
		{"4111-1111-1111-1111", "4111111111111111"},
		{"4111-1111 1111-1111", "4111111111111111"},
		{"plain", "plain"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripSeparators(c.in); got != c.want {
			t.Errorf("stripSeparators(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBrand_RecognisedNetworks(t *testing.T) {
	cases := map[string]string{
		"4111111111111111": "visa",
		"5555555555554444": "mastercard",
		"378282246310005":  "amex",
		"6011111111111117": "discover",
		"3530111333300000": "jcb",
		"1234":             "", // unknown
		"":                 "",
	}
	for digits, want := range cases {
		if got := brand(digits); got != want {
			t.Errorf("brand(%q) = %q, want %q", digits, got, want)
		}
	}
}
