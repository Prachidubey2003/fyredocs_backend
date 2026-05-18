// Package corpus is the editor's golden test fixture set — PDFs
// every editor test exercises edits against. Plan §5.10 calls for
// a 10k-doc corpus in git-lfs eventually; until that lands, this
// package generates a small set of hand-crafted PDFs in Go so the
// fixtures live in the source tree (no LFS, no binary churn) and
// every consumer has bit-for-bit identical bytes regardless of
// where the test runs.
//
// Each generator returns a fresh []byte so callers can mutate the
// result without affecting later calls. Builders are deterministic
// — repeated calls return the same bytes.
//
// Why one package rather than per-fixture files: the byte-layout
// is tightly coupled to the xref table, and centralising the
// "build parts → compute offsets → emit xref + trailer" pattern
// keeps every generator easy to read and trivially extendable.
package corpus

import (
	"bytes"
	"fmt"
	"strings"
)

// Minimal returns a 1-page, no-content PDF. Equivalent to the
// hand-rolled minimalPDF() helpers previously duplicated across
// pdfedit, pdfwriter, spdom, and editops test files.
//
// Page size is US Letter (612 x 792 pt). No fonts, no content
// stream — readers that require /Contents synthesise an empty
// stream. Sufficient for every editor op that mutates page-level
// metadata or appends annotation objects without inspecting the
// content stream.
func Minimal() []byte {
	return buildClassic([]string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>\nendobj\n",
	}, "1 0 R")
}

// MultiPage returns a PDF with `n` blank pages. n must be >= 1;
// values <= 0 are clamped to 1 to avoid producing an invalid PDF
// with no pages (which pdfcpu rejects). Used to exercise
// page.delete + page.insert ops that change /Kids / /Count.
//
// Object layout: catalog (1), pages root (2), then page leaves
// 3..3+n-1. Keeping the page-leaf IDs contiguous after the
// pages-root makes it easy to reason about the xref offsets when
// debugging a failure.
func MultiPage(n int) []byte {
	if n < 1 {
		n = 1
	}
	var kids []string
	for i := 0; i < n; i++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", 3+i))
	}
	parts := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		fmt.Sprintf("2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n",
			strings.Join(kids, " "), n),
	}
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf(
			"%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>\nendobj\n",
			3+i,
		))
	}
	return buildClassic(parts, "1 0 R")
}

// WithText returns a 1-page PDF that displays each entry of `lines`
// as a separate text line (Helvetica 12pt, baseline starting at
// y=750 and descending 16pt per line). The /Contents stream and
// /Font resource are added so the page actually renders something
// — sPDOM tests need glyph-bearing content to extract.
//
// Special characters are escaped via PDF's literal-string rules
// so callers can embed parens / backslashes safely.
func WithText(lines []string) []byte {
	var stream bytes.Buffer
	stream.WriteString("BT\n/F1 12 Tf\n")
	y := 750
	for _, ln := range lines {
		fmt.Fprintf(&stream, "1 0 0 1 72 %d Tm\n(%s) Tj\n", y, escapeLiteral(ln))
		y -= 16
	}
	stream.WriteString("ET\n")

	contentObj := fmt.Sprintf(
		"4 0 obj\n<< /Length %d >>\nstream\n%sendstream\nendobj\n",
		stream.Len(), stream.String(),
	)
	parts := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n",
		contentObj,
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	}
	return buildClassic(parts, "1 0 R")
}

// buildClassic assembles a PDF from `parts` (each must be a
// fully-formed `N 0 obj … endobj\n` block in source order), then
// appends a classic xref table + trailer pointing at `root`.
//
// Returns the assembled bytes. We use the classic xref form
// (rather than xref streams) because every editor op in v0 emits
// classic incremental sections and we want fixtures to exercise
// the same path. Once §4.3.1's xref-stream incremental writer
// lands, mirror this with buildXrefStream().
func buildClassic(parts []string, root string) []byte {
	header := "%PDF-1.4\n"
	offsets := make([]int, 0, len(parts))
	pos := len(header)
	for _, p := range parts {
		offsets = append(offsets, pos)
		pos += len(p)
	}
	xrefStart := pos

	var buf bytes.Buffer
	buf.WriteString(header)
	for _, p := range parts {
		buf.WriteString(p)
	}
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(parts)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %s >>\nstartxref\n%d\n%%%%EOF\n",
		len(parts)+1, root, xrefStart)
	return buf.Bytes()
}

// escapeLiteral applies PDF literal-string escaping (ISO 32000-1
// §7.3.4.2) to parens and backslash. Newlines / tabs pass through
// — readers interpret them as the corresponding control chars,
// which is what we want for multi-line content.
//
// Mirrors the helper in internal/pdfedit/annotation.go; duplicated
// here so the corpus package has no dependency on pdfedit.
func escapeLiteral(s string) string {
	if s == "" {
		return ""
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '(', ')', '\\':
			out = append(out, '\\', c)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}
