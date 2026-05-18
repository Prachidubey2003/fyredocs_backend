// Package pdftext is the shared PDF-text extraction primitive
// for the Fyredocs platform. Returns a single string of all
// PDF text-literal content across every page of an input PDF.
//
// The library is intentionally minimal — DLP scanning + simple
// search-indexing are the v0 consumers, and both want raw
// strings without per-glyph positions / font / page metadata.
// Editor-service has a much richer state-machine extractor in
// `internal/spdom/` that yields positioned glyph events; that
// machinery is overkill for "is there an SSN in this PDF".
//
// Coverage (v0):
//   - PDF string literals `(...)` inside content streams.
//     Decodes the standard PDF escapes (`\(`, `\)`, `\\`,
//     `\n`, `\r`, `\t`, `\b`, `\f`, octal `\nnn`).
//   - Balanced nested parens inside a literal (per PDF spec).
//
// What this library does NOT do yet (tracked):
//   - Hex strings `<48 65 6c 6c 6f>` — used in some
//     CJK/Asian-language PDFs. False-negative source for DLP
//     in that subset. Caller can post-process for hex blobs
//     if needed.
//   - ToUnicode CMap resolution. Embedded fonts that map
//     custom CIDs to private-use glyphs won't decode to
//     human-readable text. Real-world contracts / forms /
//     legal PDFs use Identity encoding for standard glyphs
//     so this is usually fine for DLP; rich
//     extraction-quality work belongs in editor-service.
//   - Reading order / paragraph breaks. Output is whatever
//     order the content stream traversed; pages joined by
//     `\n`. DLP scanning treats this as a bag of strings, so
//     reading order doesn't matter.
//   - Position info, font, size — only the string content.
//
// This package is pure transformation: in = []byte, out =
// string. No filesystem, no network. Caller streams PDF bytes
// from wherever (assembled-upload temp file, audit-log
// archive, document storage).
package pdftext

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
)

// MaxOutputBytes is the cap on the total extracted text we
// return. Prevents an adversarial PDF (or a legitimate
// 10000-page legal archive) from ballooning the caller's
// memory. Truncation is silent — callers that need fidelity
// over a full-coverage scan should chunk the PDF themselves.
const MaxOutputBytes = 8 * 1024 * 1024 // 8MB

// Extract reads a PDF from `pdfBytes` and returns every text
// literal across every page concatenated with newlines.
// Returns an empty string + nil error for valid PDFs with no
// text content (image-only scans, blank documents).
//
// Errors:
//   - non-nil + empty output for inputs that aren't valid
//     PDFs (parse failure surfaces with the pdfcpu error
//     wrapped).
//   - never returns nil error with non-empty output if
//     truncation happened — silent truncation when the
//     output crosses MaxOutputBytes.
func Extract(pdfBytes []byte) (string, error) {
	if len(pdfBytes) == 0 {
		return "", fmt.Errorf("pdftext: empty input")
	}

	ctx, err := api.ReadContext(bytes.NewReader(pdfBytes), nil)
	if err != nil {
		return "", fmt.Errorf("pdftext: parse PDF: %w", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return "", fmt.Errorf("pdftext: resolve page count: %w", err)
	}

	var out strings.Builder
	out.Grow(64 * 1024) // most documents fit; oversize doesn't matter

	for page := 1; page <= ctx.PageCount; page++ {
		// pdfcpu returns nil + error on pages without
		// content streams (image-only, blank); treat as
		// "no text", not as a fatal error.
		rd, err := pdfcpu.ExtractPageContent(ctx, page)
		if err != nil || rd == nil {
			continue
		}
		stream, err := io.ReadAll(rd)
		if err != nil || len(stream) == 0 {
			continue
		}

		// Walk the content stream, decoding every `(...)`
		// literal into the output. We don't track text-state
		// (Tj/'/"/TJ vs other operators) because EVERY
		// PDF string literal is potentially-interesting
		// text — a font dictionary name encoded as a literal
		// shouldn't show up here, but if it does the DLP
		// false-positive cost is much lower than the
		// false-negative cost of missing a real SSN.
		appendLiterals(&out, stream)

		// Page separator. Helps the DLP regex anchor on
		// per-page boundaries (e.g., not matching SSN
		// patterns that straddle a page break).
		if out.Len() > 0 && out.Len() < MaxOutputBytes {
			out.WriteByte('\n')
		}

		if out.Len() >= MaxOutputBytes {
			// Silent truncation. DLP at this scale wants
			// "did we find anything" not "did we find
			// everything"; partial coverage is fine.
			break
		}
	}

	// Trim a trailing newline (from the per-page separator).
	result := strings.TrimRight(out.String(), "\n")
	if len(result) > MaxOutputBytes {
		result = result[:MaxOutputBytes]
	}
	return result, nil
}

// appendLiterals walks `stream` byte-by-byte; on every `(`
// outside an existing comment, it reads a balanced PDF
// literal (handling `\(`, `\)`, `\\`, and nested unescaped
// parens per ISO 32000-1 § 7.3.4.2), decodes the escape
// sequences, and writes the result to `out`. Other bytes are
// ignored — PDF operators, dictionary references, hex
// strings, etc. all pass through silently.
//
// Comments (`%` to end-of-line) are skipped because a literal
// `(` inside a comment must not be interpreted as a string
// start. Outside comments, raw `)` characters that don't
// match an open `(` are also ignored (they're operands to
// some non-text operator we don't care about).
func appendLiterals(out *strings.Builder, stream []byte) {
	i := 0
	for i < len(stream) {
		if out.Len() >= MaxOutputBytes {
			return
		}
		c := stream[i]
		switch c {
		case '%':
			// Comment until newline.
			for i < len(stream) && stream[i] != '\n' && stream[i] != '\r' {
				i++
			}
		case '(':
			lit, advance := readBalancedLiteral(stream[i:])
			i += advance
			if len(lit) > 0 {
				decoded := decodePDFLiteral(lit)
				// Insert a space between consecutive
				// literals so adjacent strings don't
				// false-merge into one DLP candidate
				// (e.g., two adjacent runs "(123-)" and
				// "(45-6789)" shouldn't form a fake SSN
				// that crosses a literal boundary —
				// inserting whitespace breaks the regex
				// anchor).
				if out.Len() > 0 {
					out.WriteByte(' ')
				}
				out.Write(decoded)
			}
		default:
			i++
		}
	}
}

// readBalancedLiteral consumes a `(...)`-delimited PDF literal
// starting at `b[0] == '('`. Returns the inner bytes (without
// the surrounding parens) + the number of bytes consumed from
// `b` (including both parens).
//
// Inner content respects:
//   - `\(`, `\)`, `\\` — escaped paren / backslash, doesn't
//     change depth.
//   - Unescaped `(` / `)` — change nesting depth; the
//     matching outer `)` ends the literal.
//   - Anything else copied verbatim.
//
// If the input is malformed (unbalanced parens), reads to
// end-of-input and returns what it has — best-effort.
func readBalancedLiteral(b []byte) (inner []byte, advance int) {
	if len(b) == 0 || b[0] != '(' {
		return nil, 0
	}
	depth := 1
	i := 1
	for i < len(b) && depth > 0 {
		c := b[i]
		switch c {
		case '\\':
			// Escape: include the escape + the following
			// byte verbatim (the decoder handles them).
			if i+1 < len(b) {
				inner = append(inner, '\\', b[i+1])
				i += 2
			} else {
				inner = append(inner, '\\')
				i++
			}
		case '(':
			depth++
			inner = append(inner, c)
			i++
		case ')':
			depth--
			if depth == 0 {
				i++ // consume the closing paren
				return inner, i
			}
			inner = append(inner, c)
			i++
		default:
			inner = append(inner, c)
			i++
		}
	}
	// Unbalanced — return what we read. PDF spec says
	// strict readers reject; we're a permissive DLP scanner.
	return inner, i
}

// decodePDFLiteral resolves the PDF string-escape sequences
// inside an already-extracted literal body (no surrounding
// parens). Per ISO 32000-1 § 7.3.4.2:
//
//   \n \r \t \b \f → corresponding C escape
//   \\             → single backslash
//   \( \)          → literal parens
//   \nnn           → octal byte value (up to 3 digits)
//   \<newline>     → line-continuation, drops both bytes
//   \<anything-else> → drops the backslash, keeps the char
//
// Allocation-efficient: returns the input unchanged when no
// backslash is present (the common case for plain text).
func decodePDFLiteral(b []byte) []byte {
	// Fast path — no backslashes, no transformation.
	if bytes.IndexByte(b, '\\') < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c != '\\' || i+1 >= len(b) {
			out = append(out, c)
			continue
		}
		// Look at the byte following the backslash.
		next := b[i+1]
		switch next {
		case 'n':
			out = append(out, '\n')
			i++
		case 'r':
			out = append(out, '\r')
			i++
		case 't':
			out = append(out, '\t')
			i++
		case 'b':
			out = append(out, '\b')
			i++
		case 'f':
			out = append(out, '\f')
			i++
		case '\\':
			out = append(out, '\\')
			i++
		case '(':
			out = append(out, '(')
			i++
		case ')':
			out = append(out, ')')
			i++
		case '\n':
			// Line continuation: drop both bytes.
			i++
		case '\r':
			// CR or CRLF line continuation.
			i++
			if i+1 < len(b) && b[i+1] == '\n' {
				i++
			}
		default:
			if next >= '0' && next <= '7' {
				// Octal escape: 1–3 digits. Consume
				// greedily up to 3 octal digits.
				val := 0
				digits := 0
				for digits < 3 && i+1 < len(b) && b[i+1] >= '0' && b[i+1] <= '7' {
					val = val*8 + int(b[i+1]-'0')
					i++
					digits++
				}
				out = append(out, byte(val))
			} else {
				// Unknown escape: PDF spec says drop the
				// backslash and emit the next char.
				out = append(out, next)
				i++
			}
		}
	}
	return out
}
