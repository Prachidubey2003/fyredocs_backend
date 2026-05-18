package invoice

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"fyredocs/shared/pdftext"
)

// fixtureInvoice is a known-good invoice the PDF tests share.
// Computed via New so totals match what the renderer reads.
func fixtureInvoice(t *testing.T) Invoice {
	t.Helper()
	inv, err := New(Invoice{
		Number:   "FYR-2026-0001",
		IssuedAt: "2026-05-17",
		DueAt:    "2026-06-16",
		Currency: "usd",
		Issuer: Party{
			Name:        "Fyredocs Inc.",
			Email:       "billing@fyredocs.com",
			AddressLine: "1 Market Street\nSan Francisco, CA 94105",
			TaxID:       "EIN 12-3456789",
		},
		Customer: Party{
			Name:        "Acme Corp",
			Email:       "ap@acme.example",
			AddressLine: "500 Industrial Ave\nDetroit, MI 48201",
		},
		Lines: []LineItem{
			{Description: "Pro plan — May 2026", Quantity: 1, UnitPriceCents: 1200},
			{Description: "API overage (2,400 calls × $0.001)", Quantity: 2400, UnitPriceCents: 1},
			{Description: "Discount: annual prepay", Quantity: 1, UnitPriceCents: -200},
		},
		TaxBps: 825,
		Memo:   "Thank you for your business.",
	})
	if err != nil {
		t.Fatalf("fixtureInvoice: %v", err)
	}
	return inv
}

func TestRenderPDF_ReturnsValidPDFMagic(t *testing.T) {
	inv := fixtureInvoice(t)
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.")) {
		t.Errorf("output does not start with PDF magic: %q", pdf[:min(8, len(pdf))])
	}
	if !bytes.Contains(pdf, []byte("%%EOF")) {
		t.Error("output missing trailer marker")
	}
}

func TestRenderPDF_RoundTripsThroughPDFText(t *testing.T) {
	// The strongest invariant: a PDF the platform's own text
	// extractor (shared/pdftext) can read back. If the bytes
	// are malformed enough that pdfcpu rejects them, this
	// fails — and we'd never know via a magic-bytes check
	// alone.
	inv := fixtureInvoice(t)
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text, err := pdftext.Extract(pdf)
	if err != nil {
		t.Fatalf("pdftext.Extract: %v\nfirst 200 bytes: %q", err, pdf[:min(200, len(pdf))])
	}
	// Header content must round-trip.
	for _, want := range []string{"INVOICE", "FYR-2026-0001", "Fyredocs Inc.", "Acme Corp"} {
		if !strings.Contains(text, want) {
			t.Errorf("extracted text missing %q\ngot: %s", want, text)
		}
	}
}

func TestRenderPDF_IncludesLineDescriptionsAndTotals(t *testing.T) {
	inv := fixtureInvoice(t)
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text, err := pdftext.Extract(pdf)
	if err != nil {
		t.Fatalf("pdftext.Extract: %v", err)
	}
	// Line items + computed totals must all appear.
	wants := []string{
		"Pro plan",
		"API overage",
		"Discount", // negative line surfaces as a regular line
		"Subtotal",
		"Tax",
		"Total",
	}
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("PDF text missing %q\ngot: %s", w, text)
		}
	}
}

func TestRenderPDF_OmitsTaxRowWhenZeroTax(t *testing.T) {
	// Zero-tax invoices shouldn't render a "Tax $0.00" row —
	// it's visual noise. Keep the renderer terse for the
	// common no-tax case (digital-only subscriptions).
	inv, err := New(Invoice{
		Number:   "FYR-2026-NOVAT",
		Currency: "USD",
		Lines:    []LineItem{{Description: "Pro plan", Quantity: 1, UnitPriceCents: 1200}},
		TaxBps:   0,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text, err := pdftext.Extract(pdf)
	if err != nil {
		t.Fatalf("pdftext.Extract: %v", err)
	}
	if strings.Contains(text, "Tax") {
		t.Errorf("zero-tax invoice should not include a Tax row; got:\n%s", text)
	}
	if !strings.Contains(text, "Subtotal") || !strings.Contains(text, "Total") {
		t.Errorf("Subtotal+Total must still appear; got:\n%s", text)
	}
}

func TestRenderPDF_RejectsEmptyNumber(t *testing.T) {
	inv := fixtureInvoice(t)
	inv.Number = ""
	pdf, err := inv.RenderPDF()
	if !errors.Is(err, ErrEmptyNumber) {
		t.Errorf("expected ErrEmptyNumber; got %v", err)
	}
	if pdf != nil {
		t.Errorf("expected nil bytes on guard failure; got %d bytes", len(pdf))
	}
}

func TestRenderPDF_RejectsEmptyCurrency(t *testing.T) {
	inv := fixtureInvoice(t)
	inv.Currency = ""
	_, err := inv.RenderPDF()
	if !errors.Is(err, ErrEmptyCurrency) {
		t.Errorf("expected ErrEmptyCurrency; got %v", err)
	}
}

func TestRenderPDF_DeterministicForFixedInput(t *testing.T) {
	// Stable bytes lets callers golden-test, hash for cache
	// keys, and dedupe identical invoice renders. Two RenderPDF
	// calls on the same Invoice must produce byte-identical
	// output.
	inv := fixtureInvoice(t)
	a, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	b, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("renders differ; len(a)=%d len(b)=%d", len(a), len(b))
	}
}

func TestRenderPDF_TruncatesBeyondPerDocumentCap(t *testing.T) {
	// Past pdfMaxTotalLines the renderer truncates AND still
	// produces a valid (multi-page) PDF with an omitted-lines
	// notice on the final page. Defends against an adversarial
	// 100k-row "invoice" that would balloon the byte buffer.
	lines := make([]LineItem, pdfMaxTotalLines+5)
	for i := range lines {
		lines[i] = LineItem{Description: "overflow item", Quantity: 1, UnitPriceCents: 100}
	}
	inv, err := New(Invoice{
		Number:   "FYR-OVERFLOW",
		Currency: "USD",
		Lines:    lines,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pdf, err := inv.RenderPDF()
	if !errors.Is(err, ErrLineItemsTruncated) {
		t.Errorf("expected ErrLineItemsTruncated; got %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.")) {
		t.Error("expected valid PDF bytes alongside the truncation error")
	}
	text, perr := pdftext.Extract(pdf)
	if perr != nil {
		t.Fatalf("truncated PDF should still parse: %v", perr)
	}
	if !strings.Contains(text, "more line") {
		t.Errorf("expected omitted-lines notice in PDF; got:\n%s", text)
	}
}

func TestRenderPDF_EscapesParensAndBackslashesInUserText(t *testing.T) {
	// PDF literals must escape `(`, `)`, `\`. A line
	// description containing `(test)` must NOT break the
	// content stream — the produced PDF must still parse.
	inv, err := New(Invoice{
		Number:   "FYR-ESCAPE",
		Currency: "USD",
		Issuer:   Party{Name: `Tricky\Co. (LLC)`},
		Lines: []LineItem{
			{Description: `Edge-case line (with parens) and a back\slash`, Quantity: 1, UnitPriceCents: 5000},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text, err := pdftext.Extract(pdf)
	if err != nil {
		t.Fatalf("escaped PDF should still parse: %v", err)
	}
	if !strings.Contains(text, "with parens") {
		t.Errorf("paren-escaped line did not survive round-trip; got:\n%s", text)
	}
	// Backslash survives. Issuer name (untruncated) carries it
	// so we don't run into the description-column truncation.
	if !strings.Contains(text, `Tricky\Co.`) {
		t.Errorf("backslash-escaped issuer name did not survive round-trip; got:\n%s", text)
	}
}

func TestRenderPDF_HandlesUnicodeRunesInDescriptions(t *testing.T) {
	// WinAnsi-mapped accented chars pass through; emoji/CJK
	// will render as glyph-not-found in real readers (a v0
	// limitation documented in render_pdf.go). The test asserts
	// only that the rendering doesn't error and the bytes
	// don't corrupt the content stream.
	inv, err := New(Invoice{
		Number:   "FYR-UNI",
		Currency: "USD",
		Lines:    []LineItem{{Description: "Café visit — Zürich", Quantity: 1, UnitPriceCents: 1234}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	if _, err := pdftext.Extract(pdf); err != nil {
		t.Errorf("unicode line broke PDF: %v", err)
	}
}

// ---- escapeLiteral unit tests ------------------------------------------

func TestEscapeLiteral_FastPathPassesThrough(t *testing.T) {
	in := "plain ascii — no special chars"
	if got := escapeLiteral(in); got != in {
		t.Errorf("fast path should return verbatim; got %q", got)
	}
}

func TestEscapeLiteral_EscapesParensBackslashAndNewlines(t *testing.T) {
	in := `back\slash and (open) and )close( and a
newline`
	got := escapeLiteral(in)
	for _, want := range []string{`\\`, `\(`, `\)`, `\n`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing escape sequence %q in output %q", want, got)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---- pagination + multi-page tests -------------------------------------

func makeLines(n int) []LineItem {
	out := make([]LineItem, n)
	for i := range out {
		out[i] = LineItem{
			Description:    fmt.Sprintf("line item %d", i+1),
			Quantity:       1,
			UnitPriceCents: 100,
		}
	}
	return out
}

func TestPaginate_SinglePageWhenFits(t *testing.T) {
	pages := paginate(makeLines(pdfLinesPerSinglePage))
	if len(pages) != 1 {
		t.Fatalf("expected 1 page; got %d", len(pages))
	}
	if len(pages[0]) != pdfLinesPerSinglePage {
		t.Errorf("page 1 size = %d; want %d", len(pages[0]), pdfLinesPerSinglePage)
	}
}

func TestPaginate_TwoPagesJustOverThreshold(t *testing.T) {
	// One row past the single-page threshold flips to the
	// multi-page layout (firstPage + lastPage with totals
	// reserve). Pin the exact split so a refactor of the
	// capacity constants can't silently overflow a page.
	n := pdfLinesPerSinglePage + 1
	pages := paginate(makeLines(n))
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages for n=%d; got %d", n, len(pages))
	}
	if len(pages[0]) != pdfLinesPerFirstPage {
		t.Errorf("page 1 size = %d; want pdfLinesPerFirstPage=%d", len(pages[0]), pdfLinesPerFirstPage)
	}
	if len(pages[1]) != n-pdfLinesPerFirstPage {
		t.Errorf("page 2 size = %d; want %d", len(pages[1]), n-pdfLinesPerFirstPage)
	}
}

func TestPaginate_ThreePagesWithInterior(t *testing.T) {
	// pdfLinesPerFirstPage + pdfLinesPerInteriorPage + 1
	// forces an interior page to appear. The last page holds
	// the trailing remainder.
	n := pdfLinesPerFirstPage + pdfLinesPerInteriorPage + 1
	pages := paginate(makeLines(n))
	if len(pages) != 3 {
		t.Fatalf("expected 3 pages for n=%d; got %d (sizes=%v)", n, len(pages), pageSizes(pages))
	}
	if len(pages[0]) != pdfLinesPerFirstPage {
		t.Errorf("page 1 size = %d; want %d", len(pages[0]), pdfLinesPerFirstPage)
	}
	if len(pages[1]) != pdfLinesPerInteriorPage {
		t.Errorf("page 2 size = %d; want %d", len(pages[1]), pdfLinesPerInteriorPage)
	}
	if len(pages[2]) != 1 {
		t.Errorf("page 3 size = %d; want 1", len(pages[2]))
	}
}

func TestPaginate_AllLinesPreserved(t *testing.T) {
	// Pagination must be lossless: summing chunk lengths
	// equals the input length, in order.
	for _, n := range []int{0, 1, 10, 22, 23, 60, 200, 499} {
		pages := paginate(makeLines(n))
		total := 0
		for _, p := range pages {
			total += len(p)
		}
		if total != n {
			t.Errorf("n=%d: sum of chunks = %d; want %d (sizes=%v)", n, total, n, pageSizes(pages))
		}
	}
}

func pageSizes(pages [][]LineItem) []int {
	sizes := make([]int, len(pages))
	for i, p := range pages {
		sizes[i] = len(p)
	}
	return sizes
}

func TestRenderPDF_TwoPageInvoiceParsesAndContainsTotals(t *testing.T) {
	// 40-line invoice → 2 pages. The totals row + memo must
	// land on the LAST page only.
	inv, err := New(Invoice{
		Number:   "FYR-MULTI",
		IssuedAt: "2026-05-17",
		Currency: "USD",
		Issuer:   Party{Name: "Fyredocs Inc."},
		Customer: Party{Name: "Acme Corp"},
		Lines:    makeLines(40),
		TaxBps:   825,
		Memo:     "Pay within 30 days.",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text, err := pdftext.Extract(pdf)
	if err != nil {
		t.Fatalf("pdftext.Extract: %v", err)
	}

	// Page footer indicates pagination.
	if !strings.Contains(text, "Page 1 of 2") {
		t.Errorf("expected 'Page 1 of 2' footer; got:\n%s", text)
	}
	if !strings.Contains(text, "Page 2 of 2") {
		t.Errorf("expected 'Page 2 of 2' footer; got:\n%s", text)
	}
	// Continuation header on page 2.
	if !strings.Contains(text, "continued") {
		t.Errorf("expected 'continued' continuation header; got:\n%s", text)
	}
	// Totals appear (on last page).
	for _, want := range []string{"Subtotal", "Tax", "Total", "Pay within 30 days"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in PDF; got:\n%s", want, text)
		}
	}
}

func TestRenderPDF_AllLineDescriptionsAcrossPages(t *testing.T) {
	// Every line description must appear somewhere in the
	// extracted text — losing lines silently during pagination
	// would be the worst-possible regression for a billing
	// renderer.
	lines := makeLines(75) // 3 pages: 20 + 42 + 13
	inv, err := New(Invoice{
		Number:   "FYR-3PAGE",
		Currency: "USD",
		Lines:    lines,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text, err := pdftext.Extract(pdf)
	if err != nil {
		t.Fatalf("pdftext.Extract: %v", err)
	}
	// Spot-check the first, middle, and last descriptions.
	for _, want := range []string{"line item 1", "line item 40", "line item 75"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in 3-page PDF; got truncated text", want)
		}
	}
	// And the page footer should report 3 pages.
	if !strings.Contains(text, "Page 1 of 3") || !strings.Contains(text, "Page 3 of 3") {
		t.Errorf("expected 'Page 1 of 3' and 'Page 3 of 3' footers; got:\n%s", text)
	}
}

func TestRenderPDF_SinglePageOmitsPageFooter(t *testing.T) {
	// "Page 1 of 1" is visual noise on the common single-page
	// invoice. Suppress it.
	inv := fixtureInvoice(t)
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text, err := pdftext.Extract(pdf)
	if err != nil {
		t.Fatalf("pdftext.Extract: %v", err)
	}
	if strings.Contains(text, "Page 1 of") {
		t.Errorf("single-page invoice should suppress page footer; got:\n%s", text)
	}
}

// ---- ToUnicode CMap + WinAnsi encoding ----

func TestRenderPDF_DeclaresWinAnsiEncodingOnBothFonts(t *testing.T) {
	// Without an explicit /Encoding entry, Type1 base fonts
	// fall back to StandardEncoding whose 0x80-0x9F range is
	// undefined — non-ASCII content like the euro sign or
	// curly quotes would render as wrong glyphs. Pin the
	// explicit declaration.
	inv := fixtureInvoice(t)
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	for _, want := range []string{
		"/BaseFont /Helvetica /Encoding /WinAnsiEncoding",
		"/BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding",
	} {
		if !bytes.Contains(pdf, []byte(want)) {
			t.Errorf("expected %q in output", want)
		}
	}
}

func TestRenderPDF_WiresToUnicodeIntoBothFonts(t *testing.T) {
	inv := fixtureInvoice(t)
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	// Both fonts must reference the SAME CMap object — sharing
	// is the whole point of the v0 layout (one stream, two
	// references).
	if bytes.Count(pdf, []byte("/ToUnicode 5 0 R")) != 2 {
		t.Errorf("expected /ToUnicode 5 0 R referenced from F1 + F2; got %d occurrences",
			bytes.Count(pdf, []byte("/ToUnicode 5 0 R")))
	}
	// The CMap stream itself must exist as object 5.
	if !bytes.Contains(pdf, []byte("5 0 obj\n<< /Length ")) {
		t.Error("ToUnicode CMap object 5 missing or malformed")
	}
	if !bytes.Contains(pdf, []byte("/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >>")) {
		t.Error("CMap stream missing required CIDSystemInfo dict")
	}
	if !bytes.Contains(pdf, []byte("beginbfchar")) || !bytes.Contains(pdf, []byte("endbfchar")) {
		t.Error("CMap stream missing bfchar block")
	}
}

func TestRenderPDF_PageReferencesPickUpRenumberedObjects(t *testing.T) {
	// Inserting the CMap at obj 5 shifted every page + content
	// stream by 1 (pages now start at obj 6). Verify the
	// /Kids list + Page→Contents refs use the new numbering;
	// a regression here would yield a PDF that opens but
	// renders blank pages.
	inv := fixtureInvoice(t)
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	// First page object now at 6 (was 5).
	if !bytes.Contains(pdf, []byte("/Kids [6 0 R")) {
		t.Errorf("/Kids list does not start at obj 6 (post-CMap insertion)")
	}
	if !bytes.Contains(pdf, []byte("6 0 obj\n<< /Type /Page")) {
		t.Error("page-1 object missing at obj 6")
	}
	if !bytes.Contains(pdf, []byte("/Contents 7 0 R")) {
		t.Error("page-1 contents ref not at obj 7")
	}
}

func TestRenderPDF_StillRoundTripsAfterCMapAddition(t *testing.T) {
	// The strongest regression guard: shared/pdftext (which
	// uses pdfcpu) must still be able to open + read the
	// PDF. A malformed CMap or broken renumbering would
	// break this even if the magic-bytes test passes.
	inv := fixtureInvoice(t)
	pdf, err := inv.RenderPDF()
	if err != nil {
		t.Fatalf("RenderPDF: %v", err)
	}
	text, err := pdftext.Extract(pdf)
	if err != nil {
		t.Fatalf("pdftext.Extract: %v", err)
	}
	if !strings.Contains(text, "FYR-2026-0001") {
		t.Error("invoice number missing from extracted text after CMap addition")
	}
}

func TestWinAnsiToUnicode_HappyPathRanges(t *testing.T) {
	cases := []struct {
		code byte
		want rune
	}{
		// ASCII identity.
		{0x20, 0x20}, // space
		{0x41, 0x41}, // A
		{0x7E, 0x7E}, // ~
		// Latin-1 identity (0xA0-0xFF).
		{0xA0, 0xA0},
		{0xA3, 0xA3}, // £
		{0xFF, 0xFF},
		// WinAnsi-specific glyphs (0x80-0x9F).
		{0x80, 0x20AC}, // €
		{0x95, 0x2022}, // •
		{0x91, 0x2018}, // '
		{0x92, 0x2019}, // '
		{0x96, 0x2013}, // –
		{0x97, 0x2014}, // —
	}
	for _, c := range cases {
		got := winAnsiToUnicode(c.code)
		if got != c.want {
			t.Errorf("winAnsiToUnicode(0x%02X) = U+%04X, want U+%04X", c.code, got, c.want)
		}
	}
}

func TestWinAnsiToUnicode_UnmappedCodesReturnNegativeOne(t *testing.T) {
	// Control characters (0x00-0x1F), DEL (0x7F), and the four
	// undefined slots in 0x80-0x9F. These have no glyph and
	// must NOT appear in the CMap.
	for _, code := range []byte{0x00, 0x1F, 0x7F, 0x81, 0x8D, 0x8F, 0x90, 0x9D} {
		if got := winAnsiToUnicode(code); got != -1 {
			t.Errorf("winAnsiToUnicode(0x%02X) = U+%04X, want -1 (unmapped)", code, got)
		}
	}
}

func TestBuildWinAnsiToUnicodeCMap_HasRequiredHeaderAndCodespace(t *testing.T) {
	got := buildWinAnsiToUnicodeCMap()
	for _, want := range []string{
		"/CIDInit /ProcSet findresource begin",
		"begincmap",
		"/Registry (Adobe) /Ordering (UCS) /Supplement 0",
		"/CMapName /Adobe-Identity-UCS def",
		"/CMapType 2 def",
		"1 begincodespacerange",
		"<00> <FF>",
		"endcodespacerange",
		"endcmap",
		"CMapName currentdict /CMap defineresource pop",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CMap missing required directive %q", want)
		}
	}
}

func TestBuildWinAnsiToUnicodeCMap_PinsKnownMappings(t *testing.T) {
	got := buildWinAnsiToUnicodeCMap()
	// Spot-check a few mappings — euro, em-dash, curly quote,
	// pound sterling.
	for _, want := range []string{
		"<80> <20AC>", // €
		"<97> <2014>", // —
		"<92> <2019>", // ' (right curly quote)
		"<A3> <00A3>", // £
		"<41> <0041>", // A — identity in ASCII
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CMap missing mapping %q", want)
		}
	}
}

func TestBuildWinAnsiToUnicodeCMap_SplitsIntoBfcharBlocks(t *testing.T) {
	// PDF caps bfchar blocks at 100 entries each. The full
	// WinAnsi mapping has > 200 entries so we MUST emit
	// multiple blocks. Counting beginbfchar markers verifies
	// the splitter ran.
	got := buildWinAnsiToUnicodeCMap()
	beginCount := strings.Count(got, "beginbfchar")
	endCount := strings.Count(got, "endbfchar")
	if beginCount != endCount {
		t.Errorf("unbalanced bfchar blocks: %d begin vs %d end", beginCount, endCount)
	}
	if beginCount < 2 {
		t.Errorf("expected >= 2 bfchar blocks (WinAnsi > 100 entries); got %d", beginCount)
	}
}

func TestSplitCMapEntries_RespectsPerBlockLimit(t *testing.T) {
	// 250 fake entries → with perBlock=100 we expect 3 blocks
	// of (100, 100, 50).
	var entries strings.Builder
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&entries, "<%02X> <%04X>\n", i, i)
	}
	got := splitCMapEntries(entries.String(), 100)
	if len(got) != 3 {
		t.Fatalf("got %d blocks, want 3", len(got))
	}
	if strings.Count(got[0], "\n") != 100 || strings.Count(got[1], "\n") != 100 {
		t.Errorf("first two blocks should have 100 lines each; got %d / %d",
			strings.Count(got[0], "\n"), strings.Count(got[1], "\n"))
	}
	if strings.Count(got[2], "\n") != 50 {
		t.Errorf("third block should have 50 lines; got %d", strings.Count(got[2], "\n"))
	}
}

func TestSplitCMapEntries_EmptyInputReturnsNil(t *testing.T) {
	if got := splitCMapEntries("", 100); got != nil {
		t.Errorf("empty input: got %v, want nil", got)
	}
}
