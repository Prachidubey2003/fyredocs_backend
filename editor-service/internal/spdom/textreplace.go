package spdom

import (
	"bytes"
	"errors"
)

// ErrLiteralNotFound is returned by ReplaceFirstLiteral when no
// `(text) Tj` operator carrying the requested literal is present
// in the stream. Callers map this to a 400 INVALID_INPUT so the
// user sees "couldn't find the text you asked to replace" rather
// than a 500.
var ErrLiteralNotFound = errors.New("spdom: literal not found in content stream")

// ReplaceFirstLiteral finds the first `(literal) Tj` operator in
// `stream` whose decoded literal text equals `find` and rewrites
// it to `(escaped(replace)) Tj`. Returns the new stream bytes.
//
// What's in scope (v0):
//   - Plain `Tj` with a `(…)` literal-string operand.
//   - Standard PDF escape sequences inside the literal: `\(`,
//     `\)`, `\\`, balanced unescaped parens.
//
// What's NOT in scope (returns ErrLiteralNotFound or passes
// through unchanged):
//   - TJ arrays (`[(a) -50 (b)] TJ`) — text broken into pieces
//     across numeric kerning shifts. A future iteration that
//     understands width-preservation will need TJ; for now we
//     only match single-piece runs.
//   - Hex strings `<48656c6c6f>` for non-Latin1 fonts.
//   - Reflow/width validation. Replacing with longer text may
//     overflow the surrounding layout; callers needing
//     reflow-safe edits must pre-validate against AFM widths.
//
// The new stream is a fresh byte slice — the caller owns it and
// the original `stream` is untouched.
func ReplaceFirstLiteral(stream []byte, find, replace string) ([]byte, error) {
	off, length, ok := findTjLiteralByText(stream, find)
	if !ok {
		return nil, ErrLiteralNotFound
	}
	rendered := encodeLiteral(replace)
	out := make([]byte, 0, len(stream)-length+len(rendered))
	out = append(out, stream[:off]...)
	out = append(out, rendered...)
	out = append(out, stream[off+length:]...)
	return out, nil
}

// findTjLiteralByText scans `stream` for `(literal) Tj` operators
// and returns the byte range of the literal (including its
// surrounding parens) whose decoded content equals `target`.
// `ok=false` means no match.
//
// We walk the stream linearly because the operand of Tj is always
// the most recently-pushed literal — scanning forward and
// tracking "last literal start/end" works without a real parser.
// When we hit the keyword `Tj`, we check the buffered literal's
// decoded text against the target.
func findTjLiteralByText(stream []byte, target string) (off, length int, ok bool) {
	var litStart, litEnd int
	hasLit := false

	pos := 0
	for pos < len(stream) {
		c := stream[pos]
		switch {
		case c == '%':
			// Comment to end of line.
			pos = endOfLine(stream, pos)
		case c == '(':
			// Find matching close-paren, respecting escapes and
			// nesting. Record the byte range; this is what we'd
			// rewrite if Tj fires next.
			endIdx, ok2 := scanLiteralEnd(stream, pos)
			if !ok2 {
				return 0, 0, false
			}
			litStart = pos
			litEnd = endIdx
			hasLit = true
			pos = endIdx + 1
		case c == '[':
			// TJ array. We don't peek inside — the next Tj/TJ
			// keyword consumes the buffered state. Skip to the
			// matching ']' so we don't false-match parens inside.
			end := scanArrayEnd(stream, pos)
			if end < 0 {
				return 0, 0, false
			}
			hasLit = false
			pos = end + 1
		case c == '<':
			// Hex string or dict. Skip whichever it is — both end
			// at the matching '>' / '>>'.
			pos = scanAngleEnd(stream, pos)
		case isASCIILetter(c):
			// Operator name. Read until whitespace/delim.
			end := pos
			for end < len(stream) && !isWhitespaceOrDelim(stream[end]) {
				end++
			}
			op := stream[pos:end]
			pos = end
			if hasLit && (bytes.Equal(op, []byte("Tj")) || bytes.Equal(op, []byte("'"))) {
				if string(decodeLiteral(stream[litStart+1:litEnd])) == target {
					return litStart, litEnd - litStart + 1, true
				}
				hasLit = false
			} else if hasLit && bytes.Equal(op, []byte(`"`)) {
				// The `"` operator takes (acsp, awsp, string) —
				// we'd need to track all three operands. Skip
				// for v0.
				hasLit = false
			}
		default:
			pos++
		}
	}
	return 0, 0, false
}

// scanLiteralEnd returns the index of the closing ')' for the
// literal starting at `start` (which must point at the '('),
// handling backslash escapes and nested unescaped parens. -1 is
// not returned; the bool says whether we found a terminator.
func scanLiteralEnd(stream []byte, start int) (int, bool) {
	depth := 1
	i := start + 1
	for i < len(stream) {
		c := stream[i]
		switch c {
		case '\\':
			// Skip the escape sequence — single char is enough
			// because no escape introduces another paren we
			// need to balance.
			i += 2
		case '(':
			depth++
			i++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
			i++
		default:
			i++
		}
	}
	return 0, false
}

// scanArrayEnd returns the index of the closing ']' for the array
// starting at `start` ('['), nesting and string-aware. -1 = bad
// stream; callers treat that as "no match found anywhere".
func scanArrayEnd(stream []byte, start int) int {
	depth := 0
	for i := start; i < len(stream); i++ {
		switch stream[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		case '(':
			end, ok := scanLiteralEnd(stream, i)
			if !ok {
				return -1
			}
			i = end
		case '%':
			i = endOfLine(stream, i) - 1
		}
	}
	return -1
}

// scanAngleEnd handles both hex strings `<…>` and dictionaries
// `<<…>>`. We don't need to interpret either; we just skip past.
func scanAngleEnd(stream []byte, start int) int {
	if start+1 < len(stream) && stream[start+1] == '<' {
		// Dictionary — scan to matching `>>`, nested-dict-aware.
		depth := 1
		i := start + 2
		for i+1 < len(stream) {
			if stream[i] == '<' && stream[i+1] == '<' {
				depth++
				i += 2
				continue
			}
			if stream[i] == '>' && stream[i+1] == '>' {
				depth--
				i += 2
				if depth == 0 {
					return i
				}
				continue
			}
			i++
		}
		return len(stream)
	}
	// Hex string — single '>' terminator.
	for i := start + 1; i < len(stream); i++ {
		if stream[i] == '>' {
			return i + 1
		}
	}
	return len(stream)
}

// endOfLine returns the index just past the next CR/LF.
func endOfLine(stream []byte, start int) int {
	for i := start; i < len(stream); i++ {
		if stream[i] == '\n' || stream[i] == '\r' {
			return i + 1
		}
	}
	return len(stream)
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isWhitespaceOrDelim(b byte) bool {
	switch b {
	case 0, '\t', '\n', '\f', '\r', ' ', '(', ')', '<', '>', '[', ']', '/', '%':
		return true
	}
	return false
}

// encodeLiteral renders a Go string as a PDF literal-string
// token `(...)`, escaping parens and backslash. Newlines /
// non-ASCII bytes pass through unescaped — readers handle them.
func encodeLiteral(s string) []byte {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '(')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '(', ')', '\\':
			out = append(out, '\\', c)
		default:
			out = append(out, c)
		}
	}
	out = append(out, ')')
	return out
}
