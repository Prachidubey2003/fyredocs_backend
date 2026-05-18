package spdom

import (
	"bytes"
	"strconv"
	"unicode"
)

// walkOps scans a PDF content stream and invokes fn for each operator
// with its accumulated operand stack. Used by both `extractText` (the
// plain-text path) and the position-aware extractor in layout.go.
//
// The operands slice is reused across calls — copy any operand you need
// to retain past the next invocation.
//
// Operators the scanner can't read (binary blobs, unknown sequences) are
// silently advanced; this is best-effort, not validating.
func walkOps(stream []byte, fn func(op string, operands []token)) {
	s := &scanner{src: stream}
	var operands []token
	for {
		s.skipWhitespace()
		if s.eof() {
			break
		}
		if s.peek() == '%' {
			s.skipTo('\n')
			continue
		}
		tok, ok := s.nextToken()
		if !ok {
			break
		}
		if tok.kind == tokOp {
			fn(string(tok.bytes), operands)
			operands = operands[:0]
		} else {
			operands = append(operands, tok)
		}
	}
}

// extractText is a minimal PDF content-stream text extractor.
//
// It walks the stream for the four text-show operators defined in
// ISO 32000-1 §9.4.3:
//
//	Tj       — show one literal string:        (text) Tj
//	'        — move next line + show string:   (text) '
//	"        — set spacing + show string:      aw ac (text) "
//	TJ       — show one array of strings:      [ (a) -50 (b) ] TJ
//
// The output is the concatenation of those strings, with a `\n` after
// each Tj/'/"/TJ operator. This is the L2.5 plain-text path; for
// position-aware extraction with real bboxes (LayoutPass=2) see
// layout.go's extractTextEvents which also walks via walkOps.
//
// Caveats:
//   - Strings are raw 8-bit bytes; we do not consult the font /ToUnicode
//     map. Latin text in stock fonts comes through cleanly.
//   - No text-matrix tracking — use layout.go for positions.
//   - No nested-stream or graphics-state save/restore.
func extractText(stream []byte) string {
	var out bytes.Buffer
	walkOps(stream, func(op string, operands []token) {
		switch op {
		case "Tj", "'", `"`:
			if len(operands) == 0 {
				return
			}
			last := operands[len(operands)-1]
			if last.kind == tokString {
				out.Write(decodeLiteral(last.bytes))
				out.WriteByte('\n')
			}
		case "TJ":
			if len(operands) == 0 {
				return
			}
			last := operands[len(operands)-1]
			if last.kind == tokArray {
				for _, item := range last.array {
					if item.kind == tokString {
						out.Write(decodeLiteral(item.bytes))
					}
				}
				out.WriteByte('\n')
			}
		}
	})
	return out.String()
}

// ---- minimal PDF content-stream tokenizer ----------------------------------
//
// A real PDF content stream parser is large; this one handles only what we
// need to find text-show operators. Anything we don't recognise is skipped
// (advanced to the next whitespace).

type tokKind int

const (
	tokOp tokKind = iota
	tokString
	tokNumber
	tokName
	tokArray
)

type token struct {
	kind  tokKind
	bytes []byte  // raw bytes for strings (without the wrapping parens), numbers, names, ops
	array []token // tokArray only

	// start, end are byte offsets in the source stream spanning the
	// ENTIRE token, including any wrapping delimiters: the `(…)` for
	// strings, the `[…]` for arrays, the leading `/` for names. Used
	// by ScrubRect to perform surgical byte-range rewrites without
	// having to re-tokenise. Zero for callers that don't populate
	// them (most legacy paths through walkOps).
	start, end int
}

type scanner struct {
	src []byte
	pos int
}

func (s *scanner) eof() bool  { return s.pos >= len(s.src) }
func (s *scanner) peek() byte { return s.src[s.pos] }
func (s *scanner) advance() {
	if s.pos < len(s.src) {
		s.pos++
	}
}

func (s *scanner) skipWhitespace() {
	for !s.eof() && isWhitespace(s.peek()) {
		s.advance()
	}
}

func (s *scanner) skipTo(c byte) {
	for !s.eof() && s.peek() != c {
		s.advance()
	}
}

func isWhitespace(b byte) bool {
	switch b {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}

func isDelim(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

// nextToken returns the next non-comment, non-whitespace token. It returns
// false at EOF or on a token we can't parse.
func (s *scanner) nextToken() (token, bool) {
	if s.eof() {
		return token{}, false
	}
	b := s.peek()
	switch {
	case b == '(':
		return s.readStringLiteral()
	case b == '[':
		return s.readArray()
	case b == '<':
		// We intentionally don't decode hex strings or dictionaries; just
		// skip them (they aren't operands for the text-show ops we care
		// about in the common Latin case).
		return s.skipBalancedAngle(), true
	case b == ']' || b == '>':
		// Should have been consumed inside readArray / hex/dict skip.
		s.advance()
		return token{kind: tokName, bytes: []byte{b}}, true
	case b == '/':
		return s.readName()
	case b == '-' || b == '+' || b == '.' || (b >= '0' && b <= '9'):
		return s.readNumber()
	default:
		return s.readOp()
	}
}

func (s *scanner) readStringLiteral() (token, bool) {
	// Pre-condition: peek() == '('
	tokStart := s.pos
	s.advance()
	start := s.pos
	depth := 1
	for !s.eof() {
		c := s.peek()
		switch c {
		case '\\':
			// Escaped char — advance past `\` then the next byte unconditionally
			s.advance()
			if !s.eof() {
				s.advance()
			}
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				body := s.src[start:s.pos]
				s.advance() // consume ')'
				return token{
					kind:  tokString,
					bytes: body,
					start: tokStart,
					end:   s.pos,
				}, true
			}
		}
		s.advance()
	}
	return token{}, false
}

func (s *scanner) readArray() (token, bool) {
	// Pre: peek() == '['
	tokStart := s.pos
	s.advance()
	out := token{kind: tokArray, start: tokStart}
	for !s.eof() {
		s.skipWhitespace()
		if s.eof() {
			break
		}
		if s.peek() == ']' {
			s.advance()
			out.end = s.pos
			return out, true
		}
		tok, ok := s.nextToken()
		if !ok {
			break
		}
		out.array = append(out.array, tok)
	}
	return out, false
}

func (s *scanner) skipBalancedAngle() token {
	// Could be a hex-string `<...>` or a dict `<<...>>`. We don't care
	// which — just skip past matching brackets. Treat as unknown token.
	depth := 0
	for !s.eof() {
		c := s.peek()
		s.advance()
		switch c {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return token{kind: tokName, bytes: []byte{'<'}}
			}
		}
	}
	return token{kind: tokName, bytes: []byte{'<'}}
}

func (s *scanner) readName() (token, bool) {
	// Pre: peek() == '/'
	s.advance()
	start := s.pos
	for !s.eof() {
		c := s.peek()
		if isWhitespace(c) || isDelim(c) {
			break
		}
		s.advance()
	}
	return token{kind: tokName, bytes: s.src[start:s.pos]}, true
}

func (s *scanner) readNumber() (token, bool) {
	start := s.pos
	for !s.eof() {
		c := s.peek()
		if !((c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+') {
			break
		}
		s.advance()
	}
	b := s.src[start:s.pos]
	if len(b) == 0 {
		return token{}, false
	}
	// Sanity check: if it doesn't parse as a number, treat as an op.
	if _, err := strconv.ParseFloat(string(b), 64); err != nil {
		return token{kind: tokOp, bytes: b}, true
	}
	return token{kind: tokNumber, bytes: b}, true
}

func (s *scanner) readOp() (token, bool) {
	start := s.pos
	for !s.eof() {
		c := s.peek()
		if isWhitespace(c) || isDelim(c) {
			break
		}
		s.advance()
	}
	b := s.src[start:s.pos]
	if len(b) == 0 {
		// Unparseable byte — advance and continue.
		s.advance()
		return token{}, false
	}
	return token{kind: tokOp, bytes: b}, true
}

// decodeLiteral applies the PDF string-literal escape rules from
// ISO 32000-1 §7.3.4.2.
func decodeLiteral(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c != '\\' {
			out.WriteByte(c)
			continue
		}
		if i+1 >= len(b) {
			break
		}
		i++
		c = b[i]
		switch c {
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case 'b':
			out.WriteByte('\b')
		case 'f':
			out.WriteByte('\f')
		case '(', ')', '\\':
			out.WriteByte(c)
		case '\n', '\r':
			// Backslash + newline = line continuation; consume an
			// optional second newline char of a CRLF pair.
			if c == '\r' && i+1 < len(b) && b[i+1] == '\n' {
				i++
			}
		default:
			if c >= '0' && c <= '7' {
				// Up to 3 octal digits.
				val := int(c - '0')
				for n := 0; n < 2 && i+1 < len(b); n++ {
					nxt := b[i+1]
					if nxt < '0' || nxt > '7' {
						break
					}
					val = val*8 + int(nxt-'0')
					i++
				}
				out.WriteByte(byte(val))
			} else {
				// Unknown escape: keep as-is.
				out.WriteByte(c)
			}
		}
	}
	return out.Bytes()
}

// stripControlBytes drops bytes that are neither printable ASCII / whitespace
// nor part of a multi-byte UTF-8 sequence. Useful when emitting Lines for
// the API — PDF content streams sometimes carry NUL or low control codes
// from font encodings without ToUnicode maps.
func stripControlBytes(s string) string {
	var out bytes.Buffer
	out.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsPrint(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}
