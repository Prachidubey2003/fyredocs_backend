package pdfedit_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"editor-service/internal/corpus"
	"editor-service/internal/pdfedit"
	"editor-service/internal/spdom"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// minimalPDF is a thin alias over [corpus.Minimal] so the
// pdfedit tests reference the same hand-crafted single-page
// fixture every other editor test uses. Kept as a local helper
// (rather than s/minimalPDF/corpus.Minimal/g) so the diff is
// small and the call sites read the same as before the corpus
// package landed.
func minimalPDF() []byte { return corpus.Minimal() }

func TestRotatePage_PreservesOriginalBytes(t *testing.T) {
	pdf := minimalPDF()
	out, err := pdfedit.RotatePage(pdf, 1, 90)
	if err != nil {
		t.Fatalf("RotatePage: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("rotated revision must start with the original bytes verbatim")
	}
	if len(out) <= len(pdf) {
		t.Error("rotated revision should be longer than the original (appended section)")
	}
}

func TestRotatePage_WritesRotateEntry(t *testing.T) {
	pdf := minimalPDF()
	for _, deg := range []int{0, 90, 180, 270} {
		out, err := pdfedit.RotatePage(pdf, 1, deg)
		if err != nil {
			t.Fatalf("RotatePage(%d): %v", deg, err)
		}
		appendix := string(out[len(pdf):])
		want := fmt.Sprintf("/Rotate %d", deg)
		if !strings.Contains(appendix, want) {
			t.Errorf("appendix missing %q; got:\n%s", want, appendix)
		}
	}
}

func TestRotatePage_RoundTripsThroughPdfcpu(t *testing.T) {
	pdf := minimalPDF()
	out, err := pdfedit.RotatePage(pdf, 1, 90)
	if err != nil {
		t.Fatalf("RotatePage: %v", err)
	}
	// The rotated revision must be readable by pdfcpu — the strongest
	// signal we emit conforming PDF bytes.
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("pdfcpu rejected the rotated revision: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	if ctx.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1 (rotation should not duplicate or drop pages)", ctx.PageCount)
	}
	// Confirm pdfcpu reports the rotation we asked for.
	dict, _, _, err := ctx.PageDict(1, false)
	if err != nil {
		t.Fatalf("PageDict: %v", err)
	}
	got := dict.IntEntry("Rotate")
	if got == nil {
		t.Fatal("page 1 has no /Rotate after RotatePage")
	}
	if *got != 90 {
		t.Errorf("/Rotate = %d, want 90", *got)
	}
}

func TestRotatePage_RoundTripsThroughSPDOM(t *testing.T) {
	// The L1 sPDOM parser must also accept the rotated revision (it's
	// the same underlying pdfcpu read, but exercises our pipeline end
	// to end).
	pdf := minimalPDF()
	out, err := pdfedit.RotatePage(pdf, 1, 180)
	if err != nil {
		t.Fatalf("RotatePage: %v", err)
	}
	doc, err := spdom.Parse("doc-rot", bytes.NewReader(out))
	if err != nil {
		t.Fatalf("spdom Parse: %v", err)
	}
	if doc.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1", doc.PageCount)
	}
}

func TestRotatePage_RejectsBadRotation(t *testing.T) {
	pdf := minimalPDF()
	bad := []int{1, -1, -90, 45, 360, 450}
	for _, deg := range bad {
		if _, err := pdfedit.RotatePage(pdf, 1, deg); err == nil {
			t.Errorf("RotatePage(_, _, %d) should have errored", deg)
		}
	}
}

func TestRotatePage_RejectsOutOfRangePage(t *testing.T) {
	pdf := minimalPDF()
	for _, n := range []int{0, -1, 2, 100} {
		if _, err := pdfedit.RotatePage(pdf, n, 90); err == nil {
			t.Errorf("RotatePage(_, %d, _) should have errored", n)
		}
	}
}

func TestRotatePage_RejectsNonPDFInput(t *testing.T) {
	if _, err := pdfedit.RotatePage([]byte("not a pdf at all"), 1, 90); err == nil {
		t.Fatal("expected error for non-PDF input")
	}
}

func TestRotatePage_DoubleRotateChainsRevisions(t *testing.T) {
	// Rotating twice should produce a doc whose final /Rotate is the
	// second value, with both revisions preserved in the byte stream
	// (the original revision + the first incremental section + the
	// second incremental section).
	pdf := minimalPDF()
	rev1, err := pdfedit.RotatePage(pdf, 1, 90)
	if err != nil {
		t.Fatalf("rev1: %v", err)
	}
	rev2, err := pdfedit.RotatePage(rev1, 1, 180)
	if err != nil {
		t.Fatalf("rev2: %v", err)
	}
	if !bytes.HasPrefix(rev2, rev1) {
		t.Error("rev2 must start with rev1's bytes (incremental update preserves prior content)")
	}
	// pdfcpu should resolve the final rotation (180).
	ctx, err := api.ReadContext(bytes.NewReader(rev2), nil)
	if err != nil {
		t.Fatalf("ReadContext rev2: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	dict, _, _, err := ctx.PageDict(1, false)
	if err != nil {
		t.Fatalf("PageDict: %v", err)
	}
	got := dict.IntEntry("Rotate")
	if got == nil || *got != 180 {
		t.Errorf("final /Rotate = %v, want 180", got)
	}
}

func TestRotatePageTo_StreamsToWriter(t *testing.T) {
	pdf := minimalPDF()
	var buf bytes.Buffer
	if err := pdfedit.RotatePageTo(&buf, pdf, 1, 270); err != nil {
		t.Fatalf("RotatePageTo: %v", err)
	}
	if buf.Len() <= len(pdf) {
		t.Error("RotatePageTo should write at least the original + the appended section")
	}
	if !bytes.HasPrefix(buf.Bytes(), pdf) {
		t.Error("streamed output must begin with the original bytes")
	}
}
