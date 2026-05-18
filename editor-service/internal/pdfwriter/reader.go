package pdfwriter

import (
	"bytes"
	"fmt"
	"strconv"
)

// Trailer captures the minimum information from a prior revision that we
// need to seed the next incremental update.
//
// Fields:
//
//   - StartXref: the byte offset of the *most recent* xref section in
//     the prior revision (the value that appeared after `startxref`).
//     This is what the new trailer's /Prev entry will point to.
//   - Size: the prior revision's /Size — the smallest integer larger
//     than the highest object number defined. We use it to size the new
//     trailer; the actual /Size we emit may be larger if the update
//     introduces new object numbers.
//   - Root: the indirect reference (e.g. "1 0 R") to the document
//     catalog. Required in every trailer.
//   - Info: optional reference to the /Info dictionary; preserved when
//     present so reader-side metadata doesn't disappear after an update.
//   - HasXrefStream: true when the prior revision uses an xref *stream*
//     instead of a classic xref *table*. v0 of this package refuses to
//     extend xref-stream documents — see doc.go.
type Trailer struct {
	StartXref     int64
	Size          int
	Root          string
	Info          string
	HasXrefStream bool
}

// Discover scans `pdf` for the most recent trailer + startxref and
// returns the seed for an incremental update.
//
// The scan walks backwards from EOF for two markers:
//
//	%%EOF        — end-of-file marker
//	startxref    — preceded by the xref byte offset
//
// If the byte at that offset is an `xref` keyword we parse the classic
// trailer dictionary that follows. If it's an object header (`N 0 obj`)
// it's an xref stream — we set HasXrefStream and let the caller decide.
//
// Errors are returned for malformed inputs (no %%EOF, no startxref, no
// usable trailer). The error messages are intended to surface in tests
// and ops logs; they include byte offsets when known.
func Discover(pdf []byte) (Trailer, error) {
	var t Trailer
	if len(pdf) == 0 {
		return t, fmt.Errorf("pdfwriter: empty pdf")
	}
	eofPos := bytes.LastIndex(pdf, []byte("%%EOF"))
	if eofPos < 0 {
		return t, fmt.Errorf("pdfwriter: missing %%EOF marker")
	}
	startXrefPos := bytes.LastIndex(pdf[:eofPos], []byte("startxref"))
	if startXrefPos < 0 {
		return t, fmt.Errorf("pdfwriter: missing startxref before %%EOF")
	}
	// Between startxref and %%EOF, the next non-whitespace token is the
	// xref offset as an ASCII integer.
	tail := pdf[startXrefPos+len("startxref") : eofPos]
	offset, err := readInt(tail)
	if err != nil {
		return t, fmt.Errorf("pdfwriter: parse startxref offset: %w", err)
	}
	if offset < 0 || int64(len(pdf)) <= offset {
		return t, fmt.Errorf("pdfwriter: startxref offset %d out of range", offset)
	}
	t.StartXref = offset

	// Inspect the bytes at offset. Classic xref tables start with the
	// keyword `xref`; xref streams (ISO 32000-1 §7.5.8) start with an
	// object header `N G obj` whose dictionary carries `/Type /XRef`.
	//
	// Either way the metadata we need (/Size, /Root, /Info) lives in
	// a flat dictionary — the difference is just where the keyword
	// boundary sits and whether there's a compressed binary payload
	// after it. We DON'T need to decode the payload to write the next
	// incremental section: the new section will be a classic xref
	// table whose /Prev points at this offset, and the reader follows
	// /Prev through both stream and table forms transparently.
	at := pdf[offset:]
	switch {
	case bytes.HasPrefix(at, []byte("xref")):
		size, root, info, err := parseClassicTrailer(pdf, offset)
		if err != nil {
			return t, err
		}
		t.Size = size
		t.Root = root
		t.Info = info
	case isObjectHeader(at):
		size, root, info, err := parseXrefStreamDict(pdf, offset)
		if err != nil {
			return t, err
		}
		t.Size = size
		t.Root = root
		t.Info = info
		t.HasXrefStream = true
	default:
		return t, fmt.Errorf("pdfwriter: startxref offset %d does not point at `xref` or an object header", offset)
	}
	return t, nil
}

// isObjectHeader reports whether the bytes at the start of `b` look
// like a PDF indirect-object header: `<num> <num> obj`. Only the prefix
// is inspected — enough to disambiguate from the classic `xref`
// keyword. We don't validate the full grammar here.
func isObjectHeader(b []byte) bool {
	i := 0
	// First integer
	start := i
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	if i == start || i >= len(b) || b[i] != ' ' {
		return false
	}
	i++ // space
	// Second integer
	start = i
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	if i == start || i >= len(b) || b[i] != ' ' {
		return false
	}
	i++ // space
	return bytes.HasPrefix(b[i:], []byte("obj"))
}

// parseXrefStreamDict reads the dictionary of the xref-stream object
// starting at `xrefOffset` and extracts the /Size, /Root, /Info
// entries — the same metadata `parseClassicTrailer` returns for the
// table form.
//
// We deliberately do NOT decode the stream payload. The incremental
// writer doesn't need the cross-reference entries (it computes them
// from the objects it appends) and the payload is compressed
// (FlateDecode + per-row predictors), so parsing it would pull a lot
// of code for no v0 benefit.
func parseXrefStreamDict(pdf []byte, xrefOffset int64) (size int, root string, info string, err error) {
	tail := pdf[xrefOffset:]
	// The dict is between the first `<<` and the matching `>>`.
	dictStart := bytes.Index(tail, []byte("<<"))
	if dictStart < 0 {
		err = fmt.Errorf("pdfwriter: xref-stream at %d has no dictionary", xrefOffset)
		return
	}
	dictEnd := bytes.Index(tail[dictStart:], []byte(">>"))
	if dictEnd < 0 {
		err = fmt.Errorf("pdfwriter: xref-stream dict missing closing `>>`")
		return
	}
	dict := tail[dictStart+2 : dictStart+dictEnd]

	if v, ok := lookupName(dict, "Size"); ok {
		n, perr := strconv.Atoi(string(v))
		if perr == nil {
			size = n
		}
	}
	if v, ok := lookupName(dict, "Root"); ok {
		root = string(bytes.TrimSpace(v))
	}
	if v, ok := lookupName(dict, "Info"); ok {
		info = string(bytes.TrimSpace(v))
	}
	if size == 0 || root == "" {
		err = fmt.Errorf("pdfwriter: xref-stream dict missing required /Size or /Root (size=%d root=%q)", size, root)
		return
	}
	return
}

// parseClassicTrailer reads the trailer dictionary that follows the xref
// table starting at `xrefOffset`. We only look for /Size, /Root, /Info —
// everything else is opaque and ignored.
func parseClassicTrailer(pdf []byte, xrefOffset int64) (size int, root string, info string, err error) {
	tail := pdf[xrefOffset:]
	trailerPos := bytes.Index(tail, []byte("trailer"))
	if trailerPos < 0 {
		err = fmt.Errorf("pdfwriter: xref at %d has no trailer keyword", xrefOffset)
		return
	}
	dictStart := bytes.Index(tail[trailerPos:], []byte("<<"))
	if dictStart < 0 {
		err = fmt.Errorf("pdfwriter: trailer dict missing <<")
		return
	}
	dictEnd := bytes.Index(tail[trailerPos+dictStart:], []byte(">>"))
	if dictEnd < 0 {
		err = fmt.Errorf("pdfwriter: trailer dict missing >>")
		return
	}
	dict := tail[trailerPos+dictStart+2 : trailerPos+dictStart+dictEnd]

	if v, ok := lookupName(dict, "Size"); ok {
		n, perr := strconv.Atoi(string(v))
		if perr == nil {
			size = n
		}
	}
	if v, ok := lookupName(dict, "Root"); ok {
		root = string(bytes.TrimSpace(v))
	}
	if v, ok := lookupName(dict, "Info"); ok {
		info = string(bytes.TrimSpace(v))
	}
	if size == 0 || root == "" {
		err = fmt.Errorf("pdfwriter: trailer missing required /Size or /Root (size=%d root=%q)", size, root)
		return
	}
	return
}

// readInt skips leading whitespace and reads an ASCII integer, returning
// an error on the empty case.
func readInt(b []byte) (int64, error) {
	i := 0
	for i < len(b) && isWhitespace(b[i]) {
		i++
	}
	start := i
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		i++
	}
	if i == start {
		return 0, fmt.Errorf("no digits")
	}
	return strconv.ParseInt(string(b[start:i]), 10, 64)
}

func isWhitespace(b byte) bool {
	switch b {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}

// lookupName scans a flat PDF dictionary body (already stripped of the
// outer << >>) for `/Name` and returns the value bytes — everything
// from after the name up to (but not including) the next `/` or end of
// input. This is intentionally crude: it does not descend into nested
// dictionaries or arrays. Trailer values we care about (/Size /Root
// /Info) are flat — integers or indirect refs — so this is sufficient.
func lookupName(dict []byte, name string) ([]byte, bool) {
	needle := []byte("/" + name)
	pos := bytes.Index(dict, needle)
	if pos < 0 {
		return nil, false
	}
	end := pos + len(needle)
	if end >= len(dict) {
		return nil, false
	}
	// Require a delimiter after the name so /SizeFoo doesn't match /Size.
	switch dict[end] {
	case ' ', '\t', '\n', '\r', '/':
	default:
		return nil, false
	}
	// Value runs to the next "/" (start of the next key) or to end.
	start := end
	for start < len(dict) && isWhitespace(dict[start]) {
		start++
	}
	stop := start
	for stop < len(dict) && dict[stop] != '/' {
		stop++
	}
	return bytes.TrimSpace(dict[start:stop]), true
}
