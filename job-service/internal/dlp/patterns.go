// Package dlp is the Data-Loss-Prevention scan library for
// job-service. It detects common categories of sensitive data
// (PII, payment cards, credentials, secrets) inside the text
// content of uploaded documents, returning structured findings
// for the upload handler to act on.
//
// Per plan §4.3.2, DLP starts as a library inside job-service
// rather than its own service — the surface is small enough
// that splitting is YAGNI until an enterprise tenant ships an
// ML classifier > 1GB. The library exposes:
//
//   - [Category]: an enum of finding categories.
//   - [Finding]: one match (category, exact substring, byte
//     offsets into the source text).
//   - [Scan]: run every pattern over a string and return
//     findings sorted by Start offset.
//
// What the library does NOT do (tracked follow-ups):
//   - Extract text from PDFs / DOCX / images. Callers feed the
//     library a string; pulling text from binary uploads is the
//     upload handler's job (Tika + OCR pipeline).
//   - Block uploads. The library is a pure detector — wire-up
//     into the upload reject path lands when the enterprise
//     tier ships an `org.dlp_policy` flag to gate it on.
//   - Heuristic / ML classification. v0 is regex-only (cheap,
//     fast, debuggable). ML classification graduates DLP into
//     its own service per §4.3.3.
package dlp

import "regexp"

// Category enumerates the kinds of sensitive data the scanner
// recognises. Use the constants below rather than raw strings
// so the compiler catches typos at call sites.
type Category string

const (
	// CategorySSN — US Social Security Numbers in XXX-XX-XXXX
	// format. We don't try to detect concatenated 9-digit
	// strings: the false-positive rate is too high (zip+5,
	// phone, etc.) without context. Document the limitation.
	CategorySSN Category = "ssn"

	// CategoryCreditCard — 13–19 digit sequences that pass
	// the Luhn checksum. Spaces and dashes are tolerated as
	// separators (scrubbed before validation). Brand
	// detection (Visa/MC/Amex/Discover) is on Finding.Detail.
	CategoryCreditCard Category = "credit_card"

	// CategoryAWSAccessKey — AWS IAM access-key IDs
	// (`AKIA...`). The 16 chars after the prefix are
	// upper-case alphanumeric per AWS's own format.
	CategoryAWSAccessKey Category = "aws_access_key"

	// CategoryGitHubToken — GitHub fine-grained / classic
	// tokens (`ghp_`, `ghs_`, `gho_`, `ghu_`, `ghr_`). The
	// prefix narrows enough that we don't need a checksum.
	CategoryGitHubToken Category = "github_token"

	// CategoryJWT — JSON Web Tokens. Three base64url segments
	// joined by `.`; the first must start with `eyJ` which is
	// the base64 of `{"` and is overwhelmingly the JWT
	// signal. False positives on hand-crafted base64 are
	// acceptable.
	CategoryJWT Category = "jwt"

	// CategoryEmail — RFC-shaped email addresses. Useful for
	// PII redaction even though email itself isn't strictly
	// secret. The pattern is intentionally narrow (no
	// quoted-local-part) to keep false-positive rate low on
	// normal prose.
	CategoryEmail Category = "email"
)

// patterns is the v0 regex table. Each entry has a compiled
// regex and a category; the order matches the constants block
// above. Compiled at package init so Scan stays allocation-free
// after the first call.
//
// Notes on individual patterns:
//
//   - SSN uses word-boundaries on both sides so it doesn't
//     match inside a longer digit run. The middle digits 00 and
//     last 0000 are technically invalid SSNs but excluding them
//     would cost a check and miss real-but-misformatted ones —
//     acceptable false-positive surface.
//   - Credit card regex is permissive (digit sequences with
//     optional space/dash separators); the actual filter is
//     the Luhn check inside Scan.
//   - AWS key prefix includes ASIA (temporary tokens) alongside
//     AKIA so STS-issued keys are caught too.
//   - GitHub token: `gh[psohur]_[A-Za-z0-9_]{36,}` covers the
//     five public prefixes. The 36-char floor matches GitHub's
//     own minimum.
//   - JWT: anchored on the `eyJ` header signature; allow `-_`
//     in the base64url segments.
//   - Email: standard `local@domain.tld` with letters / digits
//     / `._%+-` in the local part and `.` in the domain.
var patterns = []struct {
	cat Category
	re  *regexp.Regexp
}{
	{CategorySSN, regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)},
	{CategoryCreditCard, regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)},
	{CategoryAWSAccessKey, regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{CategoryGitHubToken, regexp.MustCompile(`\bgh[psohur]_[A-Za-z0-9_]{36,}\b`)},
	{CategoryJWT, regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)},
	{CategoryEmail, regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)},
}
