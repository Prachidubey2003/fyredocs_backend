package pdfwriter_test

import (
	"bytes"
	"fmt"
	"testing"

	"editor-service/internal/corpus"
	"editor-service/internal/pdfwriter"
	"editor-service/internal/spdom"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// minimalPDFExternal is a thin alias over [corpus.Minimal]. Kept
// as a function (rather than renaming every call site) so the
// diff is small.
func minimalPDFExternal() []byte { return corpus.Minimal() }

// TestUpdate_OutputStillParsableByPdfcpu is the heart-of-the-matter
// invariant: an incremental update produced by this package can be
// re-opened by an off-the-shelf parser (pdfcpu) and still resolves the
// right number of pages with the modifications applied. If this test
// passes, downstream readers (Acrobat, browser viewers, MuPDF) almost
// certainly accept the bytes too.
func TestUpdate_OutputStillParsableByPdfcpu(t *testing.T) {
	pdf := minimalPDFExternal()

	var u pdfwriter.Update
	u.Set(3, []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Rotate 90 >>"))
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}

	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("pdfcpu refused the appended PDF: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	if ctx.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1", ctx.PageCount)
	}
}

// TestUpdate_RoundtripViaSPDOM confirms the appended bytes can be fed
// into the same parser that drives sPDOM. We expect one page, geometry
// pass populated, no panics.
func TestUpdate_RoundtripViaSPDOM(t *testing.T) {
	pdf := minimalPDFExternal()
	var u pdfwriter.Update
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("empty update: %v", err)
	}
	doc, err := spdom.Parse("doc-rt", bytes.NewReader(out))
	if err != nil {
		t.Fatalf("spdom Parse on appended PDF: %v", err)
	}
	if doc.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1", doc.PageCount)
	}
	if len(doc.Pages) != 1 {
		t.Fatalf("len(Pages) = %d, want 1", len(doc.Pages))
	}
	if doc.Pages[0].MediaBox.Width() != 612 || doc.Pages[0].MediaBox.Height() != 792 {
		t.Errorf("MediaBox = %+v, want 612x792", doc.Pages[0].MediaBox)
	}
}

// TestUpdate_XrefStreamIncremental_RoundTripsThroughPdfcpu is the
// belt-and-braces version of the writer-internal xref-stream tests:
// the appended /Type /XRef object must not just look right under
// strings.Contains but actually parse through pdfcpu's full reader.
// Without this, a regression in the binary stream layout (wrong
// endianness, off-by-one /Length, wrong /Index packing) would slip
// past the unit tests.
func TestUpdate_XrefStreamIncremental_RoundTripsThroughPdfcpu(t *testing.T) {
	// Use a real PDF whose latest xref is a /Type /XRef stream
	// object. pdfcpu's CreateContext emits xref-stream output by
	// default for PDF 1.5+, but the simpler path is to use the
	// hand-rolled fixture in writer_test.go via the api round-trip
	// after pdfcpu rewrites it. Here we construct one inline:
	// catalog → pages → page → stream-form xref entry table.
	//
	// Build a tiny xref-stream PDF: same shape as minimalXrefStreamPDF
	// in the writer_test.go file, but with enough objects that an
	// incremental update has something meaningful to overlay.
	header := "%PDF-1.7\n"
	parts := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>\nendobj\n",
	}
	body := header
	for _, p := range parts {
		body += p
	}
	xrefObj := "4 0 obj\n<< /Type /XRef /Size 5 /Root 1 0 R /W [1 1 1] /Length 0 >>\nstream\nendstream\nendobj\n"
	startXrefOff := len(body)
	body += xrefObj
	body += fmt.Sprintf("startxref\n%d\n%%%%EOF\n", startXrefOff)
	pdf := []byte(body)

	// Now apply a real edit: rotate page 1 (object 3) by 90°.
	var u pdfwriter.Update
	u.Set(3, []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Rotate 90 >>"))
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}

	// pdfcpu must accept the result and resolve the page.
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("pdfcpu rejected xref-stream incremental output: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	if ctx.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1", ctx.PageCount)
	}

	// Spot-check that the rotation actually applied by re-reading
	// the page dict.
	dict, _, _, err := ctx.PageDict(1, false)
	if err != nil {
		t.Fatalf("PageDict: %v", err)
	}
	v, _ := dict.Find("Rotate")
	if v == nil {
		t.Error("/Rotate entry missing on rewritten page")
	}
}
