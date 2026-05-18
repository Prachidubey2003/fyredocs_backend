package pdfedit

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"editor-service/internal/pdfwriter"
	"editor-service/internal/spdom"
)

// Sentinel errors so the editops dispatcher can map them to
// HTTP status codes without scraping strings.
var (
	// ErrTextNotFound — the requested literal isn't in any
	// `(text) Tj` operator on the page. 400 from the handler.
	ErrTextNotFound = errors.New("pdfedit: text not found on page")

	// ErrStreamFiltered — the content stream has a /Filter
	// pipeline (typically /FlateDecode). v0 doesn't decode +
	// re-encode; users get a 400 telling them this PDF needs
	// the decompress + re-edit flow that lands later.
	ErrStreamFiltered = errors.New("pdfedit: content stream is compressed; v0 only edits uncompressed streams")

	// ErrContentsNotInline — the page's /Contents is an array
	// of streams (legal but uncommon) or absent entirely. Both
	// hit the same error today.
	ErrContentsNotInline = errors.New("pdfedit: page /Contents is not a single indirect-ref stream")
)

// ReplaceText finds the first `(find) Tj` operator on `pageNum`
// and rewrites it to `(replace) Tj`, emitting a new revision
// via the same incremental-update path the rest of pdfedit uses.
//
// Constraints in v0 (each surfaces as a sentinel error so the
// caller can map to a specific user message):
//   - The page's `/Contents` must be a single indirect-ref
//     stream (no array, no inline).
//   - That stream must be uncompressed (`/Filter` absent or
//     empty). FlateDecode-only handling is a follow-up.
//   - The text must appear as a single literal-string Tj
//     operand. TJ-array runs are skipped (positioning-numbers
//     between glyphs).
//   - No reflow/width validation: a longer replacement may
//     visually overrun; the call still succeeds.
//
// The new revision is layered on top of `original` as an
// incremental update — original bytes preserved verbatim, so
// any prior signature stays valid.
func ReplaceText(original []byte, pageNum int, find, replace string) ([]byte, error) {
	if pageNum < 1 {
		return nil, fmt.Errorf("pdfedit: pageNum %d must be >= 1", pageNum)
	}
	if find == "" {
		return nil, fmt.Errorf("pdfedit: text.replace requires a non-empty `find`")
	}

	ctx, err := api.ReadContext(bytes.NewReader(original), nil)
	if err != nil {
		return nil, fmt.Errorf("pdfedit: read source PDF: %w", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("pdfedit: resolve page count: %w", err)
	}
	if pageNum > ctx.PageCount {
		return nil, fmt.Errorf("pdfedit: pageNum %d out of range [1, %d]", pageNum, ctx.PageCount)
	}

	pageDict, _, _, err := ctx.PageDict(pageNum, false)
	if err != nil {
		return nil, fmt.Errorf("pdfedit: lookup page %d: %w", pageNum, err)
	}

	contentsObj, ok := pageDict.Find("Contents")
	if !ok {
		return nil, fmt.Errorf("%w: page has no /Contents", ErrContentsNotInline)
	}
	contentsRef, ok := contentsObj.(types.IndirectRef)
	if !ok {
		// /Contents was an Array (multi-stream page) or inline
		// stream — both legal, neither supported in v0.
		return nil, fmt.Errorf("%w (got %T)", ErrContentsNotInline, contentsObj)
	}
	contentsObjNum := contentsRef.ObjectNumber.Value()

	// Locate the content-stream object's byte offset via the xref.
	entry, found := ctx.XRefTable.FindTableEntry(contentsObjNum, 0)
	if !found || entry.Free || entry.Offset == nil {
		return nil, fmt.Errorf("pdfedit: /Contents object %d has no xref entry", contentsObjNum)
	}
	startOff := int(*entry.Offset)
	dictBytes, streamBytes, err := extractStreamObject(original, startOff)
	if err != nil {
		return nil, err
	}

	// Refuse compressed streams in v0.
	if dictHasFilter(dictBytes) {
		return nil, ErrStreamFiltered
	}

	newStream, err := spdom.ReplaceFirstLiteral(streamBytes, find, replace)
	if err != nil {
		if errors.Is(err, spdom.ErrLiteralNotFound) {
			return nil, fmt.Errorf("%w: %q", ErrTextNotFound, find)
		}
		return nil, err
	}

	// Build the new stream-object body with the rewritten /Length.
	newBody := buildStreamBody(dictBytes, newStream)

	var u pdfwriter.Update
	u.Set(contentsObjNum, newBody)
	return u.Bytes(original)
}

// extractStreamObject parses the body of an indirect stream
// object starting at `off` in `original`. Returns the dictionary
// bytes (everything between `<<` and `>>`) and the raw stream
// bytes (everything between `stream\n` and `\nendstream`).
//
// We don't fully tokenise — this is the simplest balanced-angle
// scan needed for v0's "uncompressed page content stream"
// requirement. A future general-purpose parser belongs in
// pdfwriter alongside Discover().
func extractStreamObject(original []byte, off int) (dict []byte, stream []byte, err error) {
	if off < 0 || off >= len(original) {
		return nil, nil, fmt.Errorf("pdfedit: object offset %d out of range", off)
	}
	// Skip past "N G obj" header.
	i := off
	// Skip whitespace at start.
	for i < len(original) && isPDFWhitespace(original[i]) {
		i++
	}
	// Skip "N G obj" — find "obj" keyword.
	objIdx := bytes.Index(original[i:], []byte("obj"))
	if objIdx < 0 {
		return nil, nil, fmt.Errorf("pdfedit: missing `obj` keyword at offset %d", off)
	}
	i += objIdx + len("obj")
	for i < len(original) && isPDFWhitespace(original[i]) {
		i++
	}

	// Dictionary: must start with `<<`.
	if i+1 >= len(original) || original[i] != '<' || original[i+1] != '<' {
		return nil, nil, fmt.Errorf("pdfedit: stream object at %d does not start with a dictionary", off)
	}
	dictEnd, ok := findDictEnd(original, i)
	if !ok {
		return nil, nil, fmt.Errorf("pdfedit: unterminated dictionary at %d", i)
	}
	dict = original[i+2 : dictEnd]
	i = dictEnd + 2

	// Whitespace, then `stream` keyword.
	for i < len(original) && isPDFWhitespace(original[i]) {
		i++
	}
	if !bytes.HasPrefix(original[i:], []byte("stream")) {
		return nil, nil, fmt.Errorf("pdfedit: expected `stream` keyword after dict")
	}
	i += len("stream")
	// PDF spec: a single \r\n or \n must follow `stream`.
	if i < len(original) && original[i] == '\r' {
		i++
	}
	if i < len(original) && original[i] == '\n' {
		i++
	}
	streamStart := i

	// Find `endstream`.
	endIdx := bytes.Index(original[i:], []byte("endstream"))
	if endIdx < 0 {
		return nil, nil, fmt.Errorf("pdfedit: missing `endstream`")
	}
	streamEnd := i + endIdx
	// Trim trailing newline before endstream (per spec).
	if streamEnd > streamStart && original[streamEnd-1] == '\n' {
		streamEnd--
	}
	if streamEnd > streamStart && original[streamEnd-1] == '\r' {
		streamEnd--
	}
	stream = original[streamStart:streamEnd]
	return dict, stream, nil
}

// findDictEnd returns the byte index of the closing `>>` for a
// dictionary starting at `start` (which must point at the
// opening `<<`).
func findDictEnd(b []byte, start int) (int, bool) {
	depth := 0
	i := start
	for i+1 < len(b) {
		if b[i] == '<' && b[i+1] == '<' {
			depth++
			i += 2
			continue
		}
		if b[i] == '>' && b[i+1] == '>' {
			depth--
			i += 2
			if depth == 0 {
				return i - 2, true
			}
			continue
		}
		// Skip over string literals (parens) so `>>` inside a
		// /Contents (...) value doesn't confuse us. PDF dict
		// values like /Title (>>) are rare but spec-legal.
		if b[i] == '(' {
			end, ok := skipParenString(b, i)
			if !ok {
				return 0, false
			}
			i = end + 1
			continue
		}
		i++
	}
	return 0, false
}

func skipParenString(b []byte, start int) (int, bool) {
	depth := 1
	i := start + 1
	for i < len(b) {
		switch b[i] {
		case '\\':
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

// dictHasFilter returns true when the dict bytes contain a
// `/Filter` key. We don't parse the value — its mere presence is
// enough to refuse the v0 path. A future iteration that handles
// /FlateDecode would parse the value and dispatch on the filter
// chain.
func dictHasFilter(dict []byte) bool {
	return bytes.Contains(dict, []byte("/Filter"))
}

// buildStreamBody constructs the body bytes of a replacement
// stream object. `dictBytes` is the existing dictionary content
// (without the surrounding `<<` `>>`); we strip its /Length entry
// and inject a fresh one matching `newStream`.
//
// The output format is `<< /Length N {rest} >>\nstream\n{bytes}\nendstream`.
func buildStreamBody(dictBytes []byte, newStream []byte) []byte {
	stripped := stripLengthEntry(dictBytes)
	var buf bytes.Buffer
	buf.WriteString("<<")
	buf.WriteString(" /Length ")
	buf.WriteString(strconv.Itoa(len(newStream)))
	if len(stripped) > 0 {
		// Only add a separator if the remaining dict bytes don't
		// already start with whitespace.
		if !isPDFWhitespace(stripped[0]) {
			buf.WriteByte(' ')
		}
		buf.Write(stripped)
	}
	buf.WriteString(" >>\nstream\n")
	buf.Write(newStream)
	buf.WriteString("\nendstream")
	return buf.Bytes()
}

// stripLengthEntry removes a `/Length N` key+value pair from the
// dictionary bytes. We accept either an integer or a `M 0 R`
// indirect-reference value (rare for /Length but legal). Returns
// the dict bytes with the entry excised; preserves the rest
// verbatim so /Filter etc. survive unchanged.
func stripLengthEntry(dict []byte) []byte {
	idx := bytes.Index(dict, []byte("/Length"))
	if idx < 0 {
		return dict
	}
	// Confirm /Length isn't a substring of /LengthN or similar.
	end := idx + len("/Length")
	if end < len(dict) && !isPDFWhitespace(dict[end]) && dict[end] != '/' && dict[end] != '<' && dict[end] != '>' {
		// Probably /Length1 or /LengthChange — skip this match.
		// Quick conservative bail; we leave the key in place.
		return dict
	}
	// Skip whitespace after /Length.
	for end < len(dict) && isPDFWhitespace(dict[end]) {
		end++
	}
	// Skip the value: an integer, or `M G R`.
	for end < len(dict) && !isPDFWhitespace(dict[end]) && dict[end] != '/' && dict[end] != '>' {
		end++
	}
	// If next non-space is a digit, this was the start of `M G R` —
	// skip generation + R too.
	for end < len(dict) && isPDFWhitespace(dict[end]) {
		end++
	}
	if end < len(dict) && (dict[end] >= '0' && dict[end] <= '9') {
		for end < len(dict) && !isPDFWhitespace(dict[end]) && dict[end] != '/' && dict[end] != '>' {
			end++
		}
		for end < len(dict) && isPDFWhitespace(dict[end]) {
			end++
		}
		if end < len(dict) && dict[end] == 'R' {
			end++
		}
	}
	// Trim trailing whitespace following the value to keep the
	// remaining dict bytes tight.
	for end < len(dict) && isPDFWhitespace(dict[end]) {
		end++
	}
	return append([]byte{}, append(dict[:idx], dict[end:]...)...)
}

func isPDFWhitespace(b byte) bool {
	switch b {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}
