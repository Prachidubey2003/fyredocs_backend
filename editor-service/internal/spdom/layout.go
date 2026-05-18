package spdom

import (
	"math"
	"sort"
	"strconv"
)

// textEvent is one chunk of text emitted at a known baseline position with
// known font + size. The position-aware extractor produces these by walking
// the PDF content-stream and tracking the text state machine (Tm/Td/TD/T*).
//
// Coordinates are PDF user-space points: origin bottom-left, Y increases
// upward. The semantics of fontSize and the optional tm[ABCD] fields
// depend on orient:
//
//   - orient ∈ {0, 90, 180, 270}: orthogonal rotation with uniform
//     scale. fontSize is the rendered point size (bare Tf × scale,
//     folded together so downstream code can size run heights
//     directly). tmA/B/C/D are unused.
//
//   - orient == -1 (OrientationSkewed): non-orthogonal Tm — skew,
//     non-uniform scale, or mirror. fontSize is the BARE Tf size
//     (text-space height in points, not the rendered size, because
//     there is no single uniform scale to fold in). tmA/B/C/D hold
//     the raw 2x2 portion of the text matrix [a b c d] so that the
//     bbox-projection code can map the four text-space corners to
//     page space.
type textEvent struct {
	x, y     float64
	fontName string
	fontSize float64
	text     string
	// orient is the on-page rotation: 0 (horizontal), 90 (text
	// reads bottom-to-top), 180 (upside-down), 270 (top-to-
	// bottom), or -1 (OrientationSkewed) for non-orthogonal
	// Tm matrices. The downstream clusterer (buildBlocksFromEvents)
	// uses orient to partition events before grouping — rotated
	// runs must not merge with horizontal runs at the same page-y
	// coordinate, and skewed events go through their own
	// one-block-per-event path (no shared logical frame).
	orient int
	// tmA, tmB, tmC, tmD are the raw 2x2 portion of the text
	// matrix [a b c d], only populated when orient == -1. For
	// orthogonal events, the 2x2 is implicit in (orient, scale)
	// and these fields are zero.
	tmA, tmB, tmC, tmD float64
}

// extractTextEvents walks a PDF content stream and emits a textEvent for
// each Tj/'/"/TJ that runs in identity-orientation text (no rotation,
// no skew, no flipped axes).
//
// Returns supported=false the first time the stream uses a text matrix
// that is not a pure translation+scale — that signals the parser to fall
// back to the plain-text path (LayoutPass=1). This is a conservative
// choice: trying to interpret rotated-text positions in the absence of a
// full matrix implementation would silently produce wrong bboxes.
//
// `fontMap` resolves a content-stream resource name (the operand of the
// Tf operator, e.g. `F1`) to the canonical font name (e.g. `Helvetica`)
// declared in the page's `/Resources/Font/<name>/BaseFont`. When a
// resource name is present in the map, the resolved canonical name is
// stored on every emitted textEvent — this is what lets approxWidth /
// CharAdvance reach pdfcpu's AFM tables for the 14 core fonts. Pass nil
// (or an empty map) to fall back to the bare resource name.
//
// PDF state per ISO 32000-1 §9.4:
//   - Inside BT … ET, the text matrix Tm and line matrix Tlm both
//     initialise to the identity at BT.
//   - Td tx ty:      Tlm = [[1 0 0][0 1 0][tx ty 1]] * Tlm   ; Tm = Tlm
//   - TD tx ty:      same as Td, plus TL = -ty
//   - T*             same as Td(0, -TL)
//   - Tm a b c d e f: Tm = Tlm = [[a b 0][c d 0][e f 1]]
//   - Tf font size:  set font + size
//   - ' string :     T* then Tj
//   - " aw ac str:   set Tw + Tc, then '
//
// This implementation tracks only translations + uniform scale (a=d=size,
// b=c=0). Anything else triggers the supported=false bail-out.
func extractTextEvents(stream []byte, fontMap map[string]string) (events []textEvent, supported bool) {
	supported = true

	// Text-state.
	var inText bool
	// Text matrix (Tm) and line matrix (Tlm), each [a b c d e f].
	var tm, tlm [6]float64
	identity := [6]float64{1, 0, 0, 1, 0, 0}
	tm, tlm = identity, identity
	var leading float64
	var fontName string
	var fontSize float64
	var charSpace, wordSpace float64

	emit := func(text string) {
		// Classify the 2x2 portion of the text matrix into one of
		// the four orthogonal rotations (0/90/180/270), optionally
		// uniformly scaled. Anything else (skew, non-uniform
		// scale, mirror) falls through to the skewed path —
		// emitted with the raw 2x2 stored so buildBlocksFromEvents
		// can compute a correct page-space AABB by projecting the
		// four text-space corners.
		//
		// Classification table (s = uniform scale factor):
		//   0°:   [s 0 0 s …] — a=d=s>0, b=c=0
		//   90°:  [0 s -s 0 …] — a=d=0, b=s>0, c=-s
		//   180°: [-s 0 0 -s …] — a=d=-s<0, b=c=0
		//   270°: [0 -s s 0 …] — a=d=0, b=-s<0, c=s
		a, b, c, d := tm[0], tm[1], tm[2], tm[3]
		var orient int
		var scale float64
		switch {
		case nearlyZero(b) && nearlyZero(c) && a > 0 && d > 0 && nearlyEqual(a, d):
			orient = 0
			scale = a
		case nearlyZero(a) && nearlyZero(d) && b > 0 && c < 0 && nearlyEqual(b, -c):
			orient = 90
			scale = b
		case nearlyZero(b) && nearlyZero(c) && a < 0 && d < 0 && nearlyEqual(a, d):
			orient = 180
			scale = -a
		case nearlyZero(a) && nearlyZero(d) && b < 0 && c > 0 && nearlyEqual(b, -c):
			orient = 270
			scale = c
		default:
			// Skew / non-uniform / mirror. Store the bare Tf size
			// and the raw 2x2 — bbox-projection happens later in
			// skewedEventBBox. Advance in text space is approxWidth
			// at bare fontSize; mapped to page space via (a, b).
			textSpaceAdvance := approxWidth(text, fontName, fontSize, charSpace, wordSpace)
			events = append(events, textEvent{
				x: tm[4], y: tm[5],
				fontName: fontName,
				fontSize: fontSize, // bare Tf size; no uniform scale to fold
				text:     text,
				orient:   OrientationSkewed,
				tmA:      a, tmB: b, tmC: c, tmD: d,
			})
			tm[4] += textSpaceAdvance * a
			tm[5] += textSpaceAdvance * b
			return
		}
		effFontSize := fontSize * scale
		events = append(events, textEvent{
			x: tm[4], y: tm[5],
			fontName: fontName,
			fontSize: effFontSize,
			text:     text,
			orient:   orient,
		})
		// Advance along the rotated baseline. In text space the
		// glyph cursor moves (w_text, 0); under Tm that maps to
		// (w_text·a, w_text·b) in page space — i.e., the
		// page-space text-direction vector. Same formula scrub.go
		// uses (sdks/embed loop verified it round-trips for the
		// orthogonal cases). Spacing operators (Tc/Tw) accumulate
		// into approxWidth so they ride along automatically.
		wText := approxWidth(text, fontName, effFontSize, charSpace, wordSpace)
		if scale > 0 {
			// approxWidth returned at effFontSize (page-space size).
			// To get the page-space delta, divide by scale to recover
			// text-space width, then multiply by (a, b). Equivalently,
			// the unit text-direction in page space is (a/scale, b/scale).
			tm[4] += wText * a / scale
			tm[5] += wText * b / scale
		}
	}

	walkOps(stream, func(op string, operands []token) {
		switch op {
		case "BT":
			inText = true
			tm, tlm = identity, identity
		case "ET":
			inText = false
		case "Tf":
			if len(operands) < 2 {
				return
			}
			// Tf operands: font name (Name) + size (Number).
			// Resolve resource-name → canonical BaseFont via fontMap
			// (built from /Resources/Font/<n>/BaseFont). Falling
			// back to the raw resource name when unmapped keeps the
			// AFM-widths path inert for opaque/subset fonts.
			if operands[0].kind == tokName {
				resourceName := string(operands[0].bytes)
				if mapped, ok := fontMap[resourceName]; ok {
					fontName = mapped
				} else {
					fontName = resourceName
				}
			}
			if operands[1].kind == tokNumber {
				fontSize, _ = strconv.ParseFloat(string(operands[1].bytes), 64)
			}
		case "TL":
			if len(operands) >= 1 && operands[0].kind == tokNumber {
				leading, _ = strconv.ParseFloat(string(operands[0].bytes), 64)
			}
		case "Tc":
			if len(operands) >= 1 && operands[0].kind == tokNumber {
				charSpace, _ = strconv.ParseFloat(string(operands[0].bytes), 64)
			}
		case "Tw":
			if len(operands) >= 1 && operands[0].kind == tokNumber {
				wordSpace, _ = strconv.ParseFloat(string(operands[0].bytes), 64)
			}
		case "Td", "TD":
			if !inText || len(operands) < 2 {
				return
			}
			tx, ok1 := numberArg(operands[len(operands)-2])
			ty, ok2 := numberArg(operands[len(operands)-1])
			if !ok1 || !ok2 {
				return
			}
			// Tlm = translate(tx, ty) * Tlm  →  for pure translations
			// tlm.e += tx; tlm.f += ty; tm = tlm.
			tlm[4] += tx*tlm[0] + ty*tlm[2]
			tlm[5] += tx*tlm[1] + ty*tlm[3]
			tm = tlm
			if op == "TD" {
				leading = -ty
			}
		case "T*":
			if !inText {
				return
			}
			tlm[4] += -leading * tlm[2]
			tlm[5] += -leading * tlm[3]
			tm = tlm
		case "Tm":
			if len(operands) < 6 {
				return
			}
			for i := 0; i < 6; i++ {
				v, ok := numberArg(operands[i])
				if !ok {
					return
				}
				tm[i] = v
				tlm[i] = v
			}
		case "Tj":
			if len(operands) == 0 {
				return
			}
			last := operands[len(operands)-1]
			if last.kind == tokString {
				emit(string(decodeLiteral(last.bytes)))
			}
		case "'":
			// next-line + show
			if inText {
				tlm[4] += -leading * tlm[2]
				tlm[5] += -leading * tlm[3]
				tm = tlm
			}
			if len(operands) > 0 && operands[len(operands)-1].kind == tokString {
				emit(string(decodeLiteral(operands[len(operands)-1].bytes)))
			}
		case `"`:
			// aw ac string " — set Tw, Tc, then '
			if len(operands) >= 3 {
				if v, ok := numberArg(operands[len(operands)-3]); ok {
					wordSpace = v
				}
				if v, ok := numberArg(operands[len(operands)-2]); ok {
					charSpace = v
				}
			}
			if inText {
				tlm[4] += -leading * tlm[2]
				tlm[5] += -leading * tlm[3]
				tm = tlm
			}
			if len(operands) > 0 && operands[len(operands)-1].kind == tokString {
				emit(string(decodeLiteral(operands[len(operands)-1].bytes)))
			}
		case "TJ":
			if len(operands) == 0 {
				return
			}
			last := operands[len(operands)-1]
			if last.kind != tokArray {
				return
			}
			for _, item := range last.array {
				switch item.kind {
				case tokString:
					emit(string(decodeLiteral(item.bytes)))
				case tokNumber:
					// Numbers in a TJ array shift the next glyph
					// horizontally by -n/1000 * fontSize (per
					// ISO 32000-1 §9.4.3). Apply to tm.e so the
					// following emit() gets the correct X.
					if v, ok := numberArg(item); ok {
						tm[4] -= v * fontSize / 1000.0
					}
				}
			}
		}
	})
	return events, supported
}

func numberArg(t token) (float64, bool) {
	if t.kind != tokNumber {
		return 0, false
	}
	v, err := strconv.ParseFloat(string(t.bytes), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func nearlyEqual(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
func nearlyZero(a float64) bool     { return math.Abs(a) < 1e-6 }

// approxWidth returns the horizontal advance for `text` rendered
// at `size` points in `fontName`, with the given Tw/Tc spacings
// applied. Used to advance the text matrix after a Tj when the
// parser is tracking glyph positions for L3 layout.
//
// `fontName` should be the canonical PostScript name (Helvetica,
// Times-Roman, Courier-Bold, …) — what comes out of the page's
// /Font/Fxx/BaseFont entry. When it is one of the 14 PDF core
// fonts, per-glyph widths come from pdfcpu's bundled AFM tables
// (i has its narrow advance, M has its wide one). When the
// fontName is unrecognised (subset fonts, opaque resource
// aliases that haven't been resolved yet), we fall back to the
// historical 0.5em-per-glyph average — gives roughly the right
// advance for typical Latin body text but loses i-vs-M fidelity.
func approxWidth(text, fontName string, size, charSpace, wordSpace float64) float64 {
	if size <= 0 || text == "" {
		return 0
	}
	total := 0.0
	for _, r := range text {
		total += CharAdvance(fontName, r, size) + charSpace
		if r == ' ' {
			total += wordSpace
		}
	}
	return total
}

// buildBlocksFromEvents groups extracted textEvents into the L4 hierarchy:
//
//	Run  — events on the same baseline with the same font + size
//	Line — runs whose baseline-Y values overlap within tolerance
//	Block — lines whose vertical gap is within `paragraphGapPt`
//
// All bboxes use the events' real positions. Run heights = fontSize;
// widths come from the running advance accumulated in extractTextEvents.
// pageMedia is used only as the outer clamp / fallback for an empty page.
//
// Each orthogonal orientation (0°/90°/180°/270°) is clustered
// INDEPENDENTLY: events are first projected from page space into a
// per-orientation "logical horizontal" frame where the rotated
// baseline runs along +lx and the rotated height extends along +ly.
// The shared clusterer (clusterEvents) runs against the logical
// events to produce Run → Line → Block nesting; the resulting
// bboxes are then projected back to page space via
// logicalToPageRect, and every Block in a rotated pass is tagged
// with Block.Orientation = orient. Doing it this way means rotated
// columns / paragraphs / multi-run lines all get the same
// clustering quality as horizontal text — a 90° watermark with
// two stacked rotated lines now lands as one Block with two
// Lines, not two coarse one-event-per-block records.
func buildBlocksFromEvents(pageID string, pageMedia Rect, events []textEvent) []*Block {
	if len(events) == 0 {
		return []*Block{}
	}

	// Partition by orientation. Stable iteration order
	// {0, 90, 180, 270} keeps NodeIDs deterministic across runs.
	byOrient := map[int][]textEvent{}
	for _, ev := range events {
		byOrient[ev.orient] = append(byOrient[ev.orient], ev)
	}

	out := []*Block{}
	for _, orient := range []int{0, 90, 180, 270} {
		group := byOrient[orient]
		if len(group) == 0 {
			continue
		}
		// Project page-space anchors to the logical horizontal
		// frame for this orientation. orient=0 is the identity
		// map, so horizontal events flow through unchanged.
		projected := make([]textEvent, len(group))
		for i, ev := range group {
			lx, ly := rotatedToLogical(orient, ev.x, ev.y)
			projected[i] = textEvent{
				x: lx, y: ly,
				fontName: ev.fontName,
				fontSize: ev.fontSize,
				text:     ev.text,
				// orient deliberately zeroed — the clusterer
				// should treat this slice as a horizontal frame.
			}
		}
		blocks := clusterEvents(pageID, len(out), projected, pageMedia)
		// Project bboxes back to page space + tag with orientation.
		// orient=0 is an identity transform, so this is a no-op
		// for horizontal blocks.
		for _, b := range blocks {
			b.Orientation = orient
			b.BBox = logicalToPageRect(orient, b.BBox)
			for _, ln := range b.Lines {
				ln.BBox = logicalToPageRect(orient, ln.BBox)
				for _, r := range ln.Runs {
					r.BBox = logicalToPageRect(orient, r.BBox)
				}
			}
		}
		out = append(out, blocks...)
	}
	// Skewed events (orient=-1) get one-block-per-event with
	// a correct page-space AABB derived from the projected
	// text-space corners. Cross-event clustering on skewed
	// text needs transform-similarity grouping (each event can
	// carry its own arbitrary 2x2); that's tracked as a v2
	// follow-up. The coarse output here is still correct —
	// every glyph is findable, redactable, and tagged as
	// skewed so consumers know not to assume axis-aligned
	// baseline geometry.
	if skewed := byOrient[OrientationSkewed]; len(skewed) > 0 {
		out = append(out, buildSkewedBlocks(pageID, len(out), skewed)...)
	}
	return out
}

// buildSkewedBlocks emits one Block per skewed/non-orthogonal
// textEvent. Block.Orientation is set to OrientationSkewed so
// consumers know to treat the AABB as approximate selection
// geometry — the rendered baseline is on a slanted axis that
// isn't exposed at the Block API yet (tracked v2 follow-up).
//
// `startIdx` is the count of Blocks already emitted by prior
// orientation passes; block NodeIDs use (startIdx + 1 ..
// startIdx + N) so skewed blocks never collide with the
// orthogonal ones.
func buildSkewedBlocks(pageID string, startIdx int, events []textEvent) []*Block {
	if len(events) == 0 {
		return nil
	}
	out := make([]*Block, 0, len(events))
	for i, ev := range events {
		idx := startIdx + i + 1
		blockID := NodeID(pageID, "block", idx)
		lineID := NodeID(blockID, "line", 1)
		runID := NodeID(lineID, "run", 1)
		bbox := skewedEventBBox(ev)
		out = append(out, &Block{
			ID:          blockID,
			Type:        BlockText,
			BBox:        bbox,
			Orientation: OrientationSkewed,
			Lines: []*Line{{
				ID:   lineID,
				BBox: bbox,
				Runs: []*Run{{
					ID:     runID,
					Text:   ev.text,
					Font:   ev.fontName,
					SizePt: ev.fontSize,
					BBox:   bbox,
				}},
			}},
		})
	}
	return out
}

// skewedEventBBox returns the page-space AABB of a skewed
// textEvent. The text-space rectangle [0, w_text] × [0, h_text]
// (where w_text = approxWidth at the bare Tf size, and
// h_text = bare Tf size) is projected through the raw 2x2 Tm
// [a b c d] anchored at (ev.x, ev.y); the bbox is the
// min/max of the four projected corners.
//
// For skewed events the bare Tf size is what's stored on the
// event (see the textEvent doc comment), because there's no
// single uniform scale to fold into a "rendered point size".
func skewedEventBBox(ev textEvent) Rect {
	a, b, c, d := ev.tmA, ev.tmB, ev.tmC, ev.tmD
	w := approxWidth(ev.text, ev.fontName, ev.fontSize, 0, 0)
	h := ev.fontSize
	// Four corners of the text-space rect [0,w]×[0,h] projected
	// to page space via Tm:
	//   (0, 0) → (ev.x, ev.y)
	//   (w, 0) → (ev.x + w·a, ev.y + w·b)
	//   (0, h) → (ev.x + h·c, ev.y + h·d)
	//   (w, h) → (ev.x + w·a + h·c, ev.y + w·b + h·d)
	xs := [4]float64{
		ev.x,
		ev.x + w*a,
		ev.x + h*c,
		ev.x + w*a + h*c,
	}
	ys := [4]float64{
		ev.y,
		ev.y + w*b,
		ev.y + h*d,
		ev.y + w*b + h*d,
	}
	xmin, xmax := xs[0], xs[0]
	ymin, ymax := ys[0], ys[0]
	for i := 1; i < 4; i++ {
		if xs[i] < xmin {
			xmin = xs[i]
		}
		if xs[i] > xmax {
			xmax = xs[i]
		}
		if ys[i] < ymin {
			ymin = ys[i]
		}
		if ys[i] > ymax {
			ymax = ys[i]
		}
	}
	return Rect{X0: xmin, Y0: ymin, X1: xmax, Y1: ymax}
}

// clusterEvents runs the core Run → Line → Block clustering
// against events that all share the same "horizontal-looking"
// baseline frame (baseline = +x, height = +y). It does not
// inspect ev.orient — orientation handling happens in the
// caller (buildBlocksFromEvents), which projects to/from this
// frame.
//
// startIdx is the count of Blocks already emitted by prior
// orientation passes; block NodeIDs use (startIdx + 1 ..
// startIdx + N) so the per-orientation passes never collide.
// pageMedia is used only as a final fallback if a block ends
// up with no resolvable bbox (defensive — shouldn't fire in
// practice because every materialised block contains at least
// one line with a finite run).
func clusterEvents(pageID string, startIdx int, events []textEvent, pageMedia Rect) []*Block {
	if len(events) == 0 {
		return nil
	}

	// Sort top-to-bottom (PDF Y descends down the page), then left-to-right.
	sort.SliceStable(events, func(i, j int) bool {
		if !nearlyEqual(events[i].y, events[j].y) {
			return events[i].y > events[j].y // higher Y first (top of page)
		}
		return events[i].x < events[j].x
	})

	// --- Group into Runs (same baseline + same font/size) ---
	type runBuilder struct {
		fontName string
		fontSize float64
		y        float64
		x0, x1   float64
		text     string
	}
	var rawRuns []runBuilder
	const baselineToleranceFraction = 0.25 // 25% of font size
	for _, ev := range events {
		merged := false
		if len(rawRuns) > 0 {
			last := &rawRuns[len(rawRuns)-1]
			tol := math.Max(last.fontSize, ev.fontSize) * baselineToleranceFraction
			if last.fontName == ev.fontName &&
				nearlyEqualEpsilon(last.fontSize, ev.fontSize, 0.01) &&
				math.Abs(last.y-ev.y) <= tol &&
				ev.x >= last.x0 {
				// Same run continues — append text and extend x1.
				last.text += ev.text
				w := approxWidth(ev.text, ev.fontName, ev.fontSize, 0, 0)
				if ev.x+w > last.x1 {
					last.x1 = ev.x + w
				}
				merged = true
			}
		}
		if !merged {
			w := approxWidth(ev.text, ev.fontName, ev.fontSize, 0, 0)
			rawRuns = append(rawRuns, runBuilder{
				fontName: ev.fontName,
				fontSize: ev.fontSize,
				y:        ev.y,
				x0:       ev.x,
				x1:       ev.x + w,
				text:     ev.text,
			})
		}
	}

	// --- Group runs into Lines (overlapping baselines) ---
	type lineBuilder struct {
		yMin, yMax float64 // baseline-Y span
		size       float64 // max font size on the line (drives line height)
		runs       []runBuilder
	}
	var rawLines []lineBuilder
	for _, r := range rawRuns {
		merged := false
		for i := range rawLines {
			ln := &rawLines[i]
			tol := math.Max(ln.size, r.fontSize) * baselineToleranceFraction
			if math.Abs(ln.yMin-r.y) <= tol || math.Abs(ln.yMax-r.y) <= tol {
				ln.runs = append(ln.runs, r)
				if r.y < ln.yMin {
					ln.yMin = r.y
				}
				if r.y > ln.yMax {
					ln.yMax = r.y
				}
				if r.fontSize > ln.size {
					ln.size = r.fontSize
				}
				merged = true
				break
			}
		}
		if !merged {
			rawLines = append(rawLines, lineBuilder{
				yMin: r.y, yMax: r.y, size: r.fontSize, runs: []runBuilder{r},
			})
		}
	}

	// Sort lines top → bottom (Y descending), then runs in each line by x.
	sort.SliceStable(rawLines, func(i, j int) bool {
		return rawLines[i].yMax > rawLines[j].yMax
	})
	for i := range rawLines {
		sort.SliceStable(rawLines[i].runs, func(a, b int) bool {
			return rawLines[i].runs[a].x0 < rawLines[i].runs[b].x0
		})
	}

	// --- Group lines into Blocks (paragraph clusters by vertical gap) ---
	type blockBuilder struct {
		lines []lineBuilder
	}
	var rawBlocks []blockBuilder
	for _, ln := range rawLines {
		merged := false
		if len(rawBlocks) > 0 {
			prevBlock := &rawBlocks[len(rawBlocks)-1]
			prevLn := prevBlock.lines[len(prevBlock.lines)-1]
			// A "natural" line gap is roughly the leading; treat anything
			// up to 1.5× the larger font size as part of the same block.
			maxSize := math.Max(prevLn.size, ln.size)
			gap := prevLn.yMin - ln.yMax
			if gap <= maxSize*1.5 {
				prevBlock.lines = append(prevBlock.lines, ln)
				merged = true
			}
		}
		if !merged {
			rawBlocks = append(rawBlocks, blockBuilder{lines: []lineBuilder{ln}})
		}
	}

	// --- Materialize sPDOM nodes ---
	out := make([]*Block, 0, len(rawBlocks))
	for bi, b := range rawBlocks {
		blockID := NodeID(pageID, "block", startIdx+bi+1)
		bbox := Rect{X0: math.Inf(1), Y0: math.Inf(1), X1: math.Inf(-1), Y1: math.Inf(-1)}
		blockLines := make([]*Line, 0, len(b.lines))
		for li, ln := range b.lines {
			lineID := NodeID(blockID, "line", li+1)
			lineBBox := Rect{X0: math.Inf(1), Y0: math.Inf(1), X1: math.Inf(-1), Y1: math.Inf(-1)}
			runs := make([]*Run, 0, len(ln.runs))
			for ri, r := range ln.runs {
				runID := NodeID(lineID, "run", ri+1)
				runBBox := Rect{
					X0: r.x0, Y0: r.y,
					X1: r.x1, Y1: r.y + r.fontSize,
				}
				runs = append(runs, &Run{
					ID:     runID,
					Text:   r.text,
					Font:   r.fontName,
					SizePt: r.fontSize,
					BBox:   runBBox,
				})
				lineBBox = expand(lineBBox, runBBox)
			}
			blockLines = append(blockLines, &Line{
				ID: lineID, BBox: lineBBox, Runs: runs,
			})
			bbox = expand(bbox, lineBBox)
		}
		// If for some reason bbox stayed unset, fall back to mediaBox so
		// downstream code never sees inf/inf.
		if math.IsInf(bbox.X0, 1) {
			bbox = pageMedia
		}
		out = append(out, &Block{
			ID:    blockID,
			Type:  BlockText,
			BBox:  bbox,
			Lines: blockLines,
		})
	}
	return out
}

// rotatedToLogical projects a page-space anchor (x, y) of an event
// at the given orientation into the "logical horizontal" frame the
// clusterer expects: baseline runs along +lx, height extends along
// +ly. The inverse — used to project bboxes back after clustering
// — is logicalToPageRect.
//
// Identity for 0°. The other three transforms are derived from
// the Tm classification in extractTextEvents:
//
//	90°:  baseline = +page_y, height = -page_x  →  lx = +y, ly = -x
//	180°: baseline = -page_x, height = -page_y  →  lx = -x, ly = -y
//	270°: baseline = -page_y, height = +page_x  →  lx = -y, ly = +x
//
// Choosing per-orientation transforms (rather than a per-event
// translation) means every rotated event in the same group shares
// one logical frame — so when two glyphs on the same rotated
// baseline arrive, they project to the same logical y and the
// clusterer merges them into one Line / Run just like horizontal
// text. Multi-line rotated paragraphs and rotated columns work
// the same way.
func rotatedToLogical(orient int, x, y float64) (lx, ly float64) {
	switch orient {
	case 90:
		return y, -x
	case 180:
		return -x, -y
	case 270:
		return -y, x
	default:
		return x, y
	}
}

// logicalToPageRect maps a bbox produced by the clusterer in the
// logical horizontal frame back to page space. Inverse of
// rotatedToLogical applied corner-wise; identity for 0°.
//
// For each orientation we explicitly pick which logical corner
// becomes which page corner so the returned Rect remains
// well-formed (X0 ≤ X1, Y0 ≤ Y1):
//
//	90°:  page_x = -ly, page_y =  lx
//	180°: page_x = -lx, page_y = -ly
//	270°: page_x =  ly, page_y = -lx
func logicalToPageRect(orient int, lr Rect) Rect {
	switch orient {
	case 90:
		return Rect{X0: -lr.Y1, Y0: lr.X0, X1: -lr.Y0, Y1: lr.X1}
	case 180:
		return Rect{X0: -lr.X1, Y0: -lr.Y1, X1: -lr.X0, Y1: -lr.Y0}
	case 270:
		return Rect{X0: lr.Y0, Y0: -lr.X1, X1: lr.Y1, Y1: -lr.X0}
	default:
		return lr
	}
}

// expand returns the smallest rect containing both a and b.
func expand(a, b Rect) Rect {
	out := a
	if math.IsInf(out.X0, 1) {
		return b
	}
	if b.X0 < out.X0 {
		out.X0 = b.X0
	}
	if b.Y0 < out.Y0 {
		out.Y0 = b.Y0
	}
	if b.X1 > out.X1 {
		out.X1 = b.X1
	}
	if b.Y1 > out.Y1 {
		out.Y1 = b.Y1
	}
	return out
}

func nearlyEqualEpsilon(a, b, eps float64) bool { return math.Abs(a-b) <= eps }
