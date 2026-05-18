package spdom

import (
	"bytes"
	"strings"
	"testing"

	"editor-service/internal/corpus"
)

// minimalPDF is a thin alias over [corpus.Minimal]. The corpus
// fixture uses the same 1-page, 612×792 layout but is generated
// from a single source of truth shared with every other editor
// test, so a layout tweak (e.g. adding /Lang to the catalog)
// updates this test alongside the rest in one place.
func minimalPDF() []byte { return corpus.Minimal() }

func TestParse_Minimal(t *testing.T) {
	doc, err := Parse("doc-1", bytes.NewReader(minimalPDF()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc == nil {
		t.Fatal("doc is nil")
	}
	if doc.ID != "doc-1" {
		t.Errorf("doc.ID = %q, want %q", doc.ID, "doc-1")
	}
	if doc.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1", doc.PageCount)
	}
	if len(doc.Pages) != 1 {
		t.Fatalf("len(Pages) = %d, want 1", len(doc.Pages))
	}
	if doc.PDFVersion == "" {
		t.Errorf("PDFVersion should be populated")
	}
	page := doc.Pages[0]
	if page.Number != 1 {
		t.Errorf("page.Number = %d, want 1", page.Number)
	}
	if page.MediaBox.Width() != 612 {
		t.Errorf("page width = %v, want 612", page.MediaBox.Width())
	}
	if page.MediaBox.Height() != 792 {
		t.Errorf("page height = %v, want 792", page.MediaBox.Height())
	}
	if page.Blocks == nil {
		t.Errorf("page.Blocks should be non-nil (empty slice, not nil)")
	}
	// Minimal PDF has no /Contents — text pass produces nothing.
	if len(page.Blocks) != 0 {
		t.Errorf("page.Blocks = %v, want empty for content-less PDF", page.Blocks)
	}
	if page.LayoutPass != LayoutPassGeometry {
		t.Errorf("LayoutPass = %d, want %d (geometry only)", page.LayoutPass, LayoutPassGeometry)
	}
}

// pdfWithText returns a single-page PDF that has a /Contents stream
// containing a few text-show operators with literal Latin strings.
// pdfcpu's ReadContext should resolve /Contents and the L2.5 text pass
// should populate Page.Blocks with the extracted text.
func pdfWithText() []byte {
	// Hand-craft an object structure:
	//   1: /Catalog
	//   2: /Pages
	//   3: /Page (with /Contents 4 0 R, /MediaBox)
	//   4: stream containing text operators
	// We use a built-in PDF font (Helvetica) via /Resources /Font /F1.
	// xref offsets are computed from the byte positions of each object.
	const stream = "BT /F1 12 Tf 72 720 Td (Hello, sPDOM!) Tj 0 -16 Td (Second line) Tj ET"
	streamHeader := "<< /Length " + itoa(len(stream)) + " >>\nstream\n"
	streamFooter := "\nendstream"

	parts := []string{
		"%PDF-1.4\n",
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n",
		"4 0 obj\n" + streamHeader + stream + streamFooter + "\nendobj\n",
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	}

	// Compute offsets.
	offsets := []int{0} // object 0 (free)
	pos := 0
	for i := 1; i < len(parts); i++ {
		// `parts[0]` is the file header; objects start at index 1 (object 1).
		// We need each object's starting offset (the byte index of the
		// first byte of its "N 0 obj\n" line).
		_ = i // index in parts maps directly to object number
	}
	// Recompute properly:
	offsets = nil
	pos = 0
	// File header
	pos += len(parts[0])
	for i := 1; i < len(parts); i++ {
		offsets = append(offsets, pos)
		pos += len(parts[i])
	}

	xrefStart := pos
	var xref string
	xref += "xref\n0 6\n0000000000 65535 f \n"
	for _, off := range offsets {
		xref += pad10(off) + " 00000 n \n"
	}
	trailer := "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n" + itoa(xrefStart) + "\n%%EOF\n"

	body := strings.Join(parts, "") + xref + trailer
	return []byte(body)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func pad10(n int) string {
	s := itoa(n)
	if len(s) >= 10 {
		return s
	}
	return strings.Repeat("0", 10-len(s)) + s
}

func TestParse_WithText_PopulatesBlocks(t *testing.T) {
	doc, err := Parse("doc-text", bytes.NewReader(pdfWithText()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Pages) != 1 {
		t.Fatalf("len(Pages) = %d, want 1", len(doc.Pages))
	}
	page := doc.Pages[0]

	// The position-aware extractor (L3) successfully reads the hand-crafted
	// fixture: pure translation matrices, no rotation. So we should land at
	// LayoutPassFull, not LayoutPassText (the L2.5 fallback).
	if page.LayoutPass != LayoutPassFull {
		t.Errorf("LayoutPass = %d, want %d (full pass)", page.LayoutPass, LayoutPassFull)
	}
	if len(page.Blocks) != 1 {
		t.Fatalf("len(Blocks) = %d, want 1", len(page.Blocks))
	}
	block := page.Blocks[0]
	if block.Type != BlockText {
		t.Errorf("Block.Type = %q, want %q", block.Type, BlockText)
	}
	if len(block.Lines) != 2 {
		t.Fatalf("len(Lines) = %d, want 2 (Hello, sPDOM! + Second line)", len(block.Lines))
	}

	// Collect run text and assert both literals are present (order matters).
	gotTexts := []string{}
	for _, line := range block.Lines {
		for _, run := range line.Runs {
			gotTexts = append(gotTexts, run.Text)
		}
	}
	joined := strings.Join(gotTexts, " | ")
	for _, want := range []string{"Hello, sPDOM!", "Second line"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing expected text %q in extracted runs: %q", want, joined)
		}
	}

	// Stable IDs: block, line, run must each be set.
	if block.ID == "" || block.Lines[0].ID == "" || block.Lines[0].Runs[0].ID == "" {
		t.Errorf("block/line/run IDs must all be populated")
	}
}

func TestParse_NodeIDsStable(t *testing.T) {
	pdf := minimalPDF()
	d1, err := Parse("doc-stable", bytes.NewReader(pdf))
	if err != nil {
		t.Fatal(err)
	}
	d2, err := Parse("doc-stable", bytes.NewReader(pdf))
	if err != nil {
		t.Fatal(err)
	}
	if d1.Pages[0].ID != d2.Pages[0].ID {
		t.Errorf("page IDs diverged across parses: %q != %q",
			d1.Pages[0].ID, d2.Pages[0].ID)
	}
}

func TestParse_NodeIDsDifferAcrossDocIDs(t *testing.T) {
	pdf := minimalPDF()
	d1, _ := Parse("doc-a", bytes.NewReader(pdf))
	d2, _ := Parse("doc-b", bytes.NewReader(pdf))
	if d1.Pages[0].ID == d2.Pages[0].ID {
		t.Errorf("page IDs should differ across distinct documents; got identical %q", d1.Pages[0].ID)
	}
}

func TestParse_EmptyReader(t *testing.T) {
	_, err := Parse("doc-1", bytes.NewReader(nil))
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestParse_NilReader(t *testing.T) {
	_, err := Parse("doc-1", nil)
	if err == nil {
		t.Fatal("expected error for nil reader")
	}
}

func TestParse_EmptyDocID(t *testing.T) {
	_, err := Parse("", bytes.NewReader(minimalPDF()))
	if err == nil {
		t.Fatal("expected error for empty doc id")
	}
}

func TestParse_GarbageBytes(t *testing.T) {
	_, err := Parse("doc-1", bytes.NewReader([]byte("not a pdf at all")))
	if err == nil {
		t.Fatal("expected error for non-PDF input")
	}
}

func TestNodeID_DeterministicAndUnique(t *testing.T) {
	a := NodeID("parent", "page", 1)
	b := NodeID("parent", "page", 1)
	if a != b {
		t.Errorf("NodeID should be deterministic; got %q vs %q", a, b)
	}

	c := NodeID("parent", "page", 2)
	if a == c {
		t.Errorf("different index should yield different id; got identical %q", a)
	}

	d := NodeID("parent", "line", 1)
	if a == d {
		t.Errorf("different kind should yield different id; got identical %q", a)
	}
}

func TestRect_WidthHeight(t *testing.T) {
	r := Rect{X0: 10, Y0: 20, X1: 110, Y1: 220}
	if r.Width() != 100 {
		t.Errorf("Width = %v, want 100", r.Width())
	}
	if r.Height() != 200 {
		t.Errorf("Height = %v, want 200", r.Height())
	}
}
