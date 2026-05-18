package pdfedit_test

import (
	"bytes"
	"strings"
	"testing"

	"editor-service/internal/pdfedit"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestAddHighlight_PreservesOriginalBytes(t *testing.T) {
	pdf := minimalPDF()
	out, err := pdfedit.AddHighlight(pdf, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, "")
	if err != nil {
		t.Fatalf("AddHighlight: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("output must preserve original bytes verbatim")
	}
}

func TestAddHighlight_AppendsAnnotAndUpdatedPage(t *testing.T) {
	pdf := minimalPDF()
	out, err := pdfedit.AddHighlight(pdf, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, "")
	if err != nil {
		t.Fatalf("AddHighlight: %v", err)
	}
	appendix := string(out[len(pdf):])

	// Two new object headers: the annotation and the rewritten page.
	if !strings.Contains(appendix, "/Subtype /Highlight") {
		t.Errorf("appendix missing /Highlight subtype:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/Annots") {
		t.Errorf("appendix missing /Annots key on rewritten page:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/QuadPoints") {
		t.Error("appendix should include /QuadPoints (required for text-markup annotations)")
	}
}

func TestAddHighlight_RoundTripsThroughPdfcpu(t *testing.T) {
	pdf := minimalPDF()
	out, err := pdfedit.AddHighlight(pdf, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, "test")
	if err != nil {
		t.Fatalf("AddHighlight: %v", err)
	}
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("pdfcpu rejected the annotated PDF: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	dict, _, _, err := ctx.PageDict(1, false)
	if err != nil {
		t.Fatalf("PageDict: %v", err)
	}
	v, ok := dict.Find("Annots")
	if !ok {
		t.Fatal("page has no /Annots after AddHighlight")
	}
	arr, ok := v.(types.Array)
	if !ok {
		t.Fatalf("/Annots is %T, want types.Array", v)
	}
	if len(arr) != 1 {
		t.Errorf("len(/Annots) = %d, want 1", len(arr))
	}
}

func TestAddHighlight_NormalisesInvertedRect(t *testing.T) {
	// Caller passes opposite corners. Should not be rejected — the rect
	// gets normalised to (X0<=X1, Y0<=Y1).
	pdf := minimalPDF()
	out, err := pdfedit.AddHighlight(pdf, 1,
		pdfedit.AnnotRect{X0: 200, Y0: 120, X1: 100, Y1: 100},
		nil, "")
	if err != nil {
		t.Fatalf("inverted rect should be accepted (normalised): %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "/Rect [100 100 200 120]") {
		t.Errorf("rect not normalised; appendix:\n%s", appendix)
	}
}

func TestAddHighlight_DefaultColorWhenNil(t *testing.T) {
	pdf := minimalPDF()
	out, err := pdfedit.AddHighlight(pdf, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, "")
	if err != nil {
		t.Fatalf("AddHighlight: %v", err)
	}
	appendix := string(out[len(pdf):])
	// DefaultHighlightColor is RGB(1, 0.92, 0.23). The exact stringification
	// depends on fmtFloat — we just check it's not RGB(0,0,0).
	if strings.Contains(appendix, "/C [0 0 0]") {
		t.Error("nil color should fall back to DefaultHighlightColor, not black")
	}
}

func TestAddHighlight_CustomColorIsEmitted(t *testing.T) {
	pdf := minimalPDF()
	color := &pdfedit.AnnotColor{R: 0.1, G: 0.2, B: 0.3}
	out, err := pdfedit.AddHighlight(pdf, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		color, "")
	if err != nil {
		t.Fatalf("AddHighlight: %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "/C [0.1 0.2 0.3]") {
		t.Errorf("custom color not emitted; appendix:\n%s", appendix)
	}
}

func TestAddHighlight_ClampsOutOfRangeColor(t *testing.T) {
	pdf := minimalPDF()
	color := &pdfedit.AnnotColor{R: 1.5, G: -0.1, B: 2.0}
	out, err := pdfedit.AddHighlight(pdf, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		color, "")
	if err != nil {
		t.Fatalf("AddHighlight: %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "/C [1 0 1]") {
		t.Errorf("out-of-range color should be clamped to [0,1]; appendix:\n%s", appendix)
	}
}

func TestAddHighlight_EmitsContentsWhenProvided(t *testing.T) {
	pdf := minimalPDF()
	out, err := pdfedit.AddHighlight(pdf, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, "Important: review by Friday")
	if err != nil {
		t.Fatalf("AddHighlight: %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "/Contents (Important: review by Friday)") {
		t.Errorf("contents not emitted as PDF literal; appendix:\n%s", appendix)
	}
}

func TestAddHighlight_EscapesParensAndBackslashInContents(t *testing.T) {
	pdf := minimalPDF()
	out, err := pdfedit.AddHighlight(pdf, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, `a (b) \c`)
	if err != nil {
		t.Fatalf("AddHighlight: %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, `/Contents (a \(b\) \\c)`) {
		t.Errorf("contents not escaped per PDF literal rules; appendix:\n%s", appendix)
	}
}

func TestAddHighlight_OmitsContentsWhenEmpty(t *testing.T) {
	pdf := minimalPDF()
	out, err := pdfedit.AddHighlight(pdf, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, "")
	if err != nil {
		t.Fatalf("AddHighlight: %v", err)
	}
	appendix := string(out[len(pdf):])
	if strings.Contains(appendix, "/Contents") {
		t.Errorf("empty contents should not emit a /Contents key; appendix:\n%s", appendix)
	}
}

func TestAddHighlight_RejectsBadPage(t *testing.T) {
	pdf := minimalPDF()
	for _, n := range []int{0, -1, 99} {
		if _, err := pdfedit.AddHighlight(pdf, n,
			pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
			nil, ""); err == nil {
			t.Errorf("AddHighlight(page=%d) should have errored", n)
		}
	}
}

func TestAddHighlight_RejectsDegenerateRect(t *testing.T) {
	pdf := minimalPDF()
	cases := []pdfedit.AnnotRect{
		{X0: 100, Y0: 100, X1: 100, Y1: 100}, // zero area
		{X0: 100, Y0: 100, X1: 100, Y1: 200}, // zero width
		{X0: 100, Y0: 100, X1: 200, Y1: 100}, // zero height
	}
	for _, rect := range cases {
		if _, err := pdfedit.AddHighlight(pdf, 1, rect, nil, ""); err == nil {
			t.Errorf("expected degenerate rect %+v to be rejected", rect)
		}
	}
}

func TestAddHighlight_RejectsNonPDFInput(t *testing.T) {
	if _, err := pdfedit.AddHighlight([]byte("not a pdf"), 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, ""); err == nil {
		t.Fatal("expected error for non-PDF input")
	}
}

func TestAddHighlight_ChainsAfterRotate(t *testing.T) {
	// First rotate, then highlight — both incremental updates must
	// stack. After the second update, pdfcpu should report BOTH the
	// rotation and the annotation on the page.
	pdf := minimalPDF()
	rev1, err := pdfedit.RotatePage(pdf, 1, 90)
	if err != nil {
		t.Fatalf("RotatePage: %v", err)
	}
	rev2, err := pdfedit.AddHighlight(rev1, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, "ok")
	if err != nil {
		t.Fatalf("AddHighlight on rotated rev: %v", err)
	}
	if !bytes.HasPrefix(rev2, rev1) {
		t.Error("rev2 must start with rev1's bytes")
	}
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
	gotRot := dict.IntEntry("Rotate")
	if gotRot == nil || *gotRot != 90 {
		t.Errorf("/Rotate = %v, want 90 (preserved across the annotation update)", gotRot)
	}
	v, ok := dict.Find("Annots")
	if !ok {
		t.Fatal("Annots missing from page after stacked update")
	}
	arr, ok := v.(types.Array)
	if !ok || len(arr) != 1 {
		t.Errorf("/Annots = %v, want a 1-element array", v)
	}
}

// kindCases exercises every annotation kind through AddAnnotation and
// asserts (a) the correct /Subtype appears in the appendix, (b) pdfcpu
// re-reads the result, and (c) the page's /Annots picks up exactly one
// new ref. This is the per-kind smoke test that locks in the dispatch.
func TestAddAnnotation_AllKindsRoundTrip(t *testing.T) {
	cases := []struct {
		kind        pdfedit.AnnotKind
		wantSubtype string
		wantQuad    bool // text-markup kinds emit /QuadPoints; /Square doesn't
		wantBS      bool // /Square emits /BS; the text-markup kinds don't
	}{
		{pdfedit.AnnotHighlight, "/Highlight", true, false},
		{pdfedit.AnnotUnderline, "/Underline", true, false},
		{pdfedit.AnnotStrikeOut, "/StrikeOut", true, false},
		{pdfedit.AnnotSquiggly, "/Squiggly", true, false},
		{pdfedit.AnnotSquare, "/Square", false, true},
		// Sticky uses /Subtype /Text, no /QuadPoints, no /BS.
		// It does carry /Name /Note + /Open false — checked separately
		// in TestAddAnnotation_StickyEmitsTextSubtypeWithName.
		{pdfedit.AnnotSticky, "/Text", false, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			pdf := minimalPDF()
			out, err := pdfedit.AddAnnotation(pdf, tc.kind, 1,
				pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
				nil, "")
			if err != nil {
				t.Fatalf("AddAnnotation: %v", err)
			}
			appendix := string(out[len(pdf):])
			if !strings.Contains(appendix, "/Subtype "+tc.wantSubtype) {
				t.Errorf("missing %s subtype; appendix:\n%s", tc.wantSubtype, appendix)
			}
			if tc.wantQuad && !strings.Contains(appendix, "/QuadPoints") {
				t.Errorf("text-markup kind %s should emit /QuadPoints; appendix:\n%s", tc.kind, appendix)
			}
			if !tc.wantQuad && strings.Contains(appendix, "/QuadPoints") {
				t.Errorf("kind %s should NOT emit /QuadPoints; appendix:\n%s", tc.kind, appendix)
			}
			if tc.wantBS && !strings.Contains(appendix, "/BS") {
				t.Errorf("/Square should emit /BS border style; appendix:\n%s", appendix)
			}
			// pdfcpu must still re-read it with one /Annots entry.
			ctx, err := api.ReadContext(bytes.NewReader(out), nil)
			if err != nil {
				t.Fatalf("pdfcpu rejected revision: %v", err)
			}
			if err := ctx.EnsurePageCount(); err != nil {
				t.Fatalf("EnsurePageCount: %v", err)
			}
			dict, _, _, err := ctx.PageDict(1, false)
			if err != nil {
				t.Fatalf("PageDict: %v", err)
			}
			v, ok := dict.Find("Annots")
			if !ok {
				t.Fatal("page missing /Annots after AddAnnotation")
			}
			arr, ok := v.(types.Array)
			if !ok || len(arr) != 1 {
				t.Errorf("/Annots = %v, want 1-element array", v)
			}
		})
	}
}

func TestAddAnnotation_StickyEmitsTextSubtypeWithName(t *testing.T) {
	pdf := minimalPDF()
	// A typical sticky placement: a tiny 16x16pt icon rect near the top.
	out, err := pdfedit.AddAnnotation(pdf, pdfedit.AnnotSticky, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 700, X1: 116, Y1: 716},
		nil, "Please review this section")
	if err != nil {
		t.Fatalf("AddAnnotation(sticky): %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "/Subtype /Text") {
		t.Errorf("sticky must use /Subtype /Text (not /Sticky); appendix:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/Name /Note") {
		t.Errorf("sticky must carry /Name /Note for the icon style; appendix:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/Open false") {
		t.Errorf("sticky must emit /Open false so the popup is collapsed; appendix:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/Contents (Please review this section)") {
		t.Errorf("sticky must include the /Contents body; appendix:\n%s", appendix)
	}
}

func TestAddAnnotation_RejectsUnknownKind(t *testing.T) {
	pdf := minimalPDF()
	_, err := pdfedit.AddAnnotation(pdf, pdfedit.AnnotKind("freehand"), 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, "")
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestAddAnnotation_PerKindDefaultColors(t *testing.T) {
	// Smoke: every kind has a default color, and it's not RGB(0,0,0)
	// (would render invisibly on a white page for some readers).
	for kind, c := range pdfedit.DefaultAnnotColor {
		if c.R == 0 && c.G == 0 && c.B == 0 {
			t.Errorf("kind %s has all-zero default color; pick a visible one", kind)
		}
	}
}

func TestAddAnnotation_TwoAnnotsOnSamePageStackInArray(t *testing.T) {
	// Two annotations of different kinds on the same page should
	// both appear in /Annots — the second update must read the first
	// one's array (from rev1) and append, not replace.
	pdf := minimalPDF()
	rev1, err := pdfedit.AddAnnotation(pdf, pdfedit.AnnotHighlight, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 200, Y1: 120},
		nil, "")
	if err != nil {
		t.Fatalf("rev1: %v", err)
	}
	rev2, err := pdfedit.AddAnnotation(rev1, pdfedit.AnnotUnderline, 1,
		pdfedit.AnnotRect{X0: 100, Y0: 130, X1: 200, Y1: 150},
		nil, "")
	if err != nil {
		t.Fatalf("rev2: %v", err)
	}
	ctx, err := api.ReadContext(bytes.NewReader(rev2), nil)
	if err != nil {
		t.Fatalf("ReadContext: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	dict, _, _, err := ctx.PageDict(1, false)
	if err != nil {
		t.Fatalf("PageDict: %v", err)
	}
	v, _ := dict.Find("Annots")
	arr, ok := v.(types.Array)
	if !ok {
		t.Fatalf("/Annots not an array: %T", v)
	}
	if len(arr) != 2 {
		t.Errorf("len(/Annots) = %d, want 2 (highlight + underline)", len(arr))
	}
}

func TestAddInkAnnotation_EmitsInkListAndAutoRect(t *testing.T) {
	pdf := minimalPDF()
	strokes := [][]float64{
		// One stroke: 3 points making an L shape.
		{100, 100, 150, 100, 150, 150},
	}
	out, err := pdfedit.AddInkAnnotation(pdf, 1, strokes, nil, "")
	if err != nil {
		t.Fatalf("AddInkAnnotation: %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "/Subtype /Ink") {
		t.Errorf("appendix missing /Ink subtype:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/InkList [[100 100 150 100 150 150]]") {
		t.Errorf("appendix missing expected /InkList:\n%s", appendix)
	}
	// Bounding box: [100,100]-[150,150] padded 2pt each side = [98,98,152,152].
	if !strings.Contains(appendix, "/Rect [98 98 152 152]") {
		t.Errorf("appendix missing auto-computed /Rect with 2pt pad:\n%s", appendix)
	}
}

func TestAddInkAnnotation_MultipleStrokes(t *testing.T) {
	pdf := minimalPDF()
	strokes := [][]float64{
		{10, 10, 50, 10},   // horizontal stroke
		{30, 0, 30, 20},    // vertical stroke crossing it
	}
	out, err := pdfedit.AddInkAnnotation(pdf, 1, strokes, nil, "")
	if err != nil {
		t.Fatalf("AddInkAnnotation: %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "/InkList [[10 10 50 10] [30 0 30 20]]") {
		t.Errorf("multi-stroke /InkList not emitted as expected:\n%s", appendix)
	}
}

func TestAddInkAnnotation_RejectsEmptyStrokes(t *testing.T) {
	pdf := minimalPDF()
	if _, err := pdfedit.AddInkAnnotation(pdf, 1, nil, nil, ""); err == nil {
		t.Error("nil strokes should error")
	}
	if _, err := pdfedit.AddInkAnnotation(pdf, 1, [][]float64{{}}, nil, ""); err == nil {
		t.Error("empty stroke should error")
	}
}

func TestAddInkAnnotation_RejectsOddCoordCount(t *testing.T) {
	pdf := minimalPDF()
	// 3 coords = 1.5 points = malformed.
	_, err := pdfedit.AddInkAnnotation(pdf, 1, [][]float64{{1, 2, 3}}, nil, "")
	if err == nil {
		t.Error("odd coord count should error")
	}
}

func TestAddInkAnnotation_RejectsSinglePointStroke(t *testing.T) {
	pdf := minimalPDF()
	// 2 coords = 1 point — a dot. PDF /Ink requires at least 2
	// points per stroke to form a renderable line; we reject
	// rather than emit something readers handle inconsistently.
	_, err := pdfedit.AddInkAnnotation(pdf, 1, [][]float64{{1, 2}}, nil, "")
	if err == nil {
		t.Error("single-point stroke should error")
	}
}

func TestAddCalloutAnnotation_EmitsFreeTextWithCalloutIntent(t *testing.T) {
	pdf := minimalPDF()
	rect := pdfedit.AnnotRect{X0: 200, Y0: 200, X1: 300, Y1: 260}
	out, err := pdfedit.AddCalloutAnnotation(pdf, 1, rect,
		[2]float64{100, 100}, nil, "see here")
	if err != nil {
		t.Fatalf("AddCalloutAnnotation: %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "/Subtype /FreeText") {
		t.Errorf("appendix missing /FreeText subtype:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/IT /FreeTextCallout") {
		t.Errorf("appendix missing /IT /FreeTextCallout:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/LE /OpenArrow") {
		t.Errorf("appendix missing /LE /OpenArrow on the anchor side:\n%s", appendix)
	}
	// /CL goes from anchor (100,100) to the nearest corner of the
	// rect — that's (200, 200) (bottom-left).
	if !strings.Contains(appendix, "/CL [100 100 200 200]") {
		t.Errorf("appendix missing expected /CL going to nearest corner:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/Contents (see here)") {
		t.Errorf("appendix missing /Contents:\n%s", appendix)
	}
}

func TestAddCalloutAnnotation_CornerSelectionPicksClosest(t *testing.T) {
	pdf := minimalPDF()
	rect := pdfedit.AnnotRect{X0: 200, Y0: 200, X1: 300, Y1: 260}
	// Anchor to the upper-right of the rect → corner should be
	// (300, 260), not (200, 200).
	out, err := pdfedit.AddCalloutAnnotation(pdf, 1, rect,
		[2]float64{500, 500}, nil, "")
	if err != nil {
		t.Fatalf("AddCalloutAnnotation: %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "/CL [500 500 300 260]") {
		t.Errorf("expected upper-right corner; appendix:\n%s", appendix)
	}
}

func TestAddCalloutAnnotation_RejectsDegenerateRect(t *testing.T) {
	pdf := minimalPDF()
	rect := pdfedit.AnnotRect{X0: 100, Y0: 100, X1: 100, Y1: 100}
	if _, err := pdfedit.AddCalloutAnnotation(pdf, 1, rect,
		[2]float64{50, 50}, nil, ""); err == nil {
		t.Error("zero-area callout rect should error")
	}
}

func TestAddInkAnnotation_RoundTripsThroughPdfcpu(t *testing.T) {
	pdf := minimalPDF()
	out, err := pdfedit.AddInkAnnotation(pdf, 1,
		[][]float64{{100, 100, 200, 200, 300, 100}}, nil, "")
	if err != nil {
		t.Fatalf("AddInkAnnotation: %v", err)
	}
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("pdfcpu rejected the ink-annotated PDF: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	dict, _, _, err := ctx.PageDict(1, false)
	if err != nil {
		t.Fatalf("PageDict: %v", err)
	}
	v, ok := dict.Find("Annots")
	if !ok {
		t.Fatal("page has no /Annots after AddInkAnnotation")
	}
	if arr, ok := v.(types.Array); !ok || len(arr) != 1 {
		t.Errorf("/Annots = %v (%T), want array len 1", v, v)
	}
}
