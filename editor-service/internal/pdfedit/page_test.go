package pdfedit_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"editor-service/internal/pdfedit"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// threePagePDF returns a hand-crafted PDF with three letter-sized
// pages under a flat Pages tree. DeletePage's tests need to exercise
// the parent-rewrite path; the single-page minimalPDF fixture would
// always hit the "last page" guard.
func threePagePDF() []byte {
	parts := []string{
		"%PDF-1.4\n",
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>\nendobj\n",
		"4 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>\nendobj\n",
		"5 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>\nendobj\n",
	}
	offsets := make([]int, 0, len(parts)-1)
	pos := len(parts[0])
	for i := 1; i < len(parts); i++ {
		offsets = append(offsets, pos)
		pos += len(parts[i])
	}
	xrefStart := pos

	var buf bytes.Buffer
	for _, p := range parts {
		buf.WriteString(p)
	}
	buf.WriteString("xref\n0 6\n")
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	buf.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n")
	fmt.Fprintf(&buf, "%d\n%%%%EOF\n", xrefStart)
	return buf.Bytes()
}

func TestDeletePage_PreservesOriginalBytes(t *testing.T) {
	pdf := threePagePDF()
	out, err := pdfedit.DeletePage(pdf, 2)
	if err != nil {
		t.Fatalf("DeletePage: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("original bytes must be preserved verbatim")
	}
}

func TestDeletePage_RewritesParentAndDecrementsCount(t *testing.T) {
	pdf := threePagePDF()
	out, err := pdfedit.DeletePage(pdf, 2)
	if err != nil {
		t.Fatalf("DeletePage: %v", err)
	}
	appendix := string(out[len(pdf):])
	// Parent Pages node (obj 2) should be rewritten with /Count 2 and
	// the middle page (obj 4) removed from /Kids.
	if !strings.Contains(appendix, "2 0 obj\n") {
		t.Errorf("appendix should contain a rewritten `2 0 obj` header:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/Count 2") {
		t.Errorf("appendix should contain /Count 2 (was 3):\n%s", appendix)
	}
	if strings.Contains(appendix, "4 0 R") {
		t.Errorf("appendix should not reference the deleted page (obj 4):\n%s", appendix)
	}
	// Kids should still include 3 0 R and 5 0 R.
	if !strings.Contains(appendix, "3 0 R") || !strings.Contains(appendix, "5 0 R") {
		t.Errorf("appendix should retain the other pages in /Kids:\n%s", appendix)
	}
}

func TestDeletePage_PdfcpuReadsFewerPages(t *testing.T) {
	pdf := threePagePDF()
	out, err := pdfedit.DeletePage(pdf, 2)
	if err != nil {
		t.Fatalf("DeletePage: %v", err)
	}
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("pdfcpu rejected the post-delete PDF: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	if ctx.PageCount != 2 {
		t.Errorf("PageCount = %d, want 2", ctx.PageCount)
	}
}

func TestDeletePage_DeletingEachPosition(t *testing.T) {
	// Deleting the first, middle, or last page should all succeed and
	// leave a 2-page document.
	for _, n := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("page=%d", n), func(t *testing.T) {
			pdf := threePagePDF()
			out, err := pdfedit.DeletePage(pdf, n)
			if err != nil {
				t.Fatalf("DeletePage(%d): %v", n, err)
			}
			ctx, err := api.ReadContext(bytes.NewReader(out), nil)
			if err != nil {
				t.Fatalf("re-read: %v", err)
			}
			if err := ctx.EnsurePageCount(); err != nil {
				t.Fatalf("EnsurePageCount: %v", err)
			}
			if ctx.PageCount != 2 {
				t.Errorf("PageCount = %d, want 2 after deleting page %d", ctx.PageCount, n)
			}
		})
	}
}

func TestDeletePage_RefusesLastPage(t *testing.T) {
	// minimalPDF is a 1-page document; deleting page 1 should fail.
	pdf := minimalPDF()
	_, err := pdfedit.DeletePage(pdf, 1)
	if err == nil {
		t.Fatal("expected error when deleting the last remaining page")
	}
	if !strings.Contains(err.Error(), "last page") {
		t.Errorf("error should mention `last page`, got: %v", err)
	}
}

func TestDeletePage_RejectsBadPage(t *testing.T) {
	pdf := threePagePDF()
	for _, n := range []int{0, -1, 4, 99} {
		if _, err := pdfedit.DeletePage(pdf, n); err == nil {
			t.Errorf("DeletePage(_, %d) should have errored", n)
		}
	}
}

func TestDeletePage_RejectsNonPDF(t *testing.T) {
	if _, err := pdfedit.DeletePage([]byte("not a pdf"), 1); err == nil {
		t.Fatal("expected error for non-PDF input")
	}
}

func TestInsertBlankPage_PreservesOriginalBytes(t *testing.T) {
	pdf := threePagePDF()
	out, err := pdfedit.InsertBlankPage(pdf, 1)
	if err != nil {
		t.Fatalf("InsertBlankPage: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("original bytes must be preserved verbatim")
	}
}

func TestInsertBlankPage_BumpsPageCount(t *testing.T) {
	pdf := threePagePDF()
	for _, after := range []int{0, 1, 2, 3} {
		t.Run(fmt.Sprintf("after=%d", after), func(t *testing.T) {
			out, err := pdfedit.InsertBlankPage(pdf, after)
			if err != nil {
				t.Fatalf("InsertBlankPage(%d): %v", after, err)
			}
			ctx, err := api.ReadContext(bytes.NewReader(out), nil)
			if err != nil {
				t.Fatalf("re-read: %v", err)
			}
			if err := ctx.EnsurePageCount(); err != nil {
				t.Fatalf("EnsurePageCount: %v", err)
			}
			if ctx.PageCount != 4 {
				t.Errorf("PageCount = %d, want 4", ctx.PageCount)
			}
		})
	}
}

func TestInsertBlankPage_NewPageHasMediaBox(t *testing.T) {
	pdf := threePagePDF()
	out, err := pdfedit.InsertBlankPage(pdf, 1)
	if err != nil {
		t.Fatalf("InsertBlankPage: %v", err)
	}
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	// The new page sits at position 2 (after the original page 1).
	dict, _, _, err := ctx.PageDict(2, false)
	if err != nil {
		t.Fatalf("PageDict(2): %v", err)
	}
	if _, ok := dict.Find("MediaBox"); !ok {
		t.Error("inserted page should carry an explicit /MediaBox")
	}
}

func TestInsertBlankPage_InsertedAtEndAppearsLast(t *testing.T) {
	pdf := threePagePDF()
	out, err := pdfedit.InsertBlankPage(pdf, 3)
	if err != nil {
		t.Fatalf("InsertBlankPage(3): %v", err)
	}
	appendix := string(out[len(pdf):])
	// Find the rewritten parent obj 2 and assert the new ref is at
	// the END of /Kids.
	startObj := strings.Index(appendix, "2 0 obj\n")
	if startObj < 0 {
		t.Fatalf("appendix missing rewritten obj 2:\n%s", appendix)
	}
	kidsStart := strings.Index(appendix[startObj:], "/Kids")
	if kidsStart < 0 {
		t.Fatalf("appendix missing /Kids key:\n%s", appendix)
	}
	openBracket := strings.Index(appendix[startObj+kidsStart:], "[")
	if openBracket < 0 {
		t.Fatalf("appendix /Kids has no `[`:\n%s", appendix)
	}
	kidsStart = startObj + kidsStart + openBracket + 1
	kidsEnd := strings.Index(appendix[kidsStart:], "]")
	if kidsEnd < 0 {
		t.Fatalf("appendix /Kids array unterminated:\n%s", appendix)
	}
	kids := appendix[kidsStart : kidsStart+kidsEnd]
	// Expect the order to end with the new page's IndirectRef AFTER
	// 5 0 R (the original last page).
	idx5 := strings.Index(kids, "5 0 R")
	if idx5 < 0 {
		t.Fatalf("/Kids missing original page-3 ref `5 0 R`:\nkids=%q", kids)
	}
	// The new page's obj number is 6 (one past the original /Size=6).
	idxNew := strings.Index(kids, "6 0 R")
	if idxNew < 0 {
		t.Fatalf("/Kids missing new page ref `6 0 R`:\nkids=%q", kids)
	}
	if idxNew < idx5 {
		t.Errorf("new page ref should come after 5 0 R; got order [%q]", kids)
	}
}

func TestInsertBlankPage_InsertedAtStartAppearsFirst(t *testing.T) {
	pdf := threePagePDF()
	out, err := pdfedit.InsertBlankPage(pdf, 0)
	if err != nil {
		t.Fatalf("InsertBlankPage(0): %v", err)
	}
	appendix := string(out[len(pdf):])
	startObj := strings.Index(appendix, "2 0 obj\n")
	if startObj < 0 {
		t.Fatalf("appendix missing rewritten obj 2:\n%s", appendix)
	}
	kidsStart := strings.Index(appendix[startObj:], "/Kids")
	if kidsStart < 0 {
		t.Fatalf("appendix missing /Kids key:\n%s", appendix)
	}
	openBracket := strings.Index(appendix[startObj+kidsStart:], "[")
	if openBracket < 0 {
		t.Fatalf("appendix /Kids has no `[`:\n%s", appendix)
	}
	kidsStart = startObj + kidsStart + openBracket + 1
	kidsEnd := strings.Index(appendix[kidsStart:], "]")
	if kidsEnd < 0 {
		t.Fatalf("appendix /Kids array unterminated:\n%s", appendix)
	}
	kids := appendix[kidsStart : kidsStart+kidsEnd]
	idxNew := strings.Index(kids, "6 0 R")
	idx3 := strings.Index(kids, "3 0 R")
	if idxNew < 0 || idx3 < 0 {
		t.Fatalf("/Kids missing expected refs:\nkids=%q", kids)
	}
	if idxNew > idx3 {
		t.Errorf("new page ref should come before 3 0 R; got order [%q]", kids)
	}
}

func TestInsertBlankPage_RejectsOutOfRange(t *testing.T) {
	pdf := threePagePDF()
	for _, n := range []int{-1, 4, 99} {
		if _, err := pdfedit.InsertBlankPage(pdf, n); err == nil {
			t.Errorf("InsertBlankPage(_, %d) should have errored", n)
		}
	}
}

func TestInsertBlankPage_RejectsNonPDF(t *testing.T) {
	if _, err := pdfedit.InsertBlankPage([]byte("not a pdf"), 0); err == nil {
		t.Fatal("expected error for non-PDF input")
	}
}

func TestInsertBlankPage_ChainsWithDelete(t *testing.T) {
	// Insert a blank page after page 2, then delete the original
	// page 1. We should end up with a 3-page doc.
	pdf := threePagePDF()
	rev1, err := pdfedit.InsertBlankPage(pdf, 2)
	if err != nil {
		t.Fatalf("InsertBlankPage: %v", err)
	}
	rev2, err := pdfedit.DeletePage(rev1, 1)
	if err != nil {
		t.Fatalf("DeletePage after insert: %v", err)
	}
	if !bytes.HasPrefix(rev2, rev1) {
		t.Error("rev2 must start with rev1 (incremental chain)")
	}
	ctx, err := api.ReadContext(bytes.NewReader(rev2), nil)
	if err != nil {
		t.Fatalf("ReadContext rev2: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	if ctx.PageCount != 3 {
		t.Errorf("PageCount = %d, want 3 after insert+delete", ctx.PageCount)
	}
}

func TestDeletePage_ChainsAfterRotate(t *testing.T) {
	// rotate page 1 → delete page 2 → should produce a 2-page doc
	// where page 1 is rotated 90°.
	pdf := threePagePDF()
	rev1, err := pdfedit.RotatePage(pdf, 1, 90)
	if err != nil {
		t.Fatalf("RotatePage: %v", err)
	}
	rev2, err := pdfedit.DeletePage(rev1, 2)
	if err != nil {
		t.Fatalf("DeletePage after rotate: %v", err)
	}
	if !bytes.HasPrefix(rev2, rev1) {
		t.Error("rev2 must start with rev1 (incremental chain)")
	}
	ctx, err := api.ReadContext(bytes.NewReader(rev2), nil)
	if err != nil {
		t.Fatalf("ReadContext rev2: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	if ctx.PageCount != 2 {
		t.Errorf("PageCount = %d, want 2", ctx.PageCount)
	}
	dict, _, _, err := ctx.PageDict(1, false)
	if err != nil {
		t.Fatalf("PageDict: %v", err)
	}
	got := dict.IntEntry("Rotate")
	if got == nil || *got != 90 {
		t.Errorf("page 1 /Rotate = %v, want 90 (preserved across delete)", got)
	}
}
