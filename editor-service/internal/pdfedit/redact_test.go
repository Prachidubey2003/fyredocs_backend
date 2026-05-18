package pdfedit_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"editor-service/internal/corpus"
	"editor-service/internal/pdfedit"
	"editor-service/internal/spdom"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func TestRedactArea_PreservesOriginalBytes(t *testing.T) {
	pdf := corpus.WithText([]string{"Sensitive payroll info", "Public summary"})
	out, err := pdfedit.RedactArea(pdf, 1, pdfedit.AnnotRect{X0: 50, Y0: 740, X1: 500, Y1: 770})
	if err != nil {
		t.Fatalf("RedactArea: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("redacted revision must start with the original bytes verbatim")
	}
	if len(out) <= len(pdf) {
		t.Error("redacted revision should be longer than the original (appended section)")
	}
}

func TestRedactArea_ScrubsInRectTextFromContentStream(t *testing.T) {
	// The fixture lays out one line per Tm + Tj at y=750, 734, …
	// (16pt spacing). Rect must catch line 1 (y=750, glyph bbox
	// up to ~762) only; line 2 (y=734, glyph bbox up to ~746)
	// must survive.
	//
	// v1 redact uses any-glyph-overlap semantics, so the rect
	// must start ABOVE line 2's glyph tops (746) — Y0=747 gives
	// a 1pt margin. The redact UI in the frontend rounds y to
	// the nearest integer above the previous line, so this is a
	// realistic value.
	pdf := corpus.WithText([]string{"Sensitive payroll info", "Public summary"})
	out, err := pdfedit.RedactArea(pdf, 1, pdfedit.AnnotRect{X0: 50, Y0: 747, X1: 500, Y1: 770})
	if err != nil {
		t.Fatalf("RedactArea: %v", err)
	}
	// The redacted bytes are produced as an incremental update —
	// `out` contains both the original (with the secret) and the
	// appended new content stream object (with the scrubbed text).
	// To check the scrub, we ask pdfcpu to re-read the document and
	// extract text. The redacted text must be gone from the
	// effective extraction; "Public summary" must remain.
	got := extractFirstPageText(t, out)
	if strings.Contains(got, "Sensitive") || strings.Contains(got, "payroll") {
		t.Errorf("redacted text leaked into extraction:\n%s", got)
	}
	if !strings.Contains(got, "Public summary") {
		t.Errorf("out-of-rect text was wiped; extraction:\n%s", got)
	}
}

func TestRedactArea_AppendsBlackOverlay(t *testing.T) {
	pdf := corpus.WithText([]string{"Hello"})
	out, err := pdfedit.RedactArea(pdf, 1, pdfedit.AnnotRect{X0: 70, Y0: 745, X1: 500, Y1: 760})
	if err != nil {
		t.Fatalf("RedactArea: %v", err)
	}
	// The new content-stream object in the appendix should carry
	// the overlay ops: black-fill (0 0 0 rg), a re rectangle, and
	// the fill op f, wrapped in q…Q.
	appendix := string(out[len(pdf):])
	for _, marker := range []string{"0 0 0 rg", " re\nf\n", "q\n", "\nQ\n"} {
		if !strings.Contains(appendix, marker) {
			t.Errorf("overlay marker %q missing from appendix:\n%s", marker, appendix)
		}
	}
}

func TestRedactArea_RoundTripsThroughPdfcpu(t *testing.T) {
	// Strongest correctness signal: pdfcpu re-reads the redacted
	// bytes without error.
	pdf := corpus.WithText([]string{"To redact"})
	out, err := pdfedit.RedactArea(pdf, 1, pdfedit.AnnotRect{X0: 50, Y0: 740, X1: 500, Y1: 770})
	if err != nil {
		t.Fatalf("RedactArea: %v", err)
	}
	if _, err := api.ReadContext(bytes.NewReader(out), nil); err != nil {
		t.Errorf("pdfcpu refused redacted PDF: %v", err)
	}
}

func TestRedactArea_RejectsDegenerateRect(t *testing.T) {
	pdf := corpus.WithText([]string{"x"})
	cases := []pdfedit.AnnotRect{
		{X0: 100, Y0: 100, X1: 100, Y1: 200}, // zero width
		{X0: 100, Y0: 100, X1: 200, Y1: 100}, // zero height
		{X0: 200, Y0: 100, X1: 100, Y1: 200}, // inverted X
	}
	for _, r := range cases {
		if _, err := pdfedit.RedactArea(pdf, 1, r); err == nil {
			t.Errorf("degenerate rect %+v: got nil error, want non-nil", r)
		} else if !strings.Contains(err.Error(), "degenerate") {
			t.Errorf("degenerate rect %+v: err = %v, want 'degenerate' marker", r, err)
		}
	}
}

func TestRedactArea_RejectsOutOfRangePage(t *testing.T) {
	pdf := corpus.WithText([]string{"x"})
	if _, err := pdfedit.RedactArea(pdf, 0, pdfedit.AnnotRect{X0: 0, Y0: 0, X1: 10, Y1: 10}); err == nil {
		t.Error("pageNum 0: got nil error, want non-nil")
	}
	if _, err := pdfedit.RedactArea(pdf, 99, pdfedit.AnnotRect{X0: 0, Y0: 0, X1: 10, Y1: 10}); err == nil {
		t.Error("pageNum 99 (out of range): got nil error, want non-nil")
	}
}

func TestRedactArea_FilteredStreamReturnsSentinel(t *testing.T) {
	// Build a fixture with a /FlateDecode-filtered content stream —
	// v0 refuses to scrub it. Reuses the same error sentinel as
	// ReplaceText so the editops classifier maps it to 400.
	pdf := buildFlatePDFFixture(t)
	_, err := pdfedit.RedactArea(pdf, 1, pdfedit.AnnotRect{X0: 0, Y0: 0, X1: 100, Y1: 100})
	if !errors.Is(err, pdfedit.ErrStreamFiltered) {
		t.Errorf("err = %v, want ErrStreamFiltered", err)
	}
}

// --- helpers ---------------------------------------------------------------

// extractFirstPageText returns the plain text of page 1 by routing
// through spdom.Parse (the same path the editor uses). Lower-level
// than running pdfcpu directly; gives us deterministic content even
// when pdfcpu's high-level extractor would otherwise inject layout
// hints.
func extractFirstPageText(t *testing.T, pdfBytes []byte) string {
	t.Helper()
	doc, err := spdom.Parse("test-doc", bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("spdom.Parse: %v", err)
	}
	if len(doc.Pages) == 0 {
		t.Fatalf("spdom.Parse: no pages")
	}
	var sb strings.Builder
	for _, b := range doc.Pages[0].Blocks {
		for _, ln := range b.Lines {
			for _, r := range ln.Runs {
				sb.WriteString(r.Text)
				sb.WriteByte(' ')
			}
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// buildFlatePDFFixture returns a tiny single-page PDF whose
// /Contents stream advertises /Filter /FlateDecode. The bytes
// don't have to be valid Flate — RedactArea checks for the
// /Filter key, not the encoding. (Same approach as the
// ReplaceText filtered-stream test.)
func buildFlatePDFFixture(t *testing.T) []byte {
	t.Helper()
	// Re-use corpus.WithText to get the dict + stream layout, then
	// surgically splice /Filter /FlateDecode into the /Contents dict.
	src := corpus.WithText([]string{"x"})
	idx := bytes.Index(src, []byte("<< /Length"))
	if idx < 0 {
		t.Fatal("could not find /Length in fixture for filtered-stream test")
	}
	// Insert /Filter /FlateDecode right before /Length.
	out := make([]byte, 0, len(src)+24)
	out = append(out, src[:idx+2]...) // include the `<<`
	out = append(out, []byte(" /Filter /FlateDecode")...)
	out = append(out, src[idx+2:]...)
	return out
}
