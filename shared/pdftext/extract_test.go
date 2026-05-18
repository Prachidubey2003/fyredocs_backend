package pdftext

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// minimalPDF builds the smallest PDF that pdfcpu will parse,
// with `body` as the (raw, unescaped) text inside a single Tj
// literal. Used across the round-trip tests so each test
// expresses its intent as "I put X in the PDF; Extract should
// give me back X" without re-implementing the structural
// scaffolding.
//
// PDF objects:
//   1  /Catalog → Pages
//   2  /Pages   → Kids [3]
//   3  /Page    → /Contents 4
//   4  /Contents stream with `BT (body) Tj ET`
//
// Caller supplies the body bytes verbatim — they're inserted
// after the `(` and before the `)` so the caller controls the
// exact in-stream bytes (and can therefore exercise the
// escape decoder without us round-tripping through Go string
// literals).
func minimalPDF(streamBody string) []byte {
	contents := "BT /F1 12 Tf 72 720 Td (" + streamBody + ") Tj ET"
	contentObj := fmt.Sprintf(
		"4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		len(contents), contents,
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

// multiPagePDF builds a 2-page PDF, each page containing one
// Tj with the supplied per-page bodies. Used to verify
// page-join behaviour + per-page traversal.
func multiPagePDF(page1Body, page2Body string) []byte {
	c1 := "BT /F1 12 Tf 72 720 Td (" + page1Body + ") Tj ET"
	c2 := "BT /F1 12 Tf 72 720 Td (" + page2Body + ") Tj ET"
	obj4 := fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(c1), c1)
	obj6 := fmt.Sprintf("6 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(c2), c2)
	parts := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 7 0 R >> >> /Contents 4 0 R >>\nendobj\n",
		obj4,
		"5 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 7 0 R >> >> /Contents 6 0 R >>\nendobj\n",
		obj6,
		"7 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	}
	return buildClassic(parts, "1 0 R")
}

// blankPagePDF returns a single-page PDF with no /Contents
// — used to confirm Extract handles image-only / blank pages
// cleanly (returns "" not an error).
func blankPagePDF() []byte {
	parts := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>\nendobj\n",
	}
	return buildClassic(parts, "1 0 R")
}

// buildClassic is a near-copy of the editor-service test
// helper. Assembles a PDF from `parts` (each must be a
// fully-formed `N 0 obj … endobj\n` block in source order),
// then appends a classic xref table + trailer pointing at
// `root`.
//
// Kept here, not imported, because shared/ can't depend on
// editor-service per CLAUDE.md § 1. The duplicate is small
// and stable.
func buildClassic(parts []string, root string) []byte {
	header := "%PDF-1.4\n"
	offsets := make([]int, 0, len(parts))
	pos := len(header)
	for _, p := range parts {
		offsets = append(offsets, pos)
		pos += len(p)
	}
	var b bytes.Buffer
	b.Grow(pos + 200)
	b.WriteString(header)
	for _, p := range parts {
		b.WriteString(p)
	}
	xrefOff := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(parts)+1)
	b.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root %s >>\nstartxref\n%d\n%%%%EOF\n",
		len(parts)+1, root, xrefOff)
	return b.Bytes()
}

// ---- Extract -----------------------------------------------------------

func TestExtract_SimpleSinglePage(t *testing.T) {
	pdf := minimalPDF("Hello world")
	got, err := Extract(pdf)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(got, "Hello world") {
		t.Errorf("got %q, expected to contain %q", got, "Hello world")
	}
}

func TestExtract_PreservesSensitivePatternsForDLP(t *testing.T) {
	// The whole point of pdftext is letting the DLP scanner
	// see PII inside PDFs. Bake in some representative
	// patterns and confirm they round-trip readable.
	pdf := minimalPDF("Patient SSN 123-45-6789 charged 4111-1111-1111-1111")
	got, err := Extract(pdf)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{
		"SSN", "123-45-6789", "4111-1111-1111-1111",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from extracted text: %q", want, got)
		}
	}
}

func TestExtract_DecodesPDFStringEscapes(t *testing.T) {
	// Construct a literal with the standard PDF escape
	// sequences in the stream itself (NOT through Go string
	// literal interpretation). The escape decoder must
	// produce the corresponding bytes.
	//
	//   \(   →  (
	//   \)   →  )
	//   \\   →  \
	//   \n   →  newline
	//   \t   →  tab
	//   \101 →  octal A
	streamBody := `Hello \(world\)\\ tab\there newline\nletter\101`
	pdf := minimalPDF(streamBody)
	got, err := Extract(pdf)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Expected decoded form (Go literal interpretation reversed):
	wantFragments := []string{
		"Hello (world)\\ tab\there newline\nletterA",
	}
	for _, want := range wantFragments {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from %q", want, got)
		}
	}
}

func TestExtract_HandlesBalancedNestedParens(t *testing.T) {
	// PDF spec § 7.3.4.2: balanced parens inside a literal
	// don't need escaping. Our extractor must depth-track or
	// it will truncate the string at the first unescaped ')'.
	pdf := minimalPDF("Name (Acme Corp) info")
	got, err := Extract(pdf)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(got, "Name (Acme Corp) info") {
		t.Errorf("balanced nested parens truncated: %q", got)
	}
}

func TestExtract_HandlesMultiplePages(t *testing.T) {
	pdf := multiPagePDF("First page text", "Second page text")
	got, err := Extract(pdf)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(got, "First page text") {
		t.Errorf("missing page-1 content: %q", got)
	}
	if !strings.Contains(got, "Second page text") {
		t.Errorf("missing page-2 content: %q", got)
	}
	// And a newline boundary between them so DLP regexes
	// don't false-merge across page breaks.
	if !strings.Contains(got, "First page text") ||
		!strings.Contains(got[strings.Index(got, "First page text"):], "\n") {
		t.Errorf("expected newline separator between pages: %q", got)
	}
}

func TestExtract_BlankPageIsNotAnError(t *testing.T) {
	// A page with no /Contents (image-only scan, blank form,
	// etc.) should produce an empty string with no error.
	got, err := Extract(blankPagePDF())
	if err != nil {
		t.Errorf("blank-page PDF returned err: %v", err)
	}
	if got != "" {
		t.Errorf("blank-page PDF returned %q, want empty", got)
	}
}

func TestExtract_RejectsEmptyInput(t *testing.T) {
	if _, err := Extract(nil); err == nil {
		t.Error("nil input should produce an error")
	}
	if _, err := Extract([]byte{}); err == nil {
		t.Error("empty bytes should produce an error")
	}
}

func TestExtract_RejectsGarbageInput(t *testing.T) {
	if _, err := Extract([]byte("this is not a PDF")); err == nil {
		t.Error("garbage input should produce an error")
	}
}

func TestExtract_IgnoresCommentsContainingParens(t *testing.T) {
	// PDF comments (`% to end-of-line`) inside the content
	// stream must not be mis-interpreted as text. A `(`
	// inside a comment should NOT open a literal.
	//
	// Build a content stream where a comment contains `(` so
	// the walker has to skip past it. The literal that
	// follows is the real string.
	stream := "% this is a fake (literal\n" +
		"BT /F1 12 Tf 72 720 Td (real text) Tj ET"
	contentObj := fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		len(stream), stream)
	parts := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n",
		contentObj,
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	}
	pdf := buildClassic(parts, "1 0 R")
	got, err := Extract(pdf)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if strings.Contains(got, "fake") {
		t.Errorf("comment text leaked into extracted output: %q", got)
	}
	if !strings.Contains(got, "real text") {
		t.Errorf("real literal missing after comment: %q", got)
	}
}

func TestExtract_SeparatesAdjacentLiteralsWithSpace(t *testing.T) {
	// Two Tj calls in a row should produce two separated
	// strings in the output, not one concatenation. Important
	// for DLP: a "1234" Tj followed by a "5678" Tj that prints
	// as "1234 5678" visually should NOT scan as "12345678"
	// (which could trigger a false credit-card finding).
	stream := "BT /F1 12 Tf 72 720 Td (1234) Tj (5678) Tj ET"
	contentObj := fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		len(stream), stream)
	parts := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n",
		contentObj,
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	}
	pdf := buildClassic(parts, "1 0 R")
	got, err := Extract(pdf)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if strings.Contains(got, "12345678") {
		t.Errorf("adjacent literals merged without separator: %q", got)
	}
	if !strings.Contains(got, "1234") || !strings.Contains(got, "5678") {
		t.Errorf("missing one of the literals: %q", got)
	}
}

// ---- Unit tests on the helpers -----------------------------------------

func TestReadBalancedLiteral_FlatString(t *testing.T) {
	inner, n := readBalancedLiteral([]byte("(hello)world"))
	if string(inner) != "hello" {
		t.Errorf("inner = %q, want %q", inner, "hello")
	}
	if n != 7 {
		t.Errorf("advance = %d, want 7", n)
	}
}

func TestReadBalancedLiteral_NestedBalanced(t *testing.T) {
	inner, n := readBalancedLiteral([]byte("(a (b) c)tail"))
	if string(inner) != "a (b) c" {
		t.Errorf("inner = %q", inner)
	}
	if n != 9 {
		t.Errorf("advance = %d", n)
	}
}

func TestReadBalancedLiteral_EscapedParens(t *testing.T) {
	// `\(` and `\)` inside a literal don't change depth.
	inner, n := readBalancedLiteral([]byte(`(open\(close\))more`))
	if string(inner) != `open\(close\)` {
		t.Errorf("inner = %q", inner)
	}
	if n != 15 {
		t.Errorf("advance = %d", n)
	}
}

func TestReadBalancedLiteral_NotALiteral(t *testing.T) {
	inner, n := readBalancedLiteral([]byte("not a paren"))
	if inner != nil || n != 0 {
		t.Errorf("non-paren start should return nil/0; got %q/%d", inner, n)
	}
}

func TestReadBalancedLiteral_UnbalancedReadsToEOF(t *testing.T) {
	// Permissive on malformed input — return what we read.
	inner, n := readBalancedLiteral([]byte("(unbalanced"))
	if string(inner) != "unbalanced" {
		t.Errorf("inner = %q", inner)
	}
	if n != 11 {
		t.Errorf("advance = %d", n)
	}
}

func TestDecodePDFLiteral_NoEscapesIsFastPath(t *testing.T) {
	in := []byte("plain ASCII no backslashes")
	got := decodePDFLiteral(in)
	if &got[0] != &in[0] {
		t.Errorf("expected fast-path to return the input slice verbatim (no allocation)")
	}
}

func TestDecodePDFLiteral_EveryStandardEscape(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`\n`, "\n"},
		{`\r`, "\r"},
		{`\t`, "\t"},
		{`\b`, "\b"},
		{`\f`, "\f"},
		{`\\`, `\`},
		{`\(`, "("},
		{`\)`, ")"},
		{`\101`, "A"},      // octal
		{`\053`, "+"},      // octal
		{`\0`, "\x00"},     // shortest octal
		{`\x`, "x"},        // unknown escape — drop backslash
		{`abc\nxyz`, "abc\nxyz"},
		{`hello \(world\)`, "hello (world)"},
	}
	for _, c := range cases {
		got := decodePDFLiteral([]byte(c.in))
		if string(got) != c.want {
			t.Errorf("decodePDFLiteral(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDecodePDFLiteral_LineContinuation(t *testing.T) {
	// `\<newline>` drops both bytes (line continuation for
	// long literals split across source lines).
	got := decodePDFLiteral([]byte("foo\\\nbar"))
	if string(got) != "foobar" {
		t.Errorf("line continuation: got %q, want %q", got, "foobar")
	}
}
