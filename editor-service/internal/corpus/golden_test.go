package corpus_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"editor-service/internal/corpus"
	"editor-service/internal/editops"
)

// The golden suite is the central conformance test for the editor
// pipeline: every shipped op type runs against a representative
// fixture from the corpus, and the result must (a) parse through
// pdfcpu without error and (b) satisfy a per-op invariant (e.g.
// "/Annots has one more entry than before"). When a translator
// regresses, this is the test that catches it — without having
// to chase the same assertion across 5 different per-op test
// files.
//
// Each subtest is a (fixture, op) pair. New ops add their case
// to the switch; new fixtures add a Corpus entry below.

type fixture struct {
	name  string
	bytes []byte
	pages int
}

func allFixtures() []fixture {
	return []fixture{
		{name: "minimal", bytes: corpus.Minimal(), pages: 1},
		{name: "multipage-3", bytes: corpus.MultiPage(3), pages: 3},
		{name: "with-text", bytes: corpus.WithText([]string{"hello", "world"}), pages: 1},
	}
}

// readContext is a tiny helper: every op-output assertion starts
// the same way (read + EnsurePageCount), so factor it out.
func readContext(t *testing.T, b []byte) *pdfcpuContext {
	t.Helper()
	ctx, err := api.ReadContext(bytes.NewReader(b), nil)
	if err != nil {
		t.Fatalf("pdfcpu rejected revision bytes: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	return &pdfcpuContext{c: ctx}
}

// pdfcpuContext is a thin wrapper so the helper signature
// doesn't drag pdfcpu's import path into every assertion line.
type pdfcpuContext struct {
	c *model.Context
}

func (p *pdfcpuContext) pageCount() int { return p.c.PageCount }

func (p *pdfcpuContext) annotCount(t *testing.T, page int) int {
	t.Helper()
	dict, _, _, err := p.c.PageDict(page, false)
	if err != nil {
		t.Fatalf("PageDict(%d): %v", page, err)
	}
	v, ok := dict.Find("Annots")
	if !ok {
		return 0
	}
	arr, ok := v.(types.Array)
	if !ok {
		return 0
	}
	return len(arr)
}

// All fixtures must validate through pdfcpu as-is. This catches
// generator bugs (bad xref offsets, missing /Size, etc.) before
// any op-level test runs.
func TestCorpus_FixturesValidate(t *testing.T) {
	for _, f := range allFixtures() {
		t.Run(f.name, func(t *testing.T) {
			ctx := readContext(t, f.bytes)
			if ctx.pageCount() != f.pages {
				t.Errorf("PageCount = %d, want %d", ctx.pageCount(), f.pages)
			}
		})
	}
}

func TestGolden_PageRotate(t *testing.T) {
	for _, f := range allFixtures() {
		t.Run(f.name, func(t *testing.T) {
			op := editops.Op{Type: editops.PageRotate, Page: 1, Rotation: 90}
			out, err := editops.Apply([]editops.Op{op}, f.bytes)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !bytes.HasPrefix(out, f.bytes) {
				t.Error("original bytes must be preserved verbatim")
			}
			ctx := readContext(t, out)
			if ctx.pageCount() != f.pages {
				t.Errorf("rotate changed page count: %d → %d", f.pages, ctx.pageCount())
			}
		})
	}
}

func TestGolden_PageInsert(t *testing.T) {
	for _, f := range allFixtures() {
		t.Run(f.name, func(t *testing.T) {
			after := 0 // insert before first page — works for every fixture
			op := editops.Op{Type: editops.PageInsert, AfterPage: &after}
			out, err := editops.Apply([]editops.Op{op}, f.bytes)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			ctx := readContext(t, out)
			if ctx.pageCount() != f.pages+1 {
				t.Errorf("PageCount = %d, want %d (one inserted)", ctx.pageCount(), f.pages+1)
			}
		})
	}
}

func TestGolden_PageDelete(t *testing.T) {
	// Skip the single-page minimal fixture: the translator refuses
	// to delete the last page, which is correct behaviour, not a
	// regression to catch in the golden suite.
	for _, f := range allFixtures() {
		if f.pages < 2 {
			continue
		}
		t.Run(f.name, func(t *testing.T) {
			op := editops.Op{Type: editops.PageDelete, Page: 1}
			out, err := editops.Apply([]editops.Op{op}, f.bytes)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			ctx := readContext(t, out)
			if ctx.pageCount() != f.pages-1 {
				t.Errorf("PageCount = %d, want %d (one deleted)", ctx.pageCount(), f.pages-1)
			}
		})
	}
}

// AnnotationAdd_AllKinds exercises every shipped wire kind against
// the minimal fixture (the only one without its own /Annots that
// would complicate before/after counting). Per-fixture combos are
// handled by the kind-agnostic per-op golden test below.
func TestGolden_AnnotationAdd_AllKinds(t *testing.T) {
	// Each entry is one op spec; build them inline so adding a new
	// kind is a 4-line change (no helper plumbing to wire through).
	rect := []float64{100, 100, 200, 120}
	anchor := [2]float64{50, 50}
	ops := []struct {
		kind string
		op   editops.Op
	}{
		{"highlight", editops.Op{Type: editops.AnnotationAdd, Page: 1, Kind: "highlight", Rect: rect}},
		{"underline", editops.Op{Type: editops.AnnotationAdd, Page: 1, Kind: "underline", Rect: rect}},
		{"strikeout", editops.Op{Type: editops.AnnotationAdd, Page: 1, Kind: "strikeout", Rect: rect}},
		{"squiggly", editops.Op{Type: editops.AnnotationAdd, Page: 1, Kind: "squiggly", Rect: rect}},
		{"square", editops.Op{Type: editops.AnnotationAdd, Page: 1, Kind: "square", Rect: rect}},
		{"sticky", editops.Op{Type: editops.AnnotationAdd, Page: 1, Kind: "sticky", Rect: rect}},
		{"freehand", editops.Op{Type: editops.AnnotationAdd, Page: 1, Kind: "freehand",
			Strokes: [][]float64{{100, 100, 150, 100, 150, 150}}}},
		{"callout", editops.Op{Type: editops.AnnotationAdd, Page: 1, Kind: "callout",
			Rect: rect, Anchor: &anchor, Contents: "see"}},
	}
	for _, tc := range ops {
		t.Run(tc.kind, func(t *testing.T) {
			pdf := corpus.Minimal()
			out, err := editops.Apply([]editops.Op{tc.op}, pdf)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			ctx := readContext(t, out)
			if got := ctx.annotCount(t, 1); got != 1 {
				t.Errorf("annot count = %d, want 1 after add(kind=%s)", got, tc.kind)
			}
		})
	}
}

// RedactApply must produce parseable PDF bytes, scrub the targeted
// text from the content stream, and append a black-fill overlay.
// Runs against the with-text fixture (the only one whose glyphs we
// can place a rect around deterministically — corpus.WithText puts
// each line on a known baseline).
func TestGolden_RedactApply(t *testing.T) {
	pdf := corpus.WithText([]string{"redact me", "keep me"})
	// First baseline at y=750 (glyph bbox y=[750, 762]); second
	// baseline at y=734 (glyph bbox y=[734, 746]). v1's any-
	// glyph-overlap semantics need the rect to start ABOVE 746
	// to leave line 2 alone — Y0=747 gives a 1pt margin.
	op := editops.Op{
		Type: editops.RedactApply,
		Page: 1,
		Rect: []float64{50, 747, 500, 770},
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("redact.apply must preserve original bytes verbatim")
	}
	ctx := readContext(t, out)
	if ctx.pageCount() != 1 {
		t.Errorf("redact changed page count: 1 → %d", ctx.pageCount())
	}
	appendix := out[len(pdf):]
	if bytes.Contains(appendix, []byte("(redact me)")) {
		t.Errorf("redacted literal leaked into appendix:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("(keep me) Tj")) {
		t.Errorf("untouched literal lost from appendix:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("0 0 0 rg")) {
		t.Errorf("missing overlay marker:\n%s", appendix)
	}
}

func TestGolden_TextInsert(t *testing.T) {
	pdf := corpus.WithText([]string{"existing line"})
	x, y := 80.0, 600.0
	op := editops.Op{
		Type:   editops.TextInsert,
		Page:   1,
		X:      &x,
		Y:      &y,
		Text:   "Inserted at 600",
		Font:   "F1",
		SizePt: 14,
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("text.insert must preserve original bytes verbatim")
	}
	ctx := readContext(t, out)
	if ctx.pageCount() != 1 {
		t.Errorf("text.insert changed page count: 1 → %d", ctx.pageCount())
	}
	appendix := out[len(pdf):]
	if !bytes.Contains(appendix, []byte("(Inserted at 600) Tj")) {
		t.Errorf("inserted literal missing from appendix:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("/F1 14 Tf")) {
		t.Errorf("Tf with the requested font/size missing:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("80 600 Td")) {
		t.Errorf("Td at the requested anchor missing:\n%s", appendix)
	}
	// Pre-existing text must be untouched — text.insert only
	// appends, never modifies prior bytes.
	if !bytes.Contains(out, []byte("(existing line)")) {
		t.Error("text.insert must not disturb prior content")
	}
}

func TestGolden_TextDelete(t *testing.T) {
	pdf := corpus.WithText([]string{"delete me", "keep me"})
	// Same anchor strategy as the redact suite: line 1 at y=750
	// (glyph bbox y=[750, 762]); line 2 at y=734. Y0=747 leaves
	// line 2 alone.
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
	ctx := readContext(t, out)
	if ctx.pageCount() != 1 {
		t.Errorf("text.delete changed page count: 1 → %d", ctx.pageCount())
	}
	appendix := out[len(pdf):]
	if bytes.Contains(appendix, []byte("(delete me)")) {
		t.Errorf("deleted literal leaked into appendix:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("(keep me) Tj")) {
		t.Errorf("untouched literal lost from appendix:\n%s", appendix)
	}
	// text.delete's distinguishing feature vs redact.apply: NO
	// black-fill overlay marker.
	if bytes.Contains(appendix, []byte("0 0 0 rg")) {
		t.Errorf("text.delete must NOT emit a redact-style overlay:\n%s", appendix)
	}
}

func TestGolden_TableCellEdit(t *testing.T) {
	pdf := corpus.WithText([]string{"Old Cell", "keep me"})
	// First baseline at y=750 (glyph bbox y=[750, 762]); second
	// baseline at y=734. Same rect strategy as the redact suite:
	// Y0=747 leaves line 2 alone.
	op := editops.Op{
		Type: editops.TableCellEdit,
		Page: 1,
		Rect: []float64{50, 747, 500, 770},
		Text: "New Cell",
	}
	out, err := editops.Apply([]editops.Op{op}, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("table.cell.edit must preserve original bytes verbatim")
	}
	ctx := readContext(t, out)
	if ctx.pageCount() != 1 {
		t.Errorf("cell edit changed page count: 1 → %d", ctx.pageCount())
	}
	appendix := out[len(pdf):]
	if bytes.Contains(appendix, []byte("(Old Cell)")) {
		t.Errorf("original cell text leaked into appendix:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("(keep me) Tj")) {
		t.Errorf("untouched literal lost from appendix:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("(New Cell) Tj")) {
		t.Errorf("replacement literal missing from appendix:\n%s", appendix)
	}
	if !bytes.Contains(appendix, []byte("/F1 12 Tf")) {
		t.Errorf("replacement should re-emit original font Tf; got:\n%s", appendix)
	}
}

// MultiOp asserts that 1–N ops chain cleanly: each one re-validates
// the prior op's output and produces incremental sections that
// stack. Specifically: rotate → annotate → insert blank page on
// the minimal fixture should yield a 2-page doc with page 1
// rotated 90° and one annotation. This is the "do everything"
// smoke test — catches any case where one op's output happens to
// not be a valid input for the next.
func TestGolden_MultiOp_Chains(t *testing.T) {
	pdf := corpus.Minimal()
	after := 1
	ops := []editops.Op{
		{Type: editops.PageRotate, Page: 1, Rotation: 90},
		{Type: editops.AnnotationAdd, Page: 1, Kind: "highlight",
			Rect: []float64{50, 50, 100, 70}},
		{Type: editops.PageInsert, AfterPage: &after},
	}
	out, err := editops.Apply(ops, pdf)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ctx := readContext(t, out)
	if ctx.pageCount() != 2 {
		t.Errorf("PageCount = %d, want 2", ctx.pageCount())
	}
	if got := ctx.annotCount(t, 1); got != 1 {
		t.Errorf("annot count on page 1 = %d, want 1", got)
	}
}

// PreservesOriginalBytes is the strongest correctness invariant:
// every editor op must emit incremental updates, never rewrite
// the prefix. A revision that breaks this would invalidate any
// digital signature over the prior bytes and defeat the audit-log
// chain (plan §3.10).
func TestGolden_AllOpsPreserveOriginalBytes(t *testing.T) {
	pdf := corpus.Minimal()
	after := 0
	ops := []editops.Op{
		{Type: editops.PageRotate, Page: 1, Rotation: 90},
		{Type: editops.PageInsert, AfterPage: &after},
		{Type: editops.AnnotationAdd, Page: 1, Kind: "highlight",
			Rect: []float64{10, 10, 50, 30}},
	}
	for _, op := range ops {
		t.Run(string(op.Type), func(t *testing.T) {
			out, err := editops.Apply([]editops.Op{op}, pdf)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !bytes.HasPrefix(out, pdf) {
				t.Errorf("%s did not preserve original bytes verbatim", op.Type)
			}
			if len(out) <= len(pdf) {
				t.Errorf("%s did not append (output shorter or equal: %d vs %d)",
					op.Type, len(out), len(pdf))
			}
		})
	}
}

// WithText_TextExtractable is a sanity test that the with-text
// fixture actually carries the text we ask for — without this,
// sPDOM regressions could ship undetected because the generator
// would emit a syntactically-valid PDF with no glyphs.
func TestCorpus_WithText_ContainsExpectedString(t *testing.T) {
	pdf := corpus.WithText([]string{"GoldenSuiteMarker", "second line"})
	// The literal strings (escaped) end up verbatim in the content
	// stream — no font subsetting in the fixture, no glyph
	// encoding indirection. Grep-style check is sufficient.
	if !strings.Contains(string(pdf), "GoldenSuiteMarker") {
		t.Error("expected literal string not found in fixture bytes")
	}
}
