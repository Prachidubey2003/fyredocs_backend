package spdom

import (
	"math"
	"testing"
)

// approxRect checks that two rects are within `eps` points on every edge.
// Used to assert layout bboxes without locking in the exact value of the
// approxWidth advance — width estimates are rough by design (§layout.go).
func approxRect(t *testing.T, got, want Rect, eps float64, label string) {
	t.Helper()
	if math.Abs(got.X0-want.X0) > eps ||
		math.Abs(got.Y0-want.Y0) > eps ||
		math.Abs(got.X1-want.X1) > eps ||
		math.Abs(got.Y1-want.Y1) > eps {
		t.Errorf("%s bbox = %+v, want ~%+v (eps=%.2f)", label, got, want, eps)
	}
}

func TestExtractTextEvents_TwoLinesViaTd(t *testing.T) {
	stream := []byte("BT /F1 12 Tf 72 720 Td (Hello) Tj 0 -16 Td (World) Tj ET")
	events, supported := extractTextEvents(stream, nil)
	if !supported {
		t.Fatal("identity-orientation stream should be supported")
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].x != 72 || events[0].y != 720 || events[0].text != "Hello" {
		t.Errorf("event[0] = %+v, want x=72 y=720 text=Hello", events[0])
	}
	// Second Td is relative to the first: tlm.e was 72, +0 → still 72;
	// tlm.f was 720, -16 → 704.
	if events[1].x != 72 || events[1].y != 704 || events[1].text != "World" {
		t.Errorf("event[1] = %+v, want x=72 y=704 text=World", events[1])
	}
	if events[0].fontName != "F1" || events[0].fontSize != 12 {
		t.Errorf("font = %q size=%v, want F1 12", events[0].fontName, events[0].fontSize)
	}
}

func TestExtractTextEvents_TmAbsolutePosition(t *testing.T) {
	// Tm sets both Tm and Tlm to the given matrix. The text-show should be
	// emitted at (200, 500).
	stream := []byte("BT /F1 12 Tf 1 0 0 1 200 500 Tm (Mid) Tj ET")
	events, supported := extractTextEvents(stream, nil)
	if !supported {
		t.Fatal("Tm with identity rotation should be supported")
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].x != 200 || events[0].y != 500 {
		t.Errorf("event = %+v, want x=200 y=500", events[0])
	}
}

func TestExtractTextEvents_RotatedTextEmittedWithOrientation(t *testing.T) {
	// v1 of the rotation pass: 90° rotated text is now emitted as
	// a textEvent with orient=90 (instead of being dropped). The
	// page-space origin is still (tm[4], tm[5]); the bbox math
	// in buildBlocksFromEvents handles the rotated rectangle.
	stream := []byte("BT /F1 12 Tf 0 1 -1 0 100 100 Tm (Rot) Tj ET")
	events, supported := extractTextEvents(stream, nil)
	if !supported {
		t.Error("rotated text should NOT mark supported=false")
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (90° rotation emitted)", len(events))
	}
	if events[0].orient != 90 {
		t.Errorf("event.orient = %d, want 90", events[0].orient)
	}
	if events[0].text != "Rot" {
		t.Errorf("event.text = %q, want \"Rot\"", events[0].text)
	}
}

func TestExtractTextEvents_MixedOrientationKeepsBothEvents(t *testing.T) {
	// v1: a page with one horizontal line + one 90°-rotated
	// watermark emits BOTH events — the horizontal one with
	// orient=0, the watermark with orient=90.
	stream := []byte(
		"BT /F1 12 Tf 1 0 0 1 72 720 Tm (Body) Tj ET " +
			"BT /F1 24 Tf 0 1 -1 0 300 400 Tm (DRAFT) Tj ET",
	)
	events, supported := extractTextEvents(stream, nil)
	if !supported {
		t.Fatal("page with rotated watermark should still be supported")
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2 (horizontal + rotated)", len(events))
	}
	// Index 0 should be "Body" (horizontal); index 1 "DRAFT" (90°).
	if events[0].text != "Body" || events[0].orient != 0 {
		t.Errorf("event[0] = %+v, want Body @ orient=0", events[0])
	}
	if events[1].text != "DRAFT" || events[1].orient != 90 {
		t.Errorf("event[1] = %+v, want DRAFT @ orient=90", events[1])
	}
}

func TestBuildBlocksFromEvents_RotatedEventBecomesOrientedBlock(t *testing.T) {
	// One 90°-rotated event must land in L4 as a Block with
	// Orientation=90 and a correctly-rotated BBox. The horizontal
	// path is untouched.
	page := Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	events := []textEvent{
		// Horizontal "Hello" at (72, 720) — anchors the horizontal
		// path and proves rotated blocks don't merge with it.
		{x: 72, y: 720, fontName: "Helvetica", fontSize: 12, text: "Hello", orient: 0},
		// 90° "Side" at (200, 300) with size 12 — bbox should
		// extend UP (+y) along the rotated baseline and LEFT (-x)
		// along the rotated height.
		{x: 200, y: 300, fontName: "Helvetica", fontSize: 12, text: "Side", orient: 90},
	}
	blocks := buildBlocksFromEvents("page-1", page, events)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (horizontal + rotated), got %d", len(blocks))
	}
	// Horizontal block always emitted first (block-1).
	if blocks[0].Orientation != 0 {
		t.Errorf("block[0].Orientation = %d, want 0", blocks[0].Orientation)
	}
	// Rotated block second (block-2). Bbox: x ∈ [200-12, 200]
	// = [188, 200]; y ∈ [300, 300+w]. Width via AFM core-font for
	// "Side" at 12pt is ≈ 24pt; just sanity-check the AABB shape
	// rather than the exact width.
	rb := blocks[1]
	if rb.Orientation != 90 {
		t.Errorf("rotated block Orientation = %d, want 90", rb.Orientation)
	}
	if rb.BBox.X1 != 200 {
		t.Errorf("rotated bbox X1 = %v, want 200 (origin x)", rb.BBox.X1)
	}
	if rb.BBox.X0 >= 200 {
		t.Errorf("rotated bbox X0 = %v, want < 200 (height extends LEFT for 90°)", rb.BBox.X0)
	}
	if rb.BBox.Y0 != 300 {
		t.Errorf("rotated bbox Y0 = %v, want 300 (origin y)", rb.BBox.Y0)
	}
	if rb.BBox.Y1 <= 300 {
		t.Errorf("rotated bbox Y1 = %v, want > 300 (width extends UP for 90°)", rb.BBox.Y1)
	}
}

func TestBuildBlocksFromEvents_AllFourRotationsGeometry(t *testing.T) {
	// Exercise the end-to-end clustering pipeline (page-space →
	// per-orientation logical frame → cluster → project back) for
	// each orthogonal rotation. fontSize = 12, "abc" at the
	// opaque "F1" fallback width (0.5em/glyph) = 18pt; height =
	// 12pt. Origin (100, 100) for every case so the geometry
	// is comparable across orientations.
	//
	// These exact bbox values are also the test of record for the
	// rotatedToLogical / logicalToPageRect transforms: any drift
	// shows up here.
	page := Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	mk := func(orient int) textEvent {
		return textEvent{x: 100, y: 100, fontName: "F1", fontSize: 12, text: "abc", orient: orient}
	}
	cases := []struct {
		name           string
		orient         int
		wantX0, wantY0 float64
		wantX1, wantY1 float64
	}{
		{"0°", 0, 100, 100, 118, 112},
		{"90°", 90, 88, 100, 100, 118},
		{"180°", 180, 82, 88, 100, 100},
		{"270°", 270, 100, 82, 112, 100},
	}
	for _, c := range cases {
		blocks := buildBlocksFromEvents("page-rot", page, []textEvent{mk(c.orient)})
		if len(blocks) != 1 {
			t.Errorf("%s: len(blocks) = %d, want 1", c.name, len(blocks))
			continue
		}
		got := blocks[0].BBox
		if got.X0 != c.wantX0 || got.Y0 != c.wantY0 ||
			got.X1 != c.wantX1 || got.Y1 != c.wantY1 {
			t.Errorf("%s: bbox = %+v, want {X0:%v Y0:%v X1:%v Y1:%v}",
				c.name, got, c.wantX0, c.wantY0, c.wantX1, c.wantY1)
		}
		if blocks[0].Orientation != c.orient {
			t.Errorf("%s: block.Orientation = %d, want %d",
				c.name, blocks[0].Orientation, c.orient)
		}
	}
}

func TestBuildBlocksFromEvents_RotatedMultiLineClusters(t *testing.T) {
	// Two 90°-rotated events that share a rotated paragraph
	// (different page_x — i.e., different rotated lines — but
	// close enough vertically in the logical frame to count
	// as one Block) must materialise as ONE Block containing
	// TWO Lines. This is the symmetry with the horizontal
	// clusterer that the v0 path lost (one-event-per-block).
	//
	// 90° geometry: baseline runs +page_y; "next line" means
	// SMALLER page_x. So Line 1 (read first) has the smallest
	// page_x; Line 2 has page_x = Line1.x + ~fontSize.
	page := Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	events := []textEvent{
		// First rotated line: at page_x=200, "FirstLine".
		{x: 200, y: 300, fontName: "F1", fontSize: 12, text: "FirstLine", orient: 90},
		// Second rotated line: page_x=212 (one font-size over),
		// same y-anchor.
		{x: 212, y: 300, fontName: "F1", fontSize: 12, text: "SecondLine", orient: 90},
	}
	blocks := buildBlocksFromEvents("page-rot-multi", page, events)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (rotated paragraph clusters together), got %d", len(blocks))
	}
	b := blocks[0]
	if b.Orientation != 90 {
		t.Errorf("block.Orientation = %d, want 90", b.Orientation)
	}
	if len(b.Lines) != 2 {
		t.Fatalf("expected 2 lines (one per rotated baseline), got %d", len(b.Lines))
	}
	// Reading order in the rotated frame: smaller page_x first
	// (= higher in the logical frame = top of the rotated
	// paragraph). The transform makes logical_y = -page_x, so
	// page_x=200 → ly=-200 (higher), page_x=212 → ly=-212 (lower).
	// Lines are emitted top→bottom in logical = larger ly first.
	got1 := b.Lines[0].Runs[0].Text
	got2 := b.Lines[1].Runs[0].Text
	if got1 != "FirstLine" || got2 != "SecondLine" {
		t.Errorf("line order = [%q, %q], want [FirstLine, SecondLine]", got1, got2)
	}
	// Block bbox should span both rotated lines:
	//   page_x: [200 - 12, 212]  = [188, 212]   (each line height 12 extends LEFT)
	//   page_y: [300, 300 + max(w1, w2)]
	// w2 (SecondLine, 10 glyphs × 6pt fallback) = 60; w1 = 54.
	// Block Y1 should be 300 + 60 = 360.
	if b.BBox.X0 != 188 {
		t.Errorf("block bbox X0 = %v, want 188 (200 - 12 height)", b.BBox.X0)
	}
	if b.BBox.X1 != 212 {
		t.Errorf("block bbox X1 = %v, want 212 (second-line anchor)", b.BBox.X1)
	}
	if b.BBox.Y0 != 300 {
		t.Errorf("block bbox Y0 = %v, want 300 (origin y)", b.BBox.Y0)
	}
	if b.BBox.Y1 != 360 {
		t.Errorf("block bbox Y1 = %v, want 360 (300 + widest rotated line width)", b.BBox.Y1)
	}
}

func TestBuildBlocksFromEvents_RotatedTwoGlyphsSameBaselineMergeIntoOneLine(t *testing.T) {
	// Two 90° events on the SAME rotated baseline (same page_x,
	// different page_y) with the same font/size must merge into
	// a single Line containing a single Run (the horizontal
	// clusterer's same-baseline-merge logic kicking in on the
	// rotated path).
	page := Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	events := []textEvent{
		// page_x=200, page_y=300 ("Hi") → logical: lx=300, ly=-200
		{x: 200, y: 300, fontName: "F1", fontSize: 12, text: "Hi", orient: 90},
		// page_x=200, page_y=312 ("There") → logical: lx=312, ly=-200
		// Same ly (same rotated baseline) and lx=312 > prev's
		// lx+approxWidth("Hi")≈312 → continues the same run.
		{x: 200, y: 312, fontName: "F1", fontSize: 12, text: "There", orient: 90},
	}
	blocks := buildBlocksFromEvents("page-rot-merge", page, events)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.Orientation != 90 {
		t.Errorf("block.Orientation = %d, want 90", b.Orientation)
	}
	if len(b.Lines) != 1 {
		t.Fatalf("expected 1 line (same rotated baseline), got %d", len(b.Lines))
	}
	if len(b.Lines[0].Runs) != 1 {
		t.Fatalf("expected 1 run (same font+baseline merges), got %d runs", len(b.Lines[0].Runs))
	}
	if b.Lines[0].Runs[0].Text != "HiThere" {
		t.Errorf("merged run text = %q, want %q", b.Lines[0].Runs[0].Text, "HiThere")
	}
}

func TestExtractTextEvents_OneEightyAndTwoSeventyRotation(t *testing.T) {
	// All four orthogonal rotations must classify correctly.
	// 0° already covered by the identity tests; this one
	// drives 180° and 270°.
	cases := []struct {
		name   string
		tm     string
		orient int
	}{
		{"180", "-1 0 0 -1", 180},
		{"270", "0 -1 1 0", 270},
	}
	for _, tc := range cases {
		stream := []byte("BT /F1 12 Tf " + tc.tm + " 100 100 Tm (X) Tj ET")
		events, supported := extractTextEvents(stream, nil)
		if !supported {
			t.Errorf("%s: supported=false", tc.name)
			continue
		}
		if len(events) != 1 || events[0].orient != tc.orient {
			t.Errorf("%s: len=%d orient=%v, want 1 / %d",
				tc.name, len(events),
				func() int {
					if len(events) > 0 {
						return events[0].orient
					}
					return -1
				}(), tc.orient)
		}
	}
}

func TestExtractTextEvents_UniformScaleFoldedIntoFontSize(t *testing.T) {
	// Tm with uniform scale 2× — the rendered point size is
	// fontSize * scale = 12 * 2 = 24. The emitted event should carry
	// the effective size, not the bare Tf value, so downstream width
	// math is correct.
	stream := []byte("BT /F1 12 Tf 2 0 0 2 100 700 Tm (Big) Tj ET")
	events, supported := extractTextEvents(stream, nil)
	if !supported {
		t.Fatal("uniform scale should be supported")
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].fontSize != 24 {
		t.Errorf("effective fontSize = %v, want 24 (12pt × 2× scale)", events[0].fontSize)
	}
}

func TestExtractTextEvents_NonUniformScaleEmittedAsSkewed(t *testing.T) {
	// a != d — non-uniform scale (stretched 2× horizontally, 3×
	// vertically). v2 of the rotation pass keeps the event with
	// the raw 2x2 stored on the textEvent so the bbox-projection
	// code can derive a correct page-space AABB. orient is the
	// OrientationSkewed sentinel (-1).
	stream := []byte("BT /F1 12 Tf 2 0 0 3 100 700 Tm (Stretched) Tj ET")
	events, supported := extractTextEvents(stream, nil)
	if !supported {
		t.Error("non-uniform scale should NOT fail the page")
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (skewed event emitted)", len(events))
	}
	ev := events[0]
	if ev.orient != OrientationSkewed {
		t.Errorf("orient = %d, want %d (OrientationSkewed)", ev.orient, OrientationSkewed)
	}
	if ev.tmA != 2 || ev.tmB != 0 || ev.tmC != 0 || ev.tmD != 3 {
		t.Errorf("tm 2x2 = [%v %v %v %v], want [2 0 0 3]", ev.tmA, ev.tmB, ev.tmC, ev.tmD)
	}
	if ev.fontSize != 12 {
		t.Errorf("fontSize = %v, want 12 (bare Tf size — no uniform scale to fold)", ev.fontSize)
	}
}

func TestExtractTextEvents_VerticalMirrorEmittedAsSkewed(t *testing.T) {
	// True mirror (one axis negated, not both) is now kept as
	// a skewed event. The bbox math (skewedEventBBox) projects
	// the four text-space corners through the raw 2x2 so the
	// AABB is correct even for reflected glyphs.
	//
	// Setup: d < 0, a > 0 — y-axis mirror (text reads
	// right-to-left in mirror space). Still rare in real PDFs
	// but no longer silently dropped.
	stream := []byte("BT /F1 12 Tf 1 0 0 -1 100 100 Tm (Mirror) Tj ET")
	events, supported := extractTextEvents(stream, nil)
	if !supported {
		t.Error("mirrored text should NOT fail the page")
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1 (mirror emitted as skewed)", len(events))
	}
	ev := events[0]
	if ev.orient != OrientationSkewed {
		t.Errorf("orient = %d, want %d (OrientationSkewed)", ev.orient, OrientationSkewed)
	}
	if ev.tmA != 1 || ev.tmB != 0 || ev.tmC != 0 || ev.tmD != -1 {
		t.Errorf("tm 2x2 = [%v %v %v %v], want [1 0 0 -1]", ev.tmA, ev.tmB, ev.tmC, ev.tmD)
	}
}

func TestExtractTextEvents_ItalicShearEmittedAsSkewed(t *testing.T) {
	// Synthetic italic via a shear in `c`: [1 0 0.2 1] tilts the
	// glyph height to the right by 20% per unit of height.
	// Common in PDFs that fake italics from a Roman font.
	stream := []byte("BT /F1 10 Tf 1 0 0.2 1 50 600 Tm (Italic) Tj ET")
	events, supported := extractTextEvents(stream, nil)
	if !supported {
		t.Error("italic shear should keep the page supported")
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.orient != OrientationSkewed {
		t.Errorf("orient = %d, want %d", ev.orient, OrientationSkewed)
	}
	if ev.tmC != 0.2 {
		t.Errorf("tmC = %v, want 0.2 (shear component)", ev.tmC)
	}
}

func TestBuildBlocksFromEvents_SkewedEventBecomesSkewedBlock(t *testing.T) {
	// One non-uniform-scaled event must land in L4 as a Block
	// with Orientation=OrientationSkewed and a correct
	// page-space AABB derived from the 4-corner projection.
	page := Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	// "Stretched" at 12pt, F1 fallback ⇒ approxWidth = 9 × 0.5 × 12 = 54.
	// Tm = [2 0 0 3] (2× wide, 3× tall) anchored at (100, 700).
	// Page-space rect: x ∈ [100, 100 + 54·2] = [100, 208];
	//                  y ∈ [700, 700 + 12·3] = [700, 736].
	events := []textEvent{
		{
			x: 100, y: 700,
			fontName: "F1", fontSize: 12, text: "Stretched",
			orient: OrientationSkewed,
			tmA:    2, tmB: 0, tmC: 0, tmD: 3,
		},
	}
	blocks := buildBlocksFromEvents("page-skew", page, events)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 skewed block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.Orientation != OrientationSkewed {
		t.Errorf("block.Orientation = %d, want %d", b.Orientation, OrientationSkewed)
	}
	want := Rect{X0: 100, Y0: 700, X1: 208, Y1: 736}
	if b.BBox != want {
		t.Errorf("bbox = %+v, want %+v", b.BBox, want)
	}
}

func TestSkewedEventBBox_HandlesMirrorAndShear(t *testing.T) {
	// Unit-test the 4-corner projection across three
	// non-orthogonal Tm matrices. fontSize = 12, "Tx" at the
	// F1 fallback width ⇒ 2 × 0.5 × 12 = 12. Anchor (100, 100)
	// for all cases so the geometry is comparable.
	mk := func(a, b, c, d float64) textEvent {
		return textEvent{
			x: 100, y: 100, fontName: "F1", fontSize: 12, text: "Tx",
			orient: OrientationSkewed, tmA: a, tmB: b, tmC: c, tmD: d,
		}
	}
	cases := []struct {
		name string
		ev   textEvent
		want Rect
	}{
		// Mirror (y-flip): corners go (100,100), (112,100),
		// (100,88), (112,88). AABB: x∈[100,112], y∈[88,100].
		{"y-mirror", mk(1, 0, 0, -1), Rect{X0: 100, Y0: 88, X1: 112, Y1: 100}},
		// Non-uniform 2×/3×: corners (100,100), (124,100),
		// (100,136), (124,136). AABB: x∈[100,124], y∈[100,136].
		{"non-uniform scale", mk(2, 0, 0, 3), Rect{X0: 100, Y0: 100, X1: 124, Y1: 136}},
		// Shear c=0.2 (italic): corners (100,100), (112,100),
		// (100+12·0.2,112)=(102.4,112), (114.4,112). AABB:
		// x∈[100,114.4], y∈[100,112].
		{"italic shear", mk(1, 0, 0.2, 1), Rect{X0: 100, Y0: 100, X1: 114.4, Y1: 112}},
	}
	for _, c := range cases {
		got := skewedEventBBox(c.ev)
		if got != c.want {
			t.Errorf("%s: bbox = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestExtractTextEvents_NextLineOperators(t *testing.T) {
	// `'` is "move to next line + show". After Td(72 720) with leading 14,
	// the apostrophe drops the cursor by 14 units. T* does the same without
	// the show step.
	stream := []byte("BT /F1 12 Tf 14 TL 72 720 Td (A) Tj (B) ' T* (C) Tj ET")
	events, supported := extractTextEvents(stream, nil)
	if !supported {
		t.Fatal("identity stream should be supported")
	}
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}
	// A at (72, 720)
	if events[0].y != 720 {
		t.Errorf("event[0].y = %v, want 720", events[0].y)
	}
	// B after ' → next line: y = 720 - 14 = 706
	if events[1].y != 706 {
		t.Errorf("event[1].y = %v, want 706", events[1].y)
	}
	// C after T* (no show) then Tj → y = 706 - 14 = 692
	if events[2].y != 692 {
		t.Errorf("event[2].y = %v, want 692", events[2].y)
	}
}

func TestExtractTextEvents_TJArray(t *testing.T) {
	stream := []byte("BT /F1 12 Tf 100 600 Td [ (Hel) -100 (lo) ] TJ ET")
	events, supported := extractTextEvents(stream, nil)
	if !supported {
		t.Fatal("supported should be true")
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].text != "Hel" || events[1].text != "lo" {
		t.Errorf("texts = %q, %q; want Hel, lo", events[0].text, events[1].text)
	}
	// The -100 between them shifts the X by +100/1000 * 12 = 1.2 in user
	// space (negative numbers move right per ISO 32000-1 §9.4.3). The
	// approximate width of "Hel" is 3 * 0.5em = 18, so events[1].x should
	// be approximately 100 + 18 + 1.2 = 119.2. Allow a generous tolerance
	// because approxWidth is intentionally rough.
	if events[1].x <= events[0].x {
		t.Errorf("event[1].x %v should be > event[0].x %v", events[1].x, events[0].x)
	}
}

func TestBuildBlocksFromEvents_GroupsTwoLinesIntoOneBlock(t *testing.T) {
	page := Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	events := []textEvent{
		{x: 72, y: 720, fontName: "F1", fontSize: 12, text: "Hello"},
		{x: 72, y: 706, fontName: "F1", fontSize: 12, text: "World"},
	}
	blocks := buildBlocksFromEvents("page-1", page, events)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	b := blocks[0]
	if b.Type != BlockText {
		t.Errorf("Type = %q, want %q", b.Type, BlockText)
	}
	if len(b.Lines) != 2 {
		t.Fatalf("len(Lines) = %d, want 2", len(b.Lines))
	}
	// Lines are sorted top→bottom (Y descending).
	if b.Lines[0].Runs[0].Text != "Hello" {
		t.Errorf("Lines[0].Runs[0].Text = %q, want Hello", b.Lines[0].Runs[0].Text)
	}
	if b.Lines[1].Runs[0].Text != "World" {
		t.Errorf("Lines[1].Runs[0].Text = %q, want World", b.Lines[1].Runs[0].Text)
	}
	// Run bbox Y0=baseline, Y1=baseline+fontSize. X0 = event.x; width is
	// approximate so allow eps=5 on X1.
	run0 := b.Lines[0].Runs[0]
	approxRect(t, run0.BBox, Rect{X0: 72, Y0: 720, X1: 72 + 30, Y1: 732}, 5, "Run[Hello]")
	// Line bbox should equal its single run's bbox.
	approxRect(t, b.Lines[0].BBox, run0.BBox, 0.001, "Line[Hello]")
	// Block bbox should span both lines.
	approxRect(t, b.BBox, Rect{X0: 72, Y0: 706, X1: 72 + 30, Y1: 732}, 5, "Block")
}

func TestBuildBlocksFromEvents_SplitsOnLargeVerticalGap(t *testing.T) {
	page := Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	// Two lines 50 points apart with 12-pt font: gap >> 1.5*12 = 18, so
	// they should become separate Blocks.
	events := []textEvent{
		{x: 72, y: 720, fontName: "F1", fontSize: 12, text: "Header"},
		{x: 72, y: 650, fontName: "F1", fontSize: 12, text: "Body"},
	}
	blocks := buildBlocksFromEvents("page-1", page, events)
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2 (paragraph break)", len(blocks))
	}
}

func TestBuildBlocksFromEvents_SameLineDifferentFontsBecomeSeparateRuns(t *testing.T) {
	page := Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	events := []textEvent{
		{x: 72, y: 720, fontName: "F1", fontSize: 12, text: "Normal "},
		{x: 120, y: 720, fontName: "F2", fontSize: 12, text: "Bold"},
	}
	blocks := buildBlocksFromEvents("page-1", page, events)
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if len(blocks[0].Lines) != 1 {
		t.Fatalf("len(Lines) = %d, want 1", len(blocks[0].Lines))
	}
	if len(blocks[0].Lines[0].Runs) != 2 {
		t.Fatalf("len(Runs) = %d, want 2 (font change splits)", len(blocks[0].Lines[0].Runs))
	}
	if blocks[0].Lines[0].Runs[0].Font != "F1" || blocks[0].Lines[0].Runs[1].Font != "F2" {
		t.Errorf("run fonts = %q, %q; want F1, F2",
			blocks[0].Lines[0].Runs[0].Font,
			blocks[0].Lines[0].Runs[1].Font)
	}
}

func TestBuildBlocksFromEvents_StableNodeIDs(t *testing.T) {
	page := Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	events := []textEvent{
		{x: 72, y: 720, fontName: "F1", fontSize: 12, text: "Stable"},
	}
	a := buildBlocksFromEvents("page-stable", page, events)
	b := buildBlocksFromEvents("page-stable", page, events)
	if a[0].ID != b[0].ID {
		t.Errorf("block IDs diverged: %q vs %q", a[0].ID, b[0].ID)
	}
	if a[0].Lines[0].Runs[0].ID != b[0].Lines[0].Runs[0].ID {
		t.Errorf("run IDs diverged across calls")
	}
}

func TestBuildBlocksFromEvents_EmptyEvents(t *testing.T) {
	page := Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}
	blocks := buildBlocksFromEvents("page-1", page, nil)
	if len(blocks) != 0 {
		t.Errorf("len(blocks) = %d, want 0 for empty events", len(blocks))
	}
}

func TestApproxWidth_ScalesWithSize(t *testing.T) {
	w1 := approxWidth("hello", "", 10, 0, 0)
	w2 := approxWidth("hello", "", 20, 0, 0)
	if w2 <= w1 {
		t.Errorf("expected w2 (size 20) > w1 (size 10); got %v vs %v", w2, w1)
	}
	if approxWidth("", "", 12, 0, 0) != 0 {
		t.Errorf("empty string should have zero width")
	}
	if approxWidth("hi", "", 0, 0, 0) != 0 {
		t.Errorf("zero size should have zero width")
	}
}

func TestExpand_HandlesInfiniteSeed(t *testing.T) {
	seed := Rect{X0: math.Inf(1), Y0: math.Inf(1), X1: math.Inf(-1), Y1: math.Inf(-1)}
	r := Rect{X0: 10, Y0: 20, X1: 30, Y1: 40}
	out := expand(seed, r)
	if out != r {
		t.Errorf("expand(inf-seed, r) = %+v, want %+v", out, r)
	}
	r2 := Rect{X0: 5, Y0: 25, X1: 25, Y1: 50}
	out = expand(out, r2)
	want := Rect{X0: 5, Y0: 20, X1: 30, Y1: 50}
	if out != want {
		t.Errorf("expand union = %+v, want %+v", out, want)
	}
}
