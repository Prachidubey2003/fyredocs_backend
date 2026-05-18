package invoice

import (
	"bytes"
	"fmt"
	"strings"
)

// RenderPDF returns a US-Letter PDF rendering of the invoice.
// Output is pure-stdlib synthesis — no font embedding, no
// external library — so the binary stays small and the
// renderer has no extra runtime surface to harden.
//
// Layout (US Letter, 612×792 pt, origin bottom-left):
//
//	page 1 of N (full header):
//	┌──────────────────────────────────────────────────────────┐
//	│ INVOICE (24pt bold)              Number: FYR-2026-0001   │
//	│ Fyredocs Inc.                    Issued: 2026-05-17      │
//	│                                  Due:    2026-06-16      │
//	│ From:                            To:                     │
//	│   <issuer block>                   <customer block>      │
//	│ ─────────────────────────────────────────────────────────│
//	│ Description                Qty    Unit       Amount      │
//	│ ─────────────────────────────────────────────────────────│
//	│ <line items, up to page-1 capacity>                      │
//	│                                                          │
//	│         Subtotal/Tax/Total (only on the last page)       │
//	│         Memo (only on the last page)                     │
//	│                                       Page 1 of N        │
//	└──────────────────────────────────────────────────────────┘
//
//	page k>1 (continuation header):
//	┌──────────────────────────────────────────────────────────┐
//	│ INVOICE FYR-2026-0001 (continued)                        │
//	│ ─────────────────────────────────────────────────────────│
//	│ Description                Qty    Unit       Amount      │
//	│ ─────────────────────────────────────────────────────────│
//	│ <line items>                                             │
//	│                                                          │
//	│         (totals/memo if this is the last page)           │
//	│                                       Page k of N        │
//	└──────────────────────────────────────────────────────────┘
//
// Constraints + tradeoffs:
//   - Paginated. ≤ pdfMaxTotalLines line items render in full
//     across N pages. Beyond that hard cap the renderer
//     returns `ErrLineItemsTruncated` alongside still-valid
//     PDF bytes that include a "... N more line(s) omitted"
//     notice — defends against an adversarial 100k-line
//     invoice OOMing the byte buffer.
//   - Standard PDF Type1 fonts only (Helvetica + Helvetica-Bold).
//     Non-ASCII line text passes through verbatim — PDF
//     readers will render WinAnsi-mapped bytes; emoji and CJK
//     won't display correctly. Acceptable for a v0 billing
//     invoice; embedded-font support is a separate work item.
//   - Output bytes are deterministic for fixed inputs (no
//     timestamps, no random IDs, no map iteration) so callers
//     can golden-test the PDF output if they want.
func (inv Invoice) RenderPDF() ([]byte, error) {
	if strings.TrimSpace(inv.Number) == "" {
		return nil, ErrEmptyNumber
	}
	if strings.TrimSpace(inv.Currency) == "" {
		return nil, ErrEmptyCurrency
	}

	lines := inv.Lines
	truncated := false
	omitted := 0
	if len(lines) > pdfMaxTotalLines {
		omitted = len(lines) - pdfMaxTotalLines
		lines = lines[:pdfMaxTotalLines]
		truncated = true
	}

	pages := paginate(lines)
	streams := make([]string, len(pages))
	for i, p := range pages {
		isFirst := i == 0
		isLast := i == len(pages)-1
		extraNotice := ""
		if isLast && truncated {
			extraNotice = fmt.Sprintf("... %d more line(s) omitted — see HTML / API for the full breakdown.", omitted)
		}
		streams[i] = buildPageContent(inv, p, i+1, len(pages), isFirst, isLast, extraNotice)
	}

	pdf := assemblePDF(streams)
	if truncated {
		return pdf, ErrLineItemsTruncated
	}
	return pdf, nil
}

// Pagination capacities. Per-page row counts are tuned so a
// page never bleeds into the footer band; bumping them
// requires re-checking the geometry below.
const (
	// pdfLinesPerFirstPage is the body-row budget for page 1
	// when there are MORE pages. Lower than page 1 standalone
	// because the totals/memo aren't on this page — we don't
	// need to reserve their space, but the full header band
	// still takes ~180pt.
	pdfLinesPerFirstPage = 20

	// pdfLinesPerInteriorPage is the budget for pages 2..N-1
	// (continuation header, no totals).
	pdfLinesPerInteriorPage = 42

	// pdfLinesPerLastPage is the budget for the LAST page when
	// there are multiple pages (continuation header + totals
	// + optional memo). Smaller than interior pages because
	// the totals/memo eat the bottom ~160pt.
	pdfLinesPerLastPage = 30

	// pdfLinesPerSinglePage is the budget when the whole
	// invoice fits on ONE page (full header + body + totals
	// + memo).
	pdfLinesPerSinglePage = 22

	// pdfMaxTotalLines is the hard cap before truncation. A
	// 500-line invoice is already absurd; this guards against
	// a runaway caller submitting a 1M-row "invoice" that
	// would balloon the PDF byte buffer.
	pdfMaxTotalLines = 500
)

// ErrLineItemsTruncated is returned alongside the PDF bytes
// when an invoice had more than pdfMaxTotalLines lines. The
// PDF is still valid + renders the kept lines + a "N more
// omitted" notice; the error signals to the caller that the
// rendering was lossy so they can route differently (e.g.,
// split into multiple invoices, or fall back to the HTML
// renderer).
var ErrLineItemsTruncated = errStr("invoice: more line items than the per-document cap — PDF truncated")

type errStr string

func (e errStr) Error() string { return string(e) }

// PDF coordinate system (US Letter):
const (
	pageW = 612 // points
	pageH = 792
	marg  = 50 // left/right margin
	topY  = 760
	footY = 40

	colDescX = 50
	colQtyX  = 340
	colUnitX = 420
	colAmtX  = 520 // right-aligned
)

// paginate chunks `lines` into per-page slices honouring the
// per-page capacities. Pure function so the caller can also
// reason about pagination decisions in tests.
//
// Single-page short-circuit: if the whole invoice fits in
// pdfLinesPerSinglePage rows we emit one page (rather than
// "page 1 of 1" with a continuation-header page 2). This is
// the common subscription-invoice case.
func paginate(lines []LineItem) [][]LineItem {
	if len(lines) <= pdfLinesPerSinglePage {
		return [][]LineItem{lines}
	}
	out := [][]LineItem{lines[:pdfLinesPerFirstPage]}
	rem := lines[pdfLinesPerFirstPage:]

	// Fill interior pages greedily while there are MORE rows
	// remaining than fit on one interior page. After this loop
	// `rem` is in [1, pdfLinesPerInteriorPage].
	for len(rem) > pdfLinesPerInteriorPage {
		out = append(out, rem[:pdfLinesPerInteriorPage])
		rem = rem[pdfLinesPerInteriorPage:]
	}

	switch {
	case len(rem) > pdfLinesPerLastPage:
		// Remainder is too big to coexist with totals/memo on
		// a single last page. Emit one more "interior-style"
		// page for the rows, then a dedicated totals-only
		// last page. Predictable + simple; the cost is one
		// extra page in a narrow remainder band.
		out = append(out, rem)
		out = append(out, []LineItem{})
	case len(rem) > 0:
		out = append(out, rem)
	}
	return out
}

// buildPageContent produces ONE page's content stream.
//
//	isFirst   — emit the full header band (INVOICE + metadata + parties)
//	isLast    — emit totals + memo at the bottom
//	extraNotice — non-empty when the document was truncated;
//	              rendered as a small final row.
func buildPageContent(inv Invoice, lines []LineItem, pageNum, totalPages int, isFirst, isLast bool, extraNotice string) string {
	var b strings.Builder
	b.Grow(4096)

	var tableTop int
	if isFirst {
		writeFullHeader(&b, inv)
		// Full header consumes the top of the page; the table
		// starts well below.
		tableTop = topY - 180
	} else {
		// Continuation header. One line, then the table.
		writeContinuationHeader(&b, inv)
		tableTop = topY - 40
	}

	// Table header row (every page).
	textAt(&b, "F2", 10, colDescX, tableTop, "Description")
	textAt(&b, "F2", 10, colQtyX, tableTop, "Qty")
	textAt(&b, "F2", 10, colUnitX, tableTop, "Unit")
	textAtRight(&b, "F2", 10, colAmtX, tableTop, "Amount")
	hline(&b, marg, tableTop-4, pageW-marg)

	// Body rows.
	rowY := tableTop - 18
	for _, l := range lines {
		textAt(&b, "F1", 10, colDescX, rowY, truncateForCol(l.Description, 44))
		textAt(&b, "F1", 10, colQtyX, rowY, fmt.Sprintf("%d", l.Quantity))
		textAt(&b, "F1", 10, colUnitX, rowY, FormatMoneyCents(l.UnitPriceCents, inv.Currency))
		textAtRight(&b, "F1", 10, colAmtX, rowY, FormatMoneyCents(l.LineTotalCents, inv.Currency))
		rowY -= 14
	}
	if isLast && extraNotice != "" {
		textAt(&b, "F1", 9, colDescX, rowY, extraNotice)
		rowY -= 14
	}
	hline(&b, marg, rowY+10, pageW-marg)

	if isLast {
		writeTotals(&b, inv, rowY-6)
		if memo := strings.TrimSpace(inv.Memo); memo != "" {
			writeMemo(&b, memo)
		}
	}

	// Page footer (every page).
	writePageFooter(&b, inv, pageNum, totalPages)

	return b.String()
}

func writeFullHeader(b *strings.Builder, inv Invoice) {
	textAt(b, "F2", 24, marg, topY, "INVOICE")
	textAt(b, "F1", 10, marg, topY-26, inv.Issuer.Name)

	rightY := topY
	textAtRight(b, "F1", 10, pageW-marg, rightY, "Number: "+inv.Number)
	rightY -= 14
	if inv.IssuedAt != "" {
		textAtRight(b, "F1", 10, pageW-marg, rightY, "Issued: "+inv.IssuedAt)
		rightY -= 14
	}
	if inv.DueAt != "" {
		textAtRight(b, "F1", 10, pageW-marg, rightY, "Due:    "+inv.DueAt)
	}

	blockY := topY - 80
	textAt(b, "F2", 11, marg, blockY, "From:")
	writePartyPDF(b, marg, blockY-14, inv.Issuer)
	textAt(b, "F2", 11, marg+260, blockY, "To:")
	writePartyPDF(b, marg+260, blockY-14, inv.Customer)
}

func writeContinuationHeader(b *strings.Builder, inv Invoice) {
	textAt(b, "F2", 12, marg, topY, fmt.Sprintf("Invoice %s (continued)", inv.Number))
}

func writeTotals(b *strings.Builder, inv Invoice, startY int) {
	labelX := colUnitX
	amtX := colAmtX
	y := startY
	textAt(b, "F1", 10, labelX, y, "Subtotal")
	textAtRight(b, "F1", 10, amtX, y, FormatMoneyCents(inv.SubtotalCents, inv.Currency))
	if inv.TaxCents != 0 {
		y -= 14
		textAt(b, "F1", 10, labelX, y, "Tax")
		textAtRight(b, "F1", 10, amtX, y, FormatMoneyCents(inv.TaxCents, inv.Currency))
	}
	y -= 16
	textAt(b, "F2", 11, labelX, y, "Total")
	textAtRight(b, "F2", 11, amtX, y, FormatMoneyCents(inv.TotalCents, inv.Currency))
}

func writeMemo(b *strings.Builder, memo string) {
	memoY := footY + 60
	textAt(b, "F2", 10, marg, memoY, "Memo")
	for i, line := range splitLines(memo, 5) {
		textAt(b, "F1", 10, marg, memoY-14*(i+1), line)
	}
}

func writePageFooter(b *strings.Builder, inv Invoice, pageNum, totalPages int) {
	if totalPages <= 1 {
		// Suppress "Page 1 of 1" — visually noisy on single-page
		// invoices and the common case wants a clean footer.
		return
	}
	textAtRight(b, "F1", 9, pageW-marg, footY-10,
		fmt.Sprintf("Page %d of %d  •  Invoice %s", pageNum, totalPages, inv.Number))
}

// textAt emits PDF operators that set font + size, jump to
// (x, y), and write `s`. Coords are user-space (origin
// bottom-left). The caller handles their own line spacing.
func textAt(b *strings.Builder, font string, size int, x, y int, s string) {
	if s == "" {
		return
	}
	fmt.Fprintf(b, "BT /%s %d Tf %d %d Td (%s) Tj ET\n",
		font, size, x, y, escapeLiteral(s))
}

// textAtRight emits text whose RIGHT edge sits at `rightX`.
// Approximates string width using Helvetica WinAnsi metrics
// (avgCharWidth × glyph count); slightly off for thin/wide
// glyphs but the totals column doesn't need sub-pixel
// alignment.
func textAtRight(b *strings.Builder, font string, size int, rightX, y int, s string) {
	if s == "" {
		return
	}
	w := approxWidth(s, font, size)
	textAt(b, font, size, rightX-w, y, s)
}

// hline draws a horizontal rule from (x1, y) to (x2, y).
func hline(b *strings.Builder, x1, y, x2 int) {
	fmt.Fprintf(b, "%d %d m %d %d l S\n", x1, y, x2, y)
}

// approxWidth is Helvetica avg-glyph-width × size × runes.
// A correct implementation would consult AFM tables per
// glyph; this is good enough for column alignment of small
// strings (numbers, currency codes).
func approxWidth(s, font string, size int) int {
	avgPerEm := 0.5
	if font == "F2" {
		avgPerEm = 0.55
	}
	return int(float64(size) * avgPerEm * float64(len([]rune(s))))
}

// truncateForCol clamps a long description to fit within the
// fixed-width Description column. PDF columns can't reflow
// without re-layout; truncation with an ellipsis is the
// simplest "won't crash the rendering" behaviour for v0.
func truncateForCol(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// splitLines breaks a memo into at most `max` lines of ≤ 80
// runes each. Honours embedded `\n`. Designed to keep memos
// inside the page footer; the cap prevents a 30-line memo
// from running off the page in v0.
func splitLines(s string, max int) []string {
	raw := strings.Split(s, "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		r := []rune(l)
		for len(r) > 80 {
			out = append(out, string(r[:80]))
			r = r[80:]
		}
		out = append(out, string(r))
		if len(out) >= max {
			return out[:max]
		}
	}
	return out
}

// writePartyPDF emits a party block (name + email + address +
// tax id) starting at (x, y) and descending by 12pt per line.
func writePartyPDF(b *strings.Builder, x, y int, p Party) {
	lineY := y
	if p.Name != "" {
		textAt(b, "F1", 10, x, lineY, p.Name)
		lineY -= 12
	}
	if p.Email != "" {
		textAt(b, "F1", 10, x, lineY, p.Email)
		lineY -= 12
	}
	if p.AddressLine != "" {
		for _, l := range strings.Split(p.AddressLine, "\n") {
			textAt(b, "F1", 10, x, lineY, l)
			lineY -= 12
		}
	}
	if p.TaxID != "" {
		textAt(b, "F1", 10, x, lineY, "Tax ID: "+p.TaxID)
	}
}

// escapeLiteral escapes a Go string for embedding inside a
// PDF literal `(...)`. Per ISO 32000-1 § 7.3.4.2 we MUST
// escape `\`, `(`, and `)`. Other bytes (including high
// bytes for WinAnsi-mapped accented chars) pass through.
func escapeLiteral(s string) string {
	if !strings.ContainsAny(s, `\()`) && !strings.ContainsAny(s, "\r\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '(':
			b.WriteString(`\(`)
		case ')':
			b.WriteString(`\)`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// assemblePDF builds a complete classic-xref PDF for `streams`,
// one content stream per page. Object layout:
//
//	1: Catalog → /Pages 2 0 R
//	2: Pages   → /Kids [6 0 R 8 0 R …] /Count N
//	3: Font F1 (Helvetica, WinAnsi, /ToUnicode 5 0 R)
//	4: Font F2 (Helvetica-Bold, WinAnsi, /ToUnicode 5 0 R)
//	5: ToUnicode CMap (shared by F1 + F2 — same encoding
//	   means same code→Unicode mapping; one stream is
//	   cheaper than two identical copies)
//	6, 8, 10, …: Page objects (alternating with Contents)
//	7, 9, 11, …: Contents streams
//
// Fonts at fixed object numbers (3, 4) and the ToUnicode CMap
// at fixed object number 5 so every Page can reference them
// identically.
//
// Both fonts declare `/Encoding /WinAnsiEncoding` explicitly
// rather than relying on PDF's implicit StandardEncoding for
// Type1 base fonts. The reason: StandardEncoding's 0x80-0x9F
// range is undefined, so non-ASCII content like "€2.50" or
// "café" would render as the wrong glyphs (or no glyphs) in
// strict readers. Declaring WinAnsiEncoding pins the 0x80-0x9F
// → Unicode mapping that the renderer's content streams
// already assume.
//
// The `/ToUnicode` CMap covers the WinAnsi range and lets the
// PDF reader recover Unicode codepoints from rendered glyph
// codes — required for correct copy/paste, text search,
// accessibility tools, and PDF/A conformance. Subset-embedded
// TTF/CFF font support (when invoices need glyphs outside
// WinAnsi — CJK, emoji, etc.) is tracked as a separate
// follow-up.
func assemblePDF(streams []string) []byte {
	n := len(streams)
	parts := make([]string, 0, 5+2*n)

	// Page objects start at obj 6 now (was 5) because the
	// ToUnicode CMap takes obj 5. Update the /Kids list.
	var kids strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			kids.WriteByte(' ')
		}
		fmt.Fprintf(&kids, "%d 0 R", 6+2*i)
	}

	cmap := buildWinAnsiToUnicodeCMap()

	parts = append(parts,
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		fmt.Sprintf("2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n", kids.String(), n),
		"3 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding /ToUnicode 5 0 R >>\nendobj\n",
		"4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding /ToUnicode 5 0 R >>\nendobj\n",
		fmt.Sprintf(
			"5 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
			len(cmap), cmap,
		),
	)

	for i, content := range streams {
		pageNum := 6 + 2*i
		contentsNum := 7 + 2*i
		parts = append(parts,
			fmt.Sprintf(
				"%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] "+
					"/Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> "+
					"/Contents %d 0 R >>\nendobj\n",
				pageNum, pageW, pageH, contentsNum,
			),
			fmt.Sprintf(
				"%d 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
				contentsNum, len(content), content,
			),
		)
	}

	header := "%PDF-1.4\n"
	offsets := make([]int, 0, len(parts))
	pos := len(header)
	for _, p := range parts {
		offsets = append(offsets, pos)
		pos += len(p)
	}
	var b bytes.Buffer
	b.Grow(pos + 256)
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
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(parts)+1, xrefOff)
	return b.Bytes()
}
