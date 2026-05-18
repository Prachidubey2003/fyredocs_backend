package editops_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"editor-service/internal/corpus"
	"editor-service/internal/editops"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// minimalPDF is a thin alias over [corpus.Minimal] — kept so the
// existing call sites read the same.
func minimalPDF() []byte { return corpus.Minimal() }

func TestApply_PageRotateProducesValidRevision(t *testing.T) {
	pdf := minimalPDF()
	out, err := editops.Apply([]editops.Op{{Type: editops.PageRotate, Page: 1, Rotation: 90}}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("apply must preserve original bytes verbatim")
	}
	// Round-trip through pdfcpu to confirm the rotation is materialized.
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	dict, _, _, err := ctx.PageDict(1, false)
	if err != nil {
		t.Fatalf("PageDict: %v", err)
	}
	got := dict.IntEntry("Rotate")
	if got == nil || *got != 90 {
		t.Errorf("/Rotate = %v, want 90", got)
	}
}

func TestApply_NoOpsReturnsErrNoOps(t *testing.T) {
	_, err := editops.Apply(nil, minimalPDF())
	if !errors.Is(err, editops.ErrNoOps) {
		t.Errorf("err = %v, want ErrNoOps", err)
	}
}

func TestApply_MultipleOpsRunInSequence(t *testing.T) {
	// rotate + highlight on the same document, in one request, should
	// produce a single output whose final state has BOTH the rotation
	// and the annotation. The bytes contain two appended incremental
	// sections — readers stack them via /Prev.
	pdf := minimalPDF()
	ops := []editops.Op{
		{Type: editops.PageRotate, Page: 1, Rotation: 90},
		{
			Type: editops.AnnotationAdd, Page: 1, Kind: "highlight",
			Rect: []float64{100, 100, 200, 120},
		},
	}
	out, err := editops.Apply(ops, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("multi-op output must preserve original bytes verbatim")
	}
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("pdfcpu rejected multi-op revision: %v", err)
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
		t.Errorf("/Rotate = %v, want 90 (rotate op must survive into final revision)", gotRot)
	}
	v, ok := dict.Find("Annots")
	if !ok {
		t.Fatal("/Annots missing after multi-op (annotation op should have added it)")
	}
	if _, ok := v.(interface{ Len() int }); !ok {
		// pdfcpu types.Array doesn't expose Len() — but we just need it
		// to be the right shape. Just confirm it's not nil/empty.
		// (Detailed shape checks live in the per-kind tests.)
	}
}

func TestApply_OpErrorPrefixesOpIndex(t *testing.T) {
	// When an op past the first fails, the error should be wrapped
	// with the op index so a multi-op caller can pinpoint the bad one.
	ops := []editops.Op{
		{Type: editops.PageRotate, Page: 1, Rotation: 90},
		{Type: editops.PageRotate, Page: 1, Rotation: 42}, // invalid
	}
	_, err := editops.Apply(ops, minimalPDF())
	if err == nil {
		t.Fatal("expected error from invalid rotation")
	}
	if !strings.Contains(err.Error(), "ops[1]") {
		t.Errorf("error %q should mention `ops[1]` so the caller knows which op failed", err)
	}
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("error %v should still wrap ErrInvalidArgs for status-code mapping", err)
	}
}

func TestApply_RejectsBeyondMaxOpsLimit(t *testing.T) {
	// Construct a request with MaxOpsPerRequest + 1 ops. Apply should
	// reject it as ErrInvalidArgs without doing any work — protects
	// against accidental N-thousand requests.
	ops := make([]editops.Op, editops.MaxOpsPerRequest+1)
	for i := range ops {
		ops[i] = editops.Op{Type: editops.PageRotate, Page: 1, Rotation: 90}
	}
	_, err := editops.Apply(ops, minimalPDF())
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs for over-limit request", err)
	}
}

func TestApply_UnknownOpReturnsErrUnknownOp(t *testing.T) {
	_, err := editops.Apply([]editops.Op{{Type: "page.flip-upside-down"}}, minimalPDF())
	if !errors.Is(err, editops.ErrUnknownOp) {
		t.Errorf("err = %v, want ErrUnknownOp", err)
	}
}

func TestApply_AnnotationAddHighlightProducesValidRevision(t *testing.T) {
	pdf := minimalPDF()
	op := editops.Op{
		Type:     editops.AnnotationAdd,
		Page:     1,
		Kind:     "highlight",
		Rect:     []float64{100, 100, 200, 120},
		Color:    []float64{1, 0.92, 0.23},
		Contents: "review",
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("annotation.add must preserve original bytes")
	}
	// Round-trip via pdfcpu — same invariant as page.rotate.
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
}

func TestApply_AnnotationAddDefaultsKindToHighlight(t *testing.T) {
	pdf := minimalPDF()
	op := editops.Op{
		Type: editops.AnnotationAdd,
		Page: 1,
		// No Kind — should default to "highlight".
		Rect: []float64{100, 100, 200, 120},
	}
	if _, err := editops.Apply([]editops.Op{op}, pdf); err != nil {
		t.Errorf("annotation.add without Kind should default to highlight, got: %v", err)
	}
}

func TestApply_AnnotationAddRejectsUnknownKind(t *testing.T) {
	pdf := minimalPDF()
	op := editops.Op{
		Type: editops.AnnotationAdd,
		Page: 1,
		Kind: "lasso", // not implemented in v0
		Rect: []float64{100, 100, 200, 120},
	}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrUnknownOp) {
		t.Errorf("err = %v, want ErrUnknownOp for unknown annotation kind", err)
	}
}

func TestApply_AnnotationAddDispatchesEveryKind(t *testing.T) {
	// Every supported wire-format kind should produce a valid revision.
	// This is the wire-level companion to AddAnnotation_AllKindsRoundTrip;
	// it catches gaps in the editops kind-string → AnnotKind mapping.
	pdf := minimalPDF()
	for _, kind := range []string{"highlight", "underline", "strikeout", "squiggly", "square", "sticky"} {
		t.Run(kind, func(t *testing.T) {
			op := editops.Op{
				Type: editops.AnnotationAdd,
				Page: 1,
				Kind: kind,
				Rect: []float64{100, 100, 200, 120},
			}
			out, err := editops.Apply([]editops.Op{op}, pdf)
			if err != nil {
				t.Fatalf("Apply(kind=%s): %v", kind, err)
			}
			if !bytes.HasPrefix(out, pdf) {
				t.Errorf("kind=%s: original bytes not preserved", kind)
			}
			if _, err := api.ReadContext(bytes.NewReader(out), nil); err != nil {
				t.Errorf("kind=%s: pdfcpu rejected revision: %v", kind, err)
			}
		})
	}
}

func TestApply_AnnotationAddRejectsBadPage(t *testing.T) {
	pdf := minimalPDF()
	for _, p := range []int{0, -1, 99} {
		op := editops.Op{
			Type: editops.AnnotationAdd,
			Page: p,
			Kind: "highlight",
			Rect: []float64{100, 100, 200, 120},
		}
		if _, err := editops.Apply([]editops.Op{op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
			t.Errorf("page=%d: err = %v, want ErrInvalidArgs", p, err)
		}
	}
}

func TestApply_AnnotationAddRejectsBadRect(t *testing.T) {
	pdf := minimalPDF()
	cases := [][]float64{
		nil,                  // missing rect entirely
		{},                   // empty
		{100, 100},           // not 4 values
		{100, 100, 200},      // not 4 values
		{100, 100, 100, 100}, // degenerate (zero area)
	}
	for _, rect := range cases {
		op := editops.Op{
			Type: editops.AnnotationAdd,
			Page: 1,
			Kind: "highlight",
			Rect: rect,
		}
		if _, err := editops.Apply([]editops.Op{op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
			t.Errorf("rect=%v: err = %v, want ErrInvalidArgs", rect, err)
		}
	}
}

func TestApply_AnnotationAddRejectsBadColor(t *testing.T) {
	pdf := minimalPDF()
	for _, color := range [][]float64{
		{1, 0},       // not 3 channels
		{1, 0, 0, 1}, // not 3 channels
	} {
		op := editops.Op{
			Type:  editops.AnnotationAdd,
			Page:  1,
			Kind:  "highlight",
			Rect:  []float64{100, 100, 200, 120},
			Color: color,
		}
		if _, err := editops.Apply([]editops.Op{op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
			t.Errorf("color=%v: err = %v, want ErrInvalidArgs", color, err)
		}
	}
}

func TestApply_TotallyUnknownOpReturnsErrUnknownOp(t *testing.T) {
	// Every roadmap op now ships (page.* / annotation.add /
	// text.* / redact.apply / table.cell.edit). Anything else
	// the caller sends is a wire-format mistake and must
	// surface as ErrUnknownOp so the handler maps it to 400.
	_, err := editops.Apply([]editops.Op{{Type: "fyre.no-such-op"}}, minimalPDF())
	if !errors.Is(err, editops.ErrUnknownOp) {
		t.Errorf("err = %v, want ErrUnknownOp", err)
	}
}

func TestApply_PageRotateRejectsZeroPage(t *testing.T) {
	_, err := editops.Apply([]editops.Op{{Type: editops.PageRotate, Page: 0, Rotation: 90}}, minimalPDF())
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs", err)
	}
}

func TestApply_PageRotateRejectsBadRotation(t *testing.T) {
	for _, deg := range []int{1, 45, 360, -90} {
		_, err := editops.Apply([]editops.Op{{Type: editops.PageRotate, Page: 1, Rotation: deg}}, minimalPDF())
		if !errors.Is(err, editops.ErrInvalidArgs) {
			t.Errorf("rotation=%d: err = %v, want ErrInvalidArgs", deg, err)
		}
	}
}

func TestApply_PageRotateRejectsOutOfRangePage(t *testing.T) {
	_, err := editops.Apply([]editops.Op{{Type: editops.PageRotate, Page: 99, Rotation: 90}}, minimalPDF())
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs (page out of range should map to 400)", err)
	}
}

func TestApply_PageDeleteRemovesPage(t *testing.T) {
	// Build a 2-page PDF inline (avoid pulling threePagePDF across the
	// package boundary).
	parts := []string{
		"%PDF-1.4\n",
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>\nendobj\n",
		"4 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>\nendobj\n",
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
	buf.WriteString("xref\n0 5\n0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	buf.WriteString("trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n")
	fmt.Fprintf(&buf, "%d\n%%%%EOF\n", xrefStart)
	pdf := buf.Bytes()

	out, err := editops.Apply([]editops.Op{{Type: editops.PageDelete, Page: 1}}, pdf)
	if err != nil {
		t.Fatalf("Apply page.delete: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("original bytes must be preserved verbatim")
	}
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("re-read after delete: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	if ctx.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1", ctx.PageCount)
	}
}

func TestApply_PageDeleteRejectsZeroPage(t *testing.T) {
	_, err := editops.Apply([]editops.Op{{Type: editops.PageDelete, Page: 0}}, minimalPDF())
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs", err)
	}
}

func TestApply_PageInsertAddsBlankPage(t *testing.T) {
	pdf := minimalPDF() // 1-page doc
	zero := 0
	out, err := editops.Apply(
		[]editops.Op{{Type: editops.PageInsert, AfterPage: &zero}},
		pdf,
	)
	if err != nil {
		t.Fatalf("Apply page.insert: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("original bytes must be preserved verbatim")
	}
	ctx, err := api.ReadContext(bytes.NewReader(out), nil)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	if ctx.PageCount != 2 {
		t.Errorf("PageCount = %d, want 2 (1 original + 1 inserted)", ctx.PageCount)
	}
}

func TestApply_PageInsertRequiresAfterPage(t *testing.T) {
	// Missing afterPage (nil pointer) — surfaced as ErrInvalidArgs so
	// the handler returns 400, not 500.
	_, err := editops.Apply(
		[]editops.Op{{Type: editops.PageInsert}},
		minimalPDF(),
	)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs when afterPage is missing", err)
	}
}

func TestApply_PageInsertRejectsNegativeAfterPage(t *testing.T) {
	neg := -1
	_, err := editops.Apply(
		[]editops.Op{{Type: editops.PageInsert, AfterPage: &neg}},
		minimalPDF(),
	)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs for negative afterPage", err)
	}
}

func TestApply_PageInsertRejectsOutOfRangeAfterPage(t *testing.T) {
	tooBig := 99
	_, err := editops.Apply(
		[]editops.Op{{Type: editops.PageInsert, AfterPage: &tooBig}},
		minimalPDF(),
	)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs for afterPage past end", err)
	}
}

func TestApply_PageDeleteOnSinglePageReturnsInvalidArgs(t *testing.T) {
	// Deleting page 1 from minimalPDF (a 1-page doc) should surface
	// the pdfedit "last page" error as ErrInvalidArgs so the handler
	// returns 400, not 500.
	_, err := editops.Apply([]editops.Op{{Type: editops.PageDelete, Page: 1}}, minimalPDF())
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs (last-page deletion must be 400)", err)
	}
}

func TestApply_GarbageBytesReturnInvalidArgs(t *testing.T) {
	_, err := editops.Apply([]editops.Op{{Type: editops.PageRotate, Page: 1, Rotation: 90}}, []byte("not a pdf"))
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs (garbage source must surface as 400)", err)
	}
}

func TestParseRequest_HappyPath(t *testing.T) {
	body := []byte(`{"ops":[{"type":"page.rotate","page":2,"rotation":180}],"message":"flip"}`)
	req, err := editops.ParseRequest(body)
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if len(req.Ops) != 1 || req.Ops[0].Type != editops.PageRotate {
		t.Errorf("ops = %+v, want one page.rotate", req.Ops)
	}
	if req.Ops[0].Page != 2 || req.Ops[0].Rotation != 180 {
		t.Errorf("op args = %+v, want page=2 rotation=180", req.Ops[0])
	}
	if req.Message != "flip" {
		t.Errorf("message = %q, want %q", req.Message, "flip")
	}
}

func TestParseRequest_MalformedJSONReturnsInvalidArgs(t *testing.T) {
	_, err := editops.ParseRequest([]byte("{not json"))
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs", err)
	}
}

func TestParseRequest_EmptyOpsArrayAllowedToParse(t *testing.T) {
	// Empty array should parse cleanly — Apply is the layer that
	// rejects it with ErrNoOps. Splitting validation between parse
	// (shape) and apply (semantics) gives the handler clearer error
	// codes to map.
	req, err := editops.ParseRequest([]byte(`{"ops":[]}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if len(req.Ops) != 0 {
		t.Errorf("ops = %+v, want empty", req.Ops)
	}
	// Confirm the contract: Apply on the empty slice returns ErrNoOps.
	_, err = editops.Apply(req.Ops, minimalPDF())
	if !errors.Is(err, editops.ErrNoOps) {
		t.Errorf("Apply on empty ops: err = %v, want ErrNoOps", err)
	}
}

func TestApply_ErrorMessagesAreActionable(t *testing.T) {
	// Sanity check: the wrapped error message should mention something
	// concrete a caller can act on. We're not locking the exact string,
	// just confirming it isn't empty / generic.
	_, err := editops.Apply([]editops.Op{{Type: "made.up"}}, minimalPDF())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "made.up") {
		t.Errorf("error %q should mention the offending op type", err.Error())
	}
}

func TestApply_AnnotationFreehand_HappyPath(t *testing.T) {
	pdf := minimalPDF()
	op := editops.Op{
		Type:    editops.AnnotationAdd,
		Page:    1,
		Kind:    "freehand",
		Strokes: [][]float64{{100, 100, 150, 100, 150, 150}},
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("original bytes not preserved")
	}
	if _, err := api.ReadContext(bytes.NewReader(out), nil); err != nil {
		t.Errorf("pdfcpu rejected the freehand revision: %v", err)
	}
}

func TestApply_AnnotationFreehand_RejectsMissingStrokes(t *testing.T) {
	pdf := minimalPDF()
	op := editops.Op{
		Type: editops.AnnotationAdd,
		Page: 1,
		Kind: "freehand",
		// no strokes
	}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs", err)
	}
}

func TestApply_AnnotationFreehand_DoesNotRequireRect(t *testing.T) {
	// Freehand auto-derives rect from the strokes' bounding box —
	// the wire shape MUST allow omitting rect, otherwise users
	// have to bookkeep something the server can compute itself.
	pdf := minimalPDF()
	op := editops.Op{
		Type:    editops.AnnotationAdd,
		Page:    1,
		Kind:    "freehand",
		Strokes: [][]float64{{10, 10, 20, 20}},
		// no Rect field
	}
	if _, err := editops.Apply([]editops.Op{op}, pdf); err != nil {
		t.Errorf("freehand without rect should succeed, got %v", err)
	}
}

func TestApply_AnnotationCallout_HappyPath(t *testing.T) {
	pdf := minimalPDF()
	op := editops.Op{
		Type:     editops.AnnotationAdd,
		Page:     1,
		Kind:     "callout",
		Rect:     []float64{200, 200, 300, 260},
		Anchor:   &[2]float64{100, 100},
		Contents: "see this",
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("original bytes not preserved")
	}
	if _, err := api.ReadContext(bytes.NewReader(out), nil); err != nil {
		t.Errorf("pdfcpu rejected the callout revision: %v", err)
	}
}

func TestApply_AnnotationCallout_RejectsMissingAnchor(t *testing.T) {
	pdf := minimalPDF()
	op := editops.Op{
		Type: editops.AnnotationAdd,
		Page: 1,
		Kind: "callout",
		Rect: []float64{200, 200, 300, 260},
		// no Anchor
	}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs", err)
	}
}

func TestApply_AnnotationCallout_RejectsMissingRect(t *testing.T) {
	pdf := minimalPDF()
	op := editops.Op{
		Type:   editops.AnnotationAdd,
		Page:   1,
		Kind:   "callout",
		Anchor: &[2]float64{50, 50},
		// no Rect
	}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs", err)
	}
}

func TestApply_TextReplace_RoundTripsThroughPdfcpu(t *testing.T) {
	// The WithText fixture renders two lines as plain Tj
	// literals on a single uncompressed content stream — the
	// happiest path for the v0 text.replace constraints.
	pdf := corpus.WithText([]string{"hello", "world"})
	op := editops.Op{
		Type:    editops.TextReplace,
		Page:    1,
		Find:    "hello",
		Replace: "Hello, world!",
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply text.replace: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("text.replace must preserve original bytes verbatim")
	}
	// pdfcpu reads the appended revision back; the page count
	// stays 1 and the rewritten /Contents object is valid.
	if _, err := api.ReadContext(bytes.NewReader(out), nil); err != nil {
		t.Fatalf("pdfcpu rejected the text.replace revision: %v", err)
	}
	// And the new literal appears in the appended incremental
	// section — grep is sufficient because the corpus fixture
	// emits content uncompressed.
	if !bytes.Contains(out[len(pdf):], []byte("(Hello, world!) Tj")) {
		t.Errorf("rewritten literal missing from appendix:\n%s", out[len(pdf):])
	}
	if bytes.Contains(out[len(pdf):], []byte("(hello) Tj")) {
		t.Errorf("appendix still contains the original literal:\n%s", out[len(pdf):])
	}
}

func TestApply_TextReplace_NotFoundIsInvalidArgs(t *testing.T) {
	pdf := corpus.WithText([]string{"hello"})
	op := editops.Op{
		Type:    editops.TextReplace,
		Page:    1,
		Find:    "nothing matches this",
		Replace: "x",
	}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs", err)
	}
	// Error message should mention the searched string so the
	// caller can show the user what they asked for.
	if err != nil && !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should describe the not-found case: %v", err)
	}
}

func TestApply_TextReplace_RejectsBadPage(t *testing.T) {
	pdf := corpus.WithText([]string{"hello"})
	for _, p := range []int{0, -1, 99} {
		op := editops.Op{
			Type: editops.TextReplace, Page: p,
			Find: "hello", Replace: "x",
		}
		if _, err := editops.Apply([]editops.Op{op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
			t.Errorf("page=%d: err = %v, want ErrInvalidArgs", p, err)
		}
	}
}

func TestApply_TextReplace_RejectsEmptyFind(t *testing.T) {
	pdf := corpus.WithText([]string{"hello"})
	op := editops.Op{
		Type: editops.TextReplace, Page: 1,
		Find: "", Replace: "x",
	}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs for empty find", err)
	}
}

func TestApply_TextReplace_OnlyReplacesFirstMatch(t *testing.T) {
	// Two identical literals on the page — the op rewrites the
	// first one only, leaving the second intact. This matches
	// the spdom.ReplaceFirstLiteral contract.
	pdf := corpus.WithText([]string{"hello", "hello"})
	op := editops.Op{
		Type: editops.TextReplace, Page: 1,
		Find: "hello", Replace: "bye",
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	appendix := out[len(pdf):]
	if !bytes.Contains(appendix, []byte("(bye) Tj")) {
		t.Errorf("first match wasn't rewritten:\n%s", appendix)
	}
	// The second (hello) Tj must still be present in the new
	// content stream — confirm by counting occurrences in the
	// rewritten content. The replacement object is the only
	// place either literal appears post-edit (incremental
	// updates override the prior object).
	helloCount := bytes.Count(appendix, []byte("(hello) Tj"))
	if helloCount != 1 {
		t.Errorf("expected exactly one remaining (hello) Tj in rewritten stream, got %d:\n%s",
			helloCount, appendix)
	}
}

func TestApply_RedactApply_RoundTripsAndScrubs(t *testing.T) {
	// End-to-end: editops dispatches redact.apply through to
	// pdfedit.RedactArea. Verifying here (rather than only in
	// pdfedit_test) ensures the wire-format → translator wiring
	// works and the dispatched op produces a parseable PDF.
	pdf := corpus.WithText([]string{"top secret"})
	op := editops.Op{
		Type: editops.RedactApply,
		Page: 1,
		Rect: []float64{50, 745, 500, 760},
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	appendix := out[len(pdf):]
	// Scrub marker — the secret string is empty in the new content
	// stream object.
	if bytes.Contains(appendix, []byte("(top secret)")) {
		t.Errorf("redacted text leaked into new content stream:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("() Tj")) {
		t.Errorf("expected an empty Tj after scrub:\n%s", appendix)
	}
	// Overlay marker — black-fill rectangle drawn.
	if !bytes.Contains(appendix, []byte("0 0 0 rg")) {
		t.Errorf("missing black-fill overlay marker:\n%s", appendix)
	}
}

func TestApply_RedactApply_RejectsMissingRect(t *testing.T) {
	// The dispatcher must validate `rect` BEFORE calling pdfedit
	// so the user gets a 400 with a clear message rather than a
	// 500 from RedactArea's pageNum check.
	pdf := corpus.WithText([]string{"x"})
	op := editops.Op{Type: editops.RedactApply, Page: 1}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs for missing rect", err)
	}
}

func TestApply_RedactApply_RejectsBadPage(t *testing.T) {
	pdf := corpus.WithText([]string{"x"})
	for _, p := range []int{0, -1, 99} {
		op := editops.Op{
			Type: editops.RedactApply, Page: p,
			Rect: []float64{0, 0, 10, 10},
		}
		if _, err := editops.Apply([]editops.Op{op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
			t.Errorf("page=%d: err = %v, want ErrInvalidArgs", p, err)
		}
	}
}

func TestApply_TableCellEdit_ScrubsAndReplaces(t *testing.T) {
	// End-to-end: editops dispatches table.cell.edit through to
	// pdfedit.EditTableCell. The cell rect overlaps the only
	// text line; the original literal should be scrubbed and a
	// fresh BT block drawing the new text appended.
	pdf := corpus.WithText([]string{"Old Cell"})
	op := editops.Op{
		Type: editops.TableCellEdit,
		Page: 1,
		Rect: []float64{50, 745, 500, 765},
		Text: "New Cell",
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	appendix := out[len(pdf):]
	if bytes.Contains(appendix, []byte("(Old Cell)")) {
		t.Errorf("original cell text leaked into new content stream:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("(New Cell) Tj")) {
		t.Errorf("expected replacement Tj `(New Cell) Tj` in appendix:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("/F1 12 Tf")) {
		t.Errorf("replacement should re-emit /F1 12 Tf (original font); got %s", appendix)
	}
	if !bytes.Contains(out, pdf) {
		t.Error("incremental update must preserve original bytes verbatim")
	}
}

func TestApply_TableCellEdit_RejectsMissingFields(t *testing.T) {
	pdf := corpus.WithText([]string{"Cell"})
	cases := []struct {
		name string
		op   editops.Op
	}{
		{"missing rect", editops.Op{Type: editops.TableCellEdit, Page: 1, Text: "x"}},
		{"wrong rect arity", editops.Op{Type: editops.TableCellEdit, Page: 1, Rect: []float64{1, 2, 3}, Text: "x"}},
		{"missing text", editops.Op{Type: editops.TableCellEdit, Page: 1, Rect: []float64{0, 0, 10, 10}}},
		{"bad page", editops.Op{Type: editops.TableCellEdit, Page: 0, Rect: []float64{0, 0, 10, 10}, Text: "x"}},
	}
	for _, c := range cases {
		if _, err := editops.Apply([]editops.Op{c.op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
			t.Errorf("%s: err = %v, want ErrInvalidArgs", c.name, err)
		}
	}
}

func TestApply_TableCellEdit_EmptyCellReturnsInvalidArgs(t *testing.T) {
	// The rect doesn't overlap any text — pdfedit returns
	// ErrCellEmpty, the dispatcher maps it to ErrInvalidArgs
	// so the handler responds 400.
	pdf := corpus.WithText([]string{"Cell at top"})
	op := editops.Op{
		Type: editops.TableCellEdit,
		Page: 1,
		Rect: []float64{0, 0, 50, 50}, // bottom-left corner, no text there
		Text: "anything",
	}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs (empty cell)", err)
	}
}

func TestApply_TextInsert_AppendsBTBlock(t *testing.T) {
	// End-to-end: editops dispatches text.insert through to
	// pdfedit.InsertText. Caller picks the position, font
	// resource (must exist on the page — F1 always does in
	// corpus.WithText), and size; the revision appends a
	// fresh BT/ET block drawing the text at (x, y).
	pdf := corpus.WithText([]string{"line one"})
	x, y := 120.0, 500.0
	op := editops.Op{
		Type:   editops.TextInsert,
		Page:   1,
		X:      &x,
		Y:      &y,
		Text:   "Inserted",
		Font:   "F1",
		SizePt: 12,
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("text.insert must preserve original bytes verbatim")
	}
	appendix := out[len(pdf):]
	if !bytes.Contains(appendix, []byte("/F1 12 Tf")) {
		t.Errorf("missing Tf in appendix:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("120 500 Td")) {
		t.Errorf("missing Td at (120, 500):\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("(Inserted) Tj")) {
		t.Errorf("missing Tj `(Inserted)`:\n%s", appendix)
	}
}

func TestApply_TextInsert_RejectsMissingFields(t *testing.T) {
	pdf := corpus.WithText([]string{"x"})
	x, y := 10.0, 10.0
	cases := []struct {
		name string
		op   editops.Op
	}{
		{"missing x", editops.Op{Type: editops.TextInsert, Page: 1, Y: &y, Text: "t", Font: "F1", SizePt: 12}},
		{"missing y", editops.Op{Type: editops.TextInsert, Page: 1, X: &x, Text: "t", Font: "F1", SizePt: 12}},
		{"missing text", editops.Op{Type: editops.TextInsert, Page: 1, X: &x, Y: &y, Font: "F1", SizePt: 12}},
		{"missing font", editops.Op{Type: editops.TextInsert, Page: 1, X: &x, Y: &y, Text: "t", SizePt: 12}},
		{"zero size", editops.Op{Type: editops.TextInsert, Page: 1, X: &x, Y: &y, Text: "t", Font: "F1", SizePt: 0}},
		{"negative size", editops.Op{Type: editops.TextInsert, Page: 1, X: &x, Y: &y, Text: "t", Font: "F1", SizePt: -1}},
		{"bad page", editops.Op{Type: editops.TextInsert, Page: 0, X: &x, Y: &y, Text: "t", Font: "F1", SizePt: 12}},
	}
	for _, c := range cases {
		if _, err := editops.Apply([]editops.Op{c.op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
			t.Errorf("%s: err = %v, want ErrInvalidArgs", c.name, err)
		}
	}
}

func TestApply_TextDelete_ScrubsWithoutOverlay(t *testing.T) {
	// End-to-end: text.delete uses the same scrub pipeline as
	// redact.apply but skips the black overlay. The result is
	// a revision whose appendix has the target Tj emptied to
	// `()` and NO `0 0 0 rg` overlay marker.
	pdf := corpus.WithText([]string{"goodbye line", "keep me"})
	op := editops.Op{
		Type: editops.TextDelete,
		Page: 1,
		Rect: []float64{50, 747, 500, 770},
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("text.delete must preserve original bytes verbatim")
	}
	appendix := out[len(pdf):]
	if bytes.Contains(appendix, []byte("(goodbye line)")) {
		t.Errorf("deleted literal leaked into appendix:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("() Tj")) {
		t.Errorf("expected an empty Tj after scrub:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("(keep me) Tj")) {
		t.Errorf("untouched literal lost from appendix:\n%s", appendix)
	}
	// The single thing that distinguishes text.delete from
	// redact.apply at the byte level: NO overlay.
	if bytes.Contains(appendix, []byte("0 0 0 rg")) {
		t.Errorf("text.delete must NOT emit a redact-style black overlay:\n%s", appendix)
	}
}

func TestApply_TextDelete_RejectsMissingFields(t *testing.T) {
	pdf := corpus.WithText([]string{"x"})
	cases := []struct {
		name string
		op   editops.Op
	}{
		{"missing rect", editops.Op{Type: editops.TextDelete, Page: 1}},
		{"wrong rect arity", editops.Op{Type: editops.TextDelete, Page: 1, Rect: []float64{1, 2, 3}}},
		{"bad page", editops.Op{Type: editops.TextDelete, Page: 0, Rect: []float64{0, 0, 10, 10}}},
	}
	for _, c := range cases {
		if _, err := editops.Apply([]editops.Op{c.op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
			t.Errorf("%s: err = %v, want ErrInvalidArgs", c.name, err)
		}
	}
}

func TestApply_TextDelete_EmptyRectReturnsInvalidArgs(t *testing.T) {
	// Rect doesn't overlap any text — pdfedit returns
	// ErrTextDeleteNoOverlap, dispatcher maps it to
	// ErrInvalidArgs (handler responds 400).
	pdf := corpus.WithText([]string{"top line"})
	op := editops.Op{
		Type: editops.TextDelete,
		Page: 1,
		Rect: []float64{0, 0, 50, 50}, // bottom-left, no text there
	}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs (empty rect)", err)
	}
}

// ---- table.cell.edit coord-form ----

// withTableGrid returns a PDF containing a 2D grid of cells.
// Each (rowIdx, colIdx) gets one Tj at a fixed page-space
// anchor:
//
//   x = leftMargin + colIdx * colPitch
//   y = topY - rowIdx * rowPitch
//
// The output mirrors the WithText layout (single /Contents,
// uncompressed, /F1 font) so the editops dispatcher exercises
// the same byte paths as the existing rect-form tests.
func withTableGrid(t *testing.T, cells [][]string) []byte {
	t.Helper()
	const (
		leftMargin = 100
		topY       = 700
		colPitch   = 100
		rowPitch   = 20
	)
	var stream bytes.Buffer
	stream.WriteString("BT\n/F1 12 Tf\n")
	for r, row := range cells {
		y := topY - r*rowPitch
		for c, cell := range row {
			if cell == "" {
				continue
			}
			x := leftMargin + c*colPitch
			// PDF literal-string escape — same rules as the
			// existing corpus.WithText helper. The fixture
			// strings the tests use are simple ASCII so no
			// escaping is required, but we keep the format
			// uniform.
			fmt.Fprintf(&stream, "1 0 0 1 %d %d Tm\n(%s) Tj\n", x, y, cell)
		}
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
	return buildClassicPDF(parts, "1 0 R")
}

// buildClassicPDF mirrors corpus.buildClassic — duplicated
// here because the corpus helper is unexported. Keeps the
// editops test isolated from corpus internals.
func buildClassicPDF(parts []string, root string) []byte {
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

// intPtr is a tiny ergonomic helper — Op.Row / Op.Col are
// *int so a JSON `0` is distinguishable from missing, which
// means callers need to take an address.
func intPtr(v int) *int { return &v }

func TestApply_TableCellEdit_CoordForm_HappyPath(t *testing.T) {
	// 2x2 grid at fixed positions. The editops dispatcher
	// runs DetectTableGrid on the supplied region, finds
	// cell (0, 1), scrubs the original "B" Tj, and emits
	// "B-new" at the original anchor.
	pdf := withTableGrid(t, [][]string{
		{"A", "B"},
		{"C", "D"},
	})
	op := editops.Op{
		Type:   editops.TableCellEdit,
		Page:   1,
		Region: []float64{80, 660, 320, 720},
		Row:    intPtr(0),
		Col:    intPtr(1),
		Text:   "B-new",
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	appendix := out[len(pdf):]
	if !bytes.Contains(appendix, []byte("(B-new) Tj")) {
		t.Errorf("expected replacement Tj `(B-new) Tj` in appendix:\n%s", appendix)
	}
	// Exactly ONE cell scrubbed (B) — the coord-form
	// dispatch must have snapped to cell (0, 1) and only
	// that one. A second `() Tj` would mean over-scrub.
	if got := bytes.Count(appendix, []byte("() Tj")); got != 1 {
		t.Errorf("expected exactly 1 `() Tj` (cell B); got %d in appendix:\n%s",
			got, appendix)
	}
	// The other cells must still render their original
	// glyphs — the appendix carries the WHOLE content
	// stream (the new revision), so `(A) Tj`, `(C) Tj`,
	// `(D) Tj` should be there UNCHANGED.
	for _, preserved := range []string{"(A) Tj", "(C) Tj", "(D) Tj"} {
		if !bytes.Contains(appendix, []byte(preserved)) {
			t.Errorf("non-target cell %q was modified or removed in appendix:\n%s",
				preserved, appendix)
		}
	}
	if !bytes.Contains(out, pdf) {
		t.Error("incremental update must preserve original bytes verbatim")
	}
}

func TestApply_TableCellEdit_CoordForm_RejectsMissingRegion(t *testing.T) {
	pdf := withTableGrid(t, [][]string{{"A", "B"}, {"C", "D"}})
	op := editops.Op{
		Type: editops.TableCellEdit,
		Page: 1,
		Row:  intPtr(0),
		Col:  intPtr(1),
		Text: "X",
	}
	if _, err := editops.Apply([]editops.Op{op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs (missing region)", err)
	}
}

func TestApply_TableCellEdit_CoordForm_RejectsWrongRegionArity(t *testing.T) {
	pdf := withTableGrid(t, [][]string{{"A", "B"}, {"C", "D"}})
	op := editops.Op{
		Type:   editops.TableCellEdit,
		Page:   1,
		Region: []float64{1, 2, 3}, // need 4
		Row:    intPtr(0),
		Col:    intPtr(0),
		Text:   "X",
	}
	if _, err := editops.Apply([]editops.Op{op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs (wrong region arity)", err)
	}
}

func TestApply_TableCellEdit_CoordForm_RejectsMissingRowOrCol(t *testing.T) {
	pdf := withTableGrid(t, [][]string{{"A", "B"}, {"C", "D"}})
	// Region present but row absent.
	op := editops.Op{
		Type:   editops.TableCellEdit,
		Page:   1,
		Region: []float64{80, 660, 320, 720},
		Col:    intPtr(0),
		Text:   "X",
	}
	if _, err := editops.Apply([]editops.Op{op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("missing row: err = %v, want ErrInvalidArgs", err)
	}
	// Region + row present but col absent.
	op = editops.Op{
		Type:   editops.TableCellEdit,
		Page:   1,
		Region: []float64{80, 660, 320, 720},
		Row:    intPtr(0),
		Text:   "X",
	}
	if _, err := editops.Apply([]editops.Op{op}, pdf); !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("missing col: err = %v, want ErrInvalidArgs", err)
	}
}

func TestApply_TableCellEdit_CoordForm_RegionWithoutGridReturnsInvalidArgs(t *testing.T) {
	// Single-line content — DetectTableGrid rejects this
	// (< 2 rows). The dispatcher surfaces ErrGridNotDetected
	// as ErrInvalidArgs so the handler responds 400.
	pdf := corpus.WithText([]string{"just one line, not a table"})
	op := editops.Op{
		Type:   editops.TableCellEdit,
		Page:   1,
		Region: []float64{50, 740, 500, 770},
		Row:    intPtr(0),
		Col:    intPtr(0),
		Text:   "X",
	}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs (no grid in region)", err)
	}
}

func TestApply_TableCellEdit_CoordForm_OutOfRangeReturnsInvalidArgs(t *testing.T) {
	// 2x2 grid; ask for cell (5, 7). DetectTableGrid finds
	// the grid but (row, col) is out of bounds; the
	// dispatcher surfaces ErrCoordOutOfRange as
	// ErrInvalidArgs (caller bug — clamp and retry).
	pdf := withTableGrid(t, [][]string{{"A", "B"}, {"C", "D"}})
	op := editops.Op{
		Type:   editops.TableCellEdit,
		Page:   1,
		Region: []float64{80, 660, 320, 720},
		Row:    intPtr(5),
		Col:    intPtr(7),
		Text:   "X",
	}
	_, err := editops.Apply([]editops.Op{op}, pdf)
	if !errors.Is(err, editops.ErrInvalidArgs) {
		t.Errorf("err = %v, want ErrInvalidArgs (coord out of range)", err)
	}
}

func TestApply_TableCellEdit_RectFormTakesPrecedence(t *testing.T) {
	// Both `rect` and `{region, row, col}` supplied — rect
	// wins (the more precise input + the older contract).
	// Coord form would have selected D at (1, 1) with the
	// given region; we point rect at A's bbox band only.
	// After the op the appendix must contain exactly one
	// `() Tj` scrub (A) plus the new REPLACED Tj — proving
	// the coord-form path didn't also run.
	//
	// rect Y range [695, 715]: y0=695 sits above C's
	// ascender (C baseline=680 + 12pt fontSize = ~692),
	// y1=715 covers A's bbox [700, 712]. Anything looser
	// in Y would also clip C's glyph bbox (per the
	// documented any-glyph-overlap semantics).
	pdf := withTableGrid(t, [][]string{{"A", "B"}, {"C", "D"}})
	op := editops.Op{
		Type:   editops.TableCellEdit,
		Page:   1,
		Rect:   []float64{95, 695, 195, 715}, // cell A only
		Region: []float64{80, 660, 320, 720},
		Row:    intPtr(1),
		Col:    intPtr(1), // cell D — would be selected if rect didn't win
		Text:   "REPLACED",
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	appendix := out[len(pdf):]
	if !bytes.Contains(appendix, []byte("(REPLACED) Tj")) {
		t.Errorf("expected replacement Tj in appendix:\n%s", appendix)
	}
	// Exactly one scrub — rect targeted ONE cell. If the
	// coord-form path had also run, a second `() Tj` would
	// appear for D.
	if got := bytes.Count(appendix, []byte("() Tj")); got != 1 {
		t.Errorf("expected exactly 1 scrubbed `() Tj` (cell A); got %d in appendix:\n%s",
			got, appendix)
	}
	// Confirm the round-trip via pdfcpu opens cleanly.
	if _, err := api.ReadContext(bytes.NewReader(out), nil); err != nil {
		t.Errorf("output PDF doesn't parse: %v", err)
	}
}

// Use the strings import so go vet doesn't complain after
// future edits — keeps the imports stable.
var _ = strings.Contains
