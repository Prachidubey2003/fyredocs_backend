package spdom

import (
	"bytes"
	"strings"
	"testing"
)

// rectAt returns a Rect spanning [x, x+w] × [y, y+h]. Tests use it
// to centre redaction on the exact baseline coordinates each
// content stream sets via Tm/Td.
func rectAt(x, y, w, h float64) Rect {
	return Rect{X0: x, Y0: y, X1: x + w, Y1: y + h}
}

func TestScrubRect_TjInsideRectIsCleared(t *testing.T) {
	// Tj at (100, 700) is fully inside the rect — its string
	// operand `(Secret)` should become `()` byte-for-byte. The
	// trailing operator + whitespace must survive.
	stream := []byte("BT /F1 12 Tf 100 700 Td (Secret) Tj ET")
	got := ScrubRect(stream, rectAt(50, 650, 200, 100))
	if !bytes.Contains(got, []byte("() Tj")) {
		t.Errorf("scrubbed stream = %q\nwant `() Tj` somewhere", got)
	}
	if bytes.Contains(got, []byte("Secret")) {
		t.Errorf("scrubbed stream still contains `Secret`: %q", got)
	}
}

func TestScrubRect_TjOutsideRectUntouched(t *testing.T) {
	// Tj at (100, 700) — rect is far away. Stream should come
	// through byte-for-byte.
	stream := []byte("BT /F1 12 Tf 100 700 Td (Visible) Tj ET")
	got := ScrubRect(stream, rectAt(0, 0, 10, 10))
	if !bytes.Equal(got, stream) {
		t.Errorf("out-of-rect text was modified.\ngot:  %q\nwant: %q", got, stream)
	}
}

func TestScrubRect_TJArrayInsideRectIsCleared(t *testing.T) {
	// TJ at (100, 700) — array `[(Hel) -100 (lo)]`. Both strings
	// are inside the rect, so v2 per-item surgery scrubs each
	// string operand to `()` while leaving the kerning number
	// `-100` and the `[ ]` delimiters in place. The result is
	// `[ () -100 () ] TJ` rather than the v1 `[] TJ`.
	stream := []byte("BT /F1 12 Tf 100 700 Td [ (Hel) -100 (lo) ] TJ ET")
	got := ScrubRect(stream, rectAt(50, 650, 200, 100))
	if bytes.Contains(got, []byte("Hel")) || bytes.Contains(got, []byte("lo")) {
		t.Errorf("array contents survived scrub: %q", got)
	}
	if !bytes.Contains(got, []byte("-100")) {
		t.Errorf("kerning number should be preserved verbatim; got %q", got)
	}
	if !bytes.Contains(got, []byte("[ () -100 () ]")) {
		t.Errorf("expected per-item scrubbed array `[ () -100 () ]`; got %q", got)
	}
}

func TestScrubRect_MultiOpStreamScrubsOnlyInsideOps(t *testing.T) {
	// Two Tj ops on the same page; only the second is inside the
	// rect. The first should be kept verbatim.
	stream := []byte(
		"BT /F1 12 Tf 50 50 Td (Kept) Tj " +
			"500 500 Td (Hidden) Tj ET",
	)
	// Note: "500 500 Td" is RELATIVE to the previous Tlm of (50,50),
	// so the second emit happens at (50+500, 50+500) = (550, 550).
	got := ScrubRect(stream, rectAt(500, 500, 200, 200))
	if !bytes.Contains(got, []byte("(Kept) Tj")) {
		t.Errorf("first (outside) Tj was lost: %q", got)
	}
	if bytes.Contains(got, []byte("(Hidden)")) {
		t.Errorf("second (inside) Tj was not scrubbed: %q", got)
	}
	if !bytes.Contains(got, []byte("() Tj")) {
		t.Errorf("expected an empty Tj operand after scrub: %q", got)
	}
}

func TestScrubRect_DegenerateRectIsNoOp(t *testing.T) {
	// X1 <= X0 or Y1 <= Y0 — return the input unchanged. Guards
	// against accidental scrubs from callers that didn't normalise
	// their rect.
	stream := []byte("BT /F1 12 Tf 100 700 Td (Hi) Tj ET")
	for _, r := range []Rect{
		{X0: 100, Y0: 100, X1: 100, Y1: 200}, // zero width
		{X0: 100, Y0: 100, X1: 200, Y1: 100}, // zero height
		{X0: 200, Y0: 100, X1: 100, Y1: 200}, // inverted X
	} {
		got := ScrubRect(stream, r)
		if !bytes.Equal(got, stream) {
			t.Errorf("degenerate rect %+v modified stream: %q", r, got)
		}
	}
}

func TestScrubRect_NonTextOpsUntouched(t *testing.T) {
	// A content stream with image + path ops next to a Tj. Only
	// the Tj should be touched.
	stream := []byte(
		"q 1 0 0 1 0 0 cm /Im1 Do Q\n" +
			"BT /F1 12 Tf 100 700 Td (Hi) Tj ET\n" +
			"100 100 200 200 re S\n",
	)
	got := ScrubRect(stream, rectAt(50, 650, 200, 100))
	// Image op survives.
	if !bytes.Contains(got, []byte("/Im1 Do")) {
		t.Errorf("XObject Do op was dropped: %q", got)
	}
	// Path op survives.
	if !bytes.Contains(got, []byte("100 100 200 200 re S")) {
		t.Errorf("path op was dropped: %q", got)
	}
	// Text scrubbed.
	if bytes.Contains(got, []byte("(Hi)")) {
		t.Errorf("Tj was not scrubbed: %q", got)
	}
}

func TestScrubRect_RotatedTextInsideRectIsScrubbed(t *testing.T) {
	// v1: rotated text is now scrubbed when its bbox overlaps the
	// redact rect. (v0 used to leave it alone, leaving the bytes
	// recoverable under the visual overlay — a privacy leak.)
	//
	// Setup: 90° rotation around origin (100, 100) with Tm
	// `0 1 -1 0 100 100`. The "Rot" glyphs extend along the
	// rotated baseline from (100, 100). With a generous rect
	// covering the page, the bbox MUST overlap.
	stream := []byte("BT /F1 12 Tf 0 1 -1 0 100 100 Tm (Rot) Tj ET")
	got := ScrubRect(stream, rectAt(0, 0, 1000, 1000))
	if bytes.Contains(got, []byte("Rot")) {
		t.Errorf("rotated text leaked into output: %q", got)
	}
	if !bytes.Contains(got, []byte("() Tj")) {
		t.Errorf("expected empty Tj operand after scrub; got %q", got)
	}
}

func TestScrubRect_RotatedTextOutsideRectIsKept(t *testing.T) {
	// Negative case: rotated text whose bbox does NOT overlap the
	// rect must survive. The 90° rotation puts "Rot" along a
	// vertical baseline starting at (100, 100). A small rect far
	// away (300, 600, 400, 700) should leave it untouched.
	stream := []byte("BT /F1 12 Tf 0 1 -1 0 100 100 Tm (Rot) Tj ET")
	got := ScrubRect(stream, rectAt(300, 600, 100, 100))
	if !bytes.Equal(got, stream) {
		t.Errorf("rotated text outside rect was modified.\ngot:  %q\nwant: %q", got, stream)
	}
}

func TestScrubRect_PreservesByteLayoutOutsideScrubRanges(t *testing.T) {
	// The bytes BEFORE and AFTER the scrubbed Tj should match the
	// original byte-for-byte. Guards against a regression where
	// the splicer trims whitespace or rewrites unrelated tokens.
	stream := []byte("BT /F1 12 Tf 100 700 Td   (Sec)  Tj  ET")
	got := ScrubRect(stream, rectAt(50, 650, 200, 100))
	// Tokens around the scrubbed string must still appear.
	for _, marker := range []string{
		"BT", "/F1", "12", "Tf", "100", "700", "Td", "Tj", "ET",
	} {
		if !bytes.Contains(got, []byte(marker)) {
			t.Errorf("token %q dropped after scrub: %q", marker, got)
		}
	}
	// The string operand `(Sec)` is gone.
	if bytes.Contains(got, []byte("Sec")) {
		t.Errorf("scrubbed text leaked: %q", got)
	}
}

func TestScrubRect_NextLineOperatorTickStillScrubbed(t *testing.T) {
	// `'` is "next-line + show". After Td(100 700) with TL 14, the
	// tick drops to y=686 then shows. Place the rect to catch the
	// SECOND show's origin.
	stream := []byte("BT /F1 12 Tf 14 TL 100 700 Td (A) Tj (B) ' ET")
	// First Tj: at (100, 700). Width advance ≈ 6pt (0.5em fallback
	// since /F1 is opaque). So second show is at ~106, 686.
	// Rect at (100, 680, 200, 700) catches the second tick.
	got := ScrubRect(stream, rectAt(100, 680, 200, 20))
	// First Tj at (100, 700) — y=700 IS within [680, 700], so it
	// also gets scrubbed.
	if !bytes.Contains(got, []byte("() Tj")) {
		t.Errorf("expected at least one empty Tj after scrub: %q", got)
	}
	if !strings.Contains(string(got), "() '") {
		t.Errorf("expected an empty operand on the ' operator: %q", got)
	}
}

// ---- v1: partial overlap + arbitrary-orientation tests --------------------

func TestScrubRect_TjPartialOverlapIsScrubbed(t *testing.T) {
	// v1: a Tj whose ORIGIN is outside the rect but whose glyphs
	// extend INTO it must be scrubbed. v0 only scrubbed origin-in-
	// rect ops, leaking text that crossed the boundary. The new
	// "any overlap" semantics close that hole.
	//
	// Setup: a long Tj starting at x=50, extending past x=400 via
	// the 0.5em fallback (each char ≈ 6pt at 12pt). A rect at
	// [200, 690, 400, 710] sits in the MIDDLE of the run.
	stream := []byte("BT /F1 12 Tf 50 700 Td (long sensitive payroll info goes here) Tj ET")
	got := ScrubRect(stream, Rect{X0: 200, Y0: 690, X1: 400, Y1: 710})
	if bytes.Contains(got, []byte("payroll")) {
		t.Errorf("partial-overlap leak: %q", got)
	}
	if !bytes.Contains(got, []byte("() Tj")) {
		t.Errorf("expected empty Tj after scrub: %q", got)
	}
}

func TestScrubRect_SkewedTextOverlappingRectIsScrubbed(t *testing.T) {
	// Skewed text matrix [1 0.5 0 1 …]. The 4-corner-bbox math
	// must handle the shear. With a rect that the skewed bbox
	// overlaps, the run is scrubbed.
	stream := []byte("BT /F1 12 Tf 1 0.5 0 1 100 200 Tm (Skewed) Tj ET")
	got := ScrubRect(stream, rectAt(50, 150, 300, 200))
	if bytes.Contains(got, []byte("Skewed")) {
		t.Errorf("skewed text leaked: %q", got)
	}
}

func TestScrubRect_MirroredTextOverlappingRectIsScrubbed(t *testing.T) {
	// 180° rotation (a=-1, d=-1) at origin (200, 200). Text
	// extends in the negative direction. The bbox covers
	// [200-w, 200] × [200-h, 200].
	stream := []byte("BT /F1 12 Tf -1 0 0 -1 200 200 Tm (Upside) Tj ET")
	got := ScrubRect(stream, rectAt(100, 150, 200, 100))
	if bytes.Contains(got, []byte("Upside")) {
		t.Errorf("mirrored text leaked: %q", got)
	}
}

func TestScrubRect_NinetyDegreeRotationAdvancesAlongRotatedAxis(t *testing.T) {
	// Two 90°-rotated Tj's chained — the second SHOULD land
	// above the first along the rotated baseline. Both originate
	// from the same vertical column, so a tall narrow rect that
	// catches the second but not the first proves the advance
	// helper handles rotation correctly.
	//
	// Tm = `0 1 -1 0 100 100` puts text along +Y from (100, 100).
	// "abc" at 12pt 0.5em = 18pt advance along +Y.
	// First Tj bbox in page space: text-space [0,18]×[0,12]
	// → Tm maps (x,y)→(100-y, 100+x), corners
	//   (0,0)→(100,100), (18,0)→(100,118),
	//   (0,12)→(88,100), (18,12)→(88,118)
	// AABB: x∈[88,100], y∈[100,118].
	// Second Tj starts at (100, 118), so its bbox is
	// x∈[88,100], y∈[118,136].
	// Pick a rect that catches the SECOND only:
	// y∈[125, 140] misses [100,118] (118 < 125 → no overlap)
	// and hits [118,136] (136 ≥ 125 and 118 ≤ 140 → overlap).
	stream := []byte("BT /F1 12 Tf 0 1 -1 0 100 100 Tm (abc) Tj (def) Tj ET")
	got := ScrubRect(stream, rectAt(80, 125, 50, 15))
	if !bytes.Contains(got, []byte("(abc)")) {
		t.Errorf("first rotated Tj at the bottom should survive; got %q", got)
	}
	if bytes.Contains(got, []byte("(def)")) {
		t.Errorf("second rotated Tj inside rect should be scrubbed; got %q", got)
	}
}

func TestScrubRect_TJArrayPartialOverlapScrubsOnlyOverlappingItems(t *testing.T) {
	// TJ array with three strings on the same baseline. v2
	// walks the array item-by-item and scrubs ONLY the strings
	// whose own glyph bbox overlaps the rect — items outside
	// the rect survive verbatim.
	//
	// Anchor (50, 700), font 12pt, F1 fallback (0.5em/glyph).
	// Item 1 "long first string here"  (22 chars) → bbox x ∈ [50, 50+132] = [50, 182]
	// kern -50           → cursor += 50/1000·12 = 0.6 → 182.6
	// Item 2 "long middle string here" (23 chars) → bbox x ∈ [182.6, 320.6]
	// kern -50           → cursor += 0.6 → 321.2
	// Item 3 "sensitive payroll data here" (27 chars) → bbox x ∈ [321.2, 483.2]
	//
	// Redact rect.x ∈ [200, 400]:
	//   item 1 [50, 182]      → NO overlap (182 < 200) → KEEP
	//   item 2 [182.6, 320.6] → OVERLAP            → SCRUB
	//   item 3 [321.2, 483.2] → OVERLAP            → SCRUB
	stream := []byte("BT /F1 12 Tf 50 700 Td [ (long first string here) -50 (long middle string here) -50 (sensitive payroll data here) ] TJ ET")
	got := ScrubRect(stream, Rect{X0: 200, Y0: 690, X1: 400, Y1: 710})
	if bytes.Contains(got, []byte("sensitive")) {
		t.Errorf("TJ partial-overlap leak: %q", got)
	}
	if bytes.Contains(got, []byte("middle")) {
		t.Errorf("overlapping middle item should be scrubbed: %q", got)
	}
	if !bytes.Contains(got, []byte("(long first string here)")) {
		t.Errorf("non-overlapping first item should survive verbatim; got %q", got)
	}
	if !bytes.Contains(got, []byte("-50")) {
		t.Errorf("kerning numbers should stay in place; got %q", got)
	}
}

func TestScrubRect_TJArrayLeavesNonOverlappingItemsAlone(t *testing.T) {
	// Inverse of the v1 over-redact case: when NONE of the
	// array items overlap the rect, the whole array must pass
	// through unchanged (kerns and strings both intact).
	stream := []byte("BT /F1 12 Tf 10 700 Td [ (alpha) -50 (beta) -50 (gamma) ] TJ ET")
	got := ScrubRect(stream, Rect{X0: 500, Y0: 690, X1: 600, Y1: 710})
	if !bytes.Equal(got, stream) {
		t.Errorf("non-overlapping TJ array should be untouched; got %q", got)
	}
}

func TestReplaceRectFirstAnchor_TJArrayCapturesFirstOverlappingItem(t *testing.T) {
	// Confirms the v2 TJ surgery also threads recordFirst()
	// correctly: when the first overlap is inside a TJ array
	// item (not a plain Tj), the anchor for replacement is
	// the cursor position at THAT item, not the TJ's start.
	//
	// Same fixture as the partial-overlap test: item 2 is the
	// first overlap, with cursor at x = 182.6.
	stream := []byte("BT /F1 12 Tf 50 700 Td [ (long first string here) -50 (long middle string here) -50 (sensitive payroll data here) ] TJ ET")
	out, ok := ReplaceRectFirstAnchor(stream, Rect{X0: 200, Y0: 690, X1: 400, Y1: 710}, "[redacted]")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	// Replacement BT block should anchor at the first overlapping
	// item's cursor (≈182.6). approxWidth's float formatting can
	// produce trailing decimals; assert on the prefix.
	if !bytes.Contains(out, []byte("Td\n([redacted]) Tj")) {
		t.Errorf("replacement Tj missing; got %q", out)
	}
	if !bytes.Contains(out, []byte("182.6 700 Td")) {
		t.Errorf("expected anchor `182.6 700 Td`; got %q", out)
	}
}

func TestScrubRect_LongTjStartingOutsideExtendingIntoRectScrubbed(t *testing.T) {
	// Pinned regression: a long Tj starting WELL outside the
	// rect (so origin-in-rect = false) but whose glyphs reach
	// INTO the rect. v0 left this alone; v1 scrubs it.
	stream := []byte("BT /F1 12 Tf 10 700 Td (this text starts left but extends right into the box) Tj ET")
	got := ScrubRect(stream, Rect{X0: 200, Y0: 690, X1: 400, Y1: 710})
	if bytes.Contains(got, []byte("extends")) {
		t.Errorf("under-redact leak: long-Tj starting outside but crossing rect was kept; got %q", got)
	}
}

func TestReplaceRectFirstAnchor_ScrubsAndAppendsBTBlock(t *testing.T) {
	// Page has a single Tj "Old". ReplaceRectFirstAnchor should
	// (a) scrub the Tj operand → `()` and (b) append a fresh BT
	// block drawing "New" at the original anchor (100, 700) in
	// the original font (F1 @ 12pt).
	stream := []byte("BT /F1 12 Tf 100 700 Td (Old) Tj ET")
	got, ok := ReplaceRectFirstAnchor(stream, Rect{X0: 90, Y0: 690, X1: 200, Y1: 720}, "New")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if !bytes.Contains(got, []byte("() Tj")) {
		t.Errorf("original Tj should be scrubbed to empty literal; got %q", got)
	}
	if !bytes.Contains(got, []byte("/F1 12 Tf")) {
		t.Errorf("replacement should re-emit /F1 12 Tf; got %q", got)
	}
	if !bytes.Contains(got, []byte("100 700 Td")) {
		t.Errorf("replacement should be anchored at (100, 700); got %q", got)
	}
	if !bytes.Contains(got, []byte("(New) Tj")) {
		t.Errorf("replacement Tj should draw \"New\"; got %q", got)
	}
}

func TestReplaceRectFirstAnchor_NoOverlapReturnsFalse(t *testing.T) {
	// The Tj sits at (100, 700); the target rect is at
	// (500, 500). No overlap → ok=false, bytes unchanged.
	stream := []byte("BT /F1 12 Tf 100 700 Td (Hello) Tj ET")
	got, ok := ReplaceRectFirstAnchor(stream, Rect{X0: 500, Y0: 500, X1: 600, Y1: 520}, "New")
	if ok {
		t.Errorf("expected ok=false for non-overlapping rect; got ok=true")
	}
	if !bytes.Equal(got, stream) {
		t.Errorf("non-overlap should return original bytes verbatim; got %q", got)
	}
}

func TestReplaceRectFirstAnchor_PicksFirstScrubbedOpAnchor(t *testing.T) {
	// Two Tj ops both inside the rect: anchors at (100, 700) and
	// (100, 680). The first scrubbed op (top of the cell, larger
	// Y) sets the replacement anchor — replacement draws at
	// (100, 700), NOT (100, 680).
	stream := []byte("BT /F1 12 Tf 100 700 Td (Top) Tj 0 -20 Td (Bottom) Tj ET")
	got, ok := ReplaceRectFirstAnchor(stream, Rect{X0: 90, Y0: 670, X1: 200, Y1: 720}, "Merged")
	if !ok {
		t.Fatalf("expected ok=true, got false")
	}
	if !bytes.Contains(got, []byte("100 700 Td")) {
		t.Errorf("replacement should anchor at first-scrubbed op's (100, 700); got %q", got)
	}
	if bytes.Contains(got, []byte("(Top)")) || bytes.Contains(got, []byte("(Bottom)")) {
		t.Errorf("both original Tjs should be scrubbed; got %q", got)
	}
	if !bytes.Contains(got, []byte("(Merged) Tj")) {
		t.Errorf("replacement Tj missing; got %q", got)
	}
}

func TestReplaceRectFirstAnchor_EscapesLiteralSpecials(t *testing.T) {
	// Replacement text containing PDF literal specials (parens,
	// backslash) must be escaped via encodeLiteral so the
	// appended BT block is well-formed.
	stream := []byte("BT /F1 12 Tf 100 700 Td (X) Tj ET")
	got, ok := ReplaceRectFirstAnchor(stream, Rect{X0: 90, Y0: 690, X1: 200, Y1: 720}, "Hello (world) \\test")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	// Expect escaped parens and backslash in the appended literal.
	if !bytes.Contains(got, []byte(`Hello \(world\) \\test`)) {
		t.Errorf("replacement literal not escaped correctly; got %q", got)
	}
}

func TestReplaceRectFirstAnchor_RotatedCellReplacementUsesMatchingTm(t *testing.T) {
	// 90°-rotated cell: Tm = `0 1 -1 0 200 300`. The original
	// glyphs render rotated; the replacement must also render
	// rotated, otherwise the cell visually flips to horizontal
	// after the edit. Verify the replacement BT block emits
	// the SAME 2×2 in a Tm operator (not the default Td form).
	stream := []byte("BT /F1 12 Tf 0 1 -1 0 200 300 Tm (Old) Tj ET")
	// Rect chosen to overlap the rotated glyph: 90° baseline
	// runs along +y, height extends -x. "Old" at 12pt ≈ 18pt
	// wide → bbox x ∈ [188, 200], y ∈ [300, 318]. Pick a rect
	// that comfortably covers it.
	got, ok := ReplaceRectFirstAnchor(stream, Rect{X0: 180, Y0: 290, X1: 210, Y1: 330}, "New")
	if !ok {
		t.Fatalf("expected ok=true; got bytes=%q", got)
	}
	if bytes.Contains(got, []byte("(Old)")) {
		t.Errorf("original literal leaked: %q", got)
	}
	// The replacement should emit the captured 2×2 as a Tm —
	// `0 1 -1 0 200 300 Tm`.
	if !bytes.Contains(got, []byte("0 1 -1 0 200 300 Tm")) {
		t.Errorf("replacement should emit the captured rotated Tm; got %q", got)
	}
	// And NOT the Td form (which would render horizontally).
	if bytes.Contains(got, []byte("200 300 Td")) {
		t.Errorf("replacement should not fall back to Td for rotated cells; got %q", got)
	}
	if !bytes.Contains(got, []byte("(New) Tj")) {
		t.Errorf("replacement Tj missing; got %q", got)
	}
}

func TestReplaceRectFirstAnchor_ScaledCellPreservesRenderedSize(t *testing.T) {
	// Uniform-scaled identity: Tm = `2 0 0 2 100 700` renders
	// Tf=12pt glyphs at 24pt. The replacement must keep that
	// scale so it visually matches the original — Td would
	// render at 12pt and look half-sized.
	stream := []byte("BT /F1 12 Tf 2 0 0 2 100 700 Tm (X) Tj ET")
	// Glyph bbox: 24pt-tall × ~12pt-wide at (100, 700).
	got, ok := ReplaceRectFirstAnchor(stream, Rect{X0: 90, Y0: 695, X1: 200, Y1: 750}, "Y")
	if !ok {
		t.Fatalf("expected ok=true; got bytes=%q", got)
	}
	if !bytes.Contains(got, []byte("2 0 0 2 100 700 Tm")) {
		t.Errorf("replacement should re-apply the captured scale via Tm; got %q", got)
	}
	if bytes.Contains(got, []byte("100 700 Td")) {
		t.Errorf("replacement must not collapse scale to Td; got %q", got)
	}
}

func TestReplaceRectFirstAnchor_HorizontalCellStillUsesTd(t *testing.T) {
	// Regression: when the captured Tm 2×2 is identity, we
	// keep emitting the shorter Td form. Confirms the
	// dispatcher branches correctly on isIdentity2x2.
	stream := []byte("BT /F1 12 Tf 1 0 0 1 100 700 Tm (Old) Tj ET")
	got, ok := ReplaceRectFirstAnchor(stream, Rect{X0: 90, Y0: 695, X1: 200, Y1: 720}, "New")
	if !ok {
		t.Fatalf("expected ok=true; got bytes=%q", got)
	}
	if !bytes.Contains(got, []byte("100 700 Td")) {
		t.Errorf("identity Tm should still use Td form; got %q", got)
	}
	// Sanity: no spurious Tm in the appended block.
	appended := got[len(stream):]
	if bytes.Contains(appended, []byte(" Tm\n")) {
		t.Errorf("identity 2×2 should not emit Tm in the replacement; got %q", appended)
	}
}

func TestIsIdentity2x2_Exhaustive(t *testing.T) {
	cases := []struct {
		name           string
		a, b, c, d     float64
		want           bool
	}{
		{"identity exact", 1, 0, 0, 1, true},
		{"identity with tiny noise", 1 + 1e-12, 1e-12, -1e-12, 1 - 1e-12, true},
		{"90° rotation", 0, 1, -1, 0, false},
		{"180° rotation", -1, 0, 0, -1, false},
		{"270° rotation", 0, -1, 1, 0, false},
		{"uniform 2x scale", 2, 0, 0, 2, false},
		{"y-mirror", 1, 0, 0, -1, false},
		{"non-uniform scale", 2, 0, 0, 3, false},
		{"shear (italic)", 1, 0, 0.2, 1, false},
		{"zero matrix", 0, 0, 0, 0, false},
	}
	for _, c := range cases {
		if got := isIdentity2x2(c.a, c.b, c.c, c.d); got != c.want {
			t.Errorf("%s: isIdentity2x2(%v,%v,%v,%v) = %v, want %v",
				c.name, c.a, c.b, c.c, c.d, got, c.want)
		}
	}
}

func TestBuildOrientedTextBlock_EmitsTmAndEscapesLiteral(t *testing.T) {
	// Hand-crafted spot check: the public builder used by the
	// scrub-and-replace path must include Tm with the right
	// values AND properly escape PDF literal specials.
	got := BuildOrientedTextBlock("F1", 14, 0, 1, -1, 0, 200, 300, "Hello (world) \\test")
	if !bytes.Contains(got, []byte("/F1 14 Tf\n0 1 -1 0 200 300 Tm")) {
		t.Errorf("missing Tf/Tm preamble: %q", got)
	}
	if !bytes.Contains(got, []byte(`Hello \(world\) \\test`)) {
		t.Errorf("literal not escaped: %q", got)
	}
	if !bytes.Contains(got, []byte(") Tj\nET\n")) {
		t.Errorf("missing Tj/ET closing: %q", got)
	}
}

// ---- WrapTextToWidth ----

func TestWrapTextToWidth_EmptyInputReturnsNil(t *testing.T) {
	if got := WrapTextToWidth("", "Helvetica", 12, 100); got != nil {
		t.Errorf("empty input: got %v, want nil", got)
	}
}

func TestWrapTextToWidth_ZeroOrNegativeWidthCollapsesToOneLine(t *testing.T) {
	// maxWidth <= 0 short-circuits to "no wrapping" — matches
	// the v0 EditTableCell non-wrap shape so callers without
	// a sensible cell width don't crash.
	got := WrapTextToWidth("one two three four", "Helvetica", 12, 0)
	if len(got) != 1 || got[0] != "one two three four" {
		t.Errorf("got %v, want single-line passthrough", got)
	}
}

func TestWrapTextToWidth_FitsOnSingleLineWhenWidthIsAmple(t *testing.T) {
	// "Hi" at 12pt Helvetica is ~12pt wide — easily fits in
	// 200pt.
	got := WrapTextToWidth("Hi", "Helvetica", 12, 200)
	if len(got) != 1 || got[0] != "Hi" {
		t.Errorf("got %v, want single line 'Hi'", got)
	}
}

func TestWrapTextToWidth_WrapsOnWordBoundary(t *testing.T) {
	// Each word is ~30pt at 12pt Helvetica. maxWidth=70 means
	// roughly two words per line. The wrapper must break on a
	// space, never mid-word.
	got := WrapTextToWidth("aaaa bbbb cccc dddd", "Helvetica", 12, 70)
	if len(got) < 2 {
		t.Fatalf("expected wrap into multiple lines; got %v", got)
	}
	for _, line := range got {
		// No empty lines, no leading/trailing spaces (the
		// wrapper joins with single spaces — leading/trailing
		// would mean a bug).
		if line == "" {
			t.Errorf("got empty line in %v", got)
		}
		if line[0] == ' ' || line[len(line)-1] == ' ' {
			t.Errorf("line has surrounding spaces: %q", line)
		}
	}
	// Re-joining the lines should give back the original
	// (modulo single-space separators replacing whatever the
	// caller used).
	joined := ""
	for i, line := range got {
		if i > 0 {
			joined += " "
		}
		joined += line
	}
	if joined != "aaaa bbbb cccc dddd" {
		t.Errorf("re-joined = %q, want original", joined)
	}
}

func TestWrapTextToWidth_NeverBreaksMidWord(t *testing.T) {
	// A single word wider than maxWidth must be emitted alone
	// on its own line (visible overflow), NEVER hyphenated /
	// split. Splitting would corrupt copy-paste.
	got := WrapTextToWidth("supercalifragilisticexpialidocious", "Helvetica", 12, 10)
	if len(got) != 1 {
		t.Errorf("oversize single word should NOT be split; got %v", got)
	}
	if got[0] != "supercalifragilisticexpialidocious" {
		t.Errorf("word was modified: %q", got[0])
	}
}

func TestWrapTextToWidth_HonoursHardNewlines(t *testing.T) {
	got := WrapTextToWidth("line one\nline two", "Helvetica", 12, 500)
	if len(got) != 2 {
		t.Fatalf("hard newline should produce 2 lines; got %v", got)
	}
	if got[0] != "line one" || got[1] != "line two" {
		t.Errorf("got %v, want ['line one', 'line two']", got)
	}
}

// ---- ReplaceRectFirstAnchorWrapped ----

func TestReplaceRectFirstAnchorWrapped_SingleLineMatchesNonWrappedOutput(t *testing.T) {
	// When the replacement fits in one line, the wrapped
	// emitter should produce a byte-identical replacement BT
	// to the non-wrapped path. Pins the contract that
	// upgrading callers from the old function is a no-op when
	// the text fits.
	stream := []byte("BT /F1 12 Tf 100 700 Td (Old) Tj ET")
	rect := Rect{X0: 90, Y0: 690, X1: 400, Y1: 720}

	gotWrapped, ok := ReplaceRectFirstAnchorWrapped(stream, rect, "New", 0)
	if !ok {
		t.Fatal("wrapped emit returned ok=false")
	}
	gotPlain, ok := ReplaceRectFirstAnchor(stream, rect, "New")
	if !ok {
		t.Fatal("plain emit returned ok=false")
	}
	if !bytes.Equal(gotWrapped, gotPlain) {
		t.Errorf("single-line wrap output diverged from plain:\nwrapped: %q\nplain: %q",
			gotWrapped, gotPlain)
	}
}

func TestReplaceRectFirstAnchorWrapped_LongReplacementProducesMultipleBTBlocks(t *testing.T) {
	// Original cell: 12pt Helvetica at (100, 700). Cell right
	// edge at x=160 → wrap target is 60pt → roughly 7-9
	// glyphs per line. Replacement is multiple words that
	// can't fit on one line.
	stream := []byte("BT /Helvetica 12 Tf 100 700 Td (Old) Tj ET")
	rect := Rect{X0: 95, Y0: 690, X1: 160, Y1: 720}
	got, ok := ReplaceRectFirstAnchorWrapped(stream, rect, "alpha bravo charlie delta echo", 0)
	if !ok {
		t.Fatal("expected ok=true")
	}
	// Multiple BT blocks → at least 2 occurrences of "BT\n/Helvetica".
	count := bytes.Count(got, []byte("\nBT\n/Helvetica 12 Tf\n"))
	if count < 2 {
		t.Errorf("expected >= 2 stacked BT blocks; got %d in %q", count, got)
	}
	// Each subsequent line moves "down" (smaller y) by
	// 1.2 * 12 = 14.4 pt. The second line's anchor should be
	// at (100, 685.6).
	if !bytes.Contains(got, []byte("100 685.6 Td")) {
		t.Errorf("second-line anchor not present in output: %q", got)
	}
	// Original Tj scrubbed.
	if !bytes.Contains(got, []byte("() Tj")) {
		t.Errorf("original Tj should be scrubbed: %q", got)
	}
}

func TestReplaceRectFirstAnchorWrapped_NoOverlapReturnsFalse(t *testing.T) {
	stream := []byte("BT /F1 12 Tf 100 700 Td (Hello) Tj ET")
	got, ok := ReplaceRectFirstAnchorWrapped(stream, Rect{X0: 500, Y0: 500, X1: 600, Y1: 520}, "New", 0)
	if ok {
		t.Error("expected ok=false for non-overlapping rect")
	}
	if !bytes.Equal(got, stream) {
		t.Error("non-overlapping wrap should return the input unchanged")
	}
}

func TestReplaceRectFirstAnchorWrapped_EmptyReplacementJustScrubs(t *testing.T) {
	// Empty replacement → only the scrub takes effect; no
	// replacement BT emitted. The "delete cell contents"
	// path.
	stream := []byte("BT /Helvetica 12 Tf 100 700 Td (Old) Tj ET")
	got, ok := ReplaceRectFirstAnchorWrapped(stream, Rect{X0: 90, Y0: 690, X1: 200, Y1: 720}, "", 0)
	if !ok {
		t.Fatal("scrub-only should still return ok=true")
	}
	if !bytes.Contains(got, []byte("() Tj")) {
		t.Errorf("expected scrub to empty; got %q", got)
	}
	if bytes.Contains(got, []byte("\nBT\n/Helvetica")) && bytes.Count(got, []byte("\nBT\n/Helvetica")) > 1 {
		// One BT/Helvetica from the original stream is fine
		// (the scrubbed one). A SECOND would mean we emitted
		// a replacement, which is wrong for empty input.
		t.Errorf("empty replacement should NOT emit a new BT block; got %q", got)
	}
}

func TestReplaceRectFirstAnchorWrapped_RespectsCustomLineLeadingFactor(t *testing.T) {
	// lineLeadingFactor=2.0 → line gap = 24pt. Second line
	// anchor moves down by 24 from y=700 → y=676.
	stream := []byte("BT /Helvetica 12 Tf 100 700 Td (Old) Tj ET")
	rect := Rect{X0: 95, Y0: 600, X1: 160, Y1: 720}
	got, ok := ReplaceRectFirstAnchorWrapped(stream, rect, "alpha bravo charlie delta", 2.0)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !bytes.Contains(got, []byte("100 676 Td")) {
		t.Errorf("custom leading not honoured; expected second-line y=676 in %q", got)
	}
}
