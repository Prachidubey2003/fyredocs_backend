package spdom

import (
	"math"
)

// xyCut recursively partitions `events` into reading-order regions
// using the classic XY-cut algorithm:
//
//  1. Project every event's bbox onto the X axis and the Y axis to
//     get two 1pt occupancy bitmaps.
//  2. Find the widest qualifying whitespace strip on each axis
//     (centred within the events' own bbox, ≥ 5% of that
//     dimension, with text on BOTH sides).
//  3. If neither axis has a qualifying strip, the region is
//     atomic — emit it as a single sub-list.
//  4. Otherwise, cut along the axis whose strip is wider. The
//     halves are recursed on independently. Reading order:
//     top-half first for horizontal cuts (higher Y in PDF
//     coords); left-half first for vertical cuts.
//
// Returns a slice of event slices in reading order. Total events
// across all returned regions equals len(events).
//
// Bounded by maxXYCutDepth so a pathological input (every word
// surrounded by whitespace) can't produce O(N) recursion.
func xyCut(events []textEvent, depth int) [][]textEvent {
	if depth >= maxXYCutDepth || len(events) < minEventsPerRegion {
		return [][]textEvent{events}
	}

	bbox := eventBBox(events)
	if bbox.X1-bbox.X0 <= 0 || bbox.Y1-bbox.Y0 <= 0 {
		return [][]textEvent{events}
	}

	vCut, vGap := findGapAlong(events, bbox, axisX)
	hCut, hGap := findGapAlong(events, bbox, axisY)

	// No qualifying strip on either axis — atomic region.
	if vGap == 0 && hGap == 0 {
		return [][]textEvent{events}
	}

	// Prefer the wider gap. Ties break toward horizontal cuts
	// (top-to-bottom reading) since most layouts read row-first.
	if hGap >= vGap && hGap > 0 {
		top, bottom := splitEventsByY(events, hCut)
		// PDF Y increases upward, so "top" = higher Y. Reading
		// order is top-down: emit higher-Y region first.
		return appendRegions(xyCut(top, depth+1), xyCut(bottom, depth+1))
	}

	left, right := splitEventsByX(events, vCut)
	return appendRegions(xyCut(left, depth+1), xyCut(right, depth+1))
}

// XY-cut tuning knobs. Kept as package-level consts so tests can
// reason about them and a future caller can override via a copy
// of the algorithm without forking the production path.
//
//   - maxXYCutDepth caps recursion so a degenerate page (e.g.
//     whitespace-rich poetry) doesn't blow the stack.
//   - minEventsPerRegion stops the recursion when a region is too
//     small to be a meaningful column — single-event sub-regions
//     would produce trivial one-glyph "columns".
//   - minGapFraction is the minimum strip width relative to the
//     region's dimension (5% matches the 2-column detector).
//   - minAbsoluteGapFactor floors the strip width at this many ×
//     mean font size, so tight sub-regions don't mistake normal
//     line leading (axisY) or inter-word spacing (axisX) for a
//     qualifying cut.
//   - centralFraction is the [low, high] envelope within which a
//     strip's center must lie. Same 20%–80% gates as the 2-column
//     detector, applied recursively to the region's bbox.
const (
	maxXYCutDepth        = 6
	minEventsPerRegion   = 4
	minGapFraction       = 0.05
	minAbsoluteGapFactor = 1.5
	centralLoFraction    = 0.20
	centralHiFraction    = 0.80
)

// axis identifies which dimension a gap-finder operates on.
// axisX = vertical whitespace strip (separates left/right columns).
// axisY = horizontal whitespace strip (separates top/bottom bands).
type axis int

const (
	axisX axis = iota
	axisY
)

// findGapAlong projects every event's bbox onto `ax` within `bbox`
// and returns (cutPos, gapWidth) for the widest qualifying empty
// strip. gapWidth == 0 means no qualifying strip exists.
//
// "Qualifying" means:
//   - at least max(minGapFraction × bbox-dim, minAbsoluteGapFactor
//     × meanFontSize) wide. The absolute floor stops tiny sub-
//     regions from mis-detecting line spacing as a column gap on
//     axisX, or paragraph leading as a section break on axisY.
//   - centred within [centralLoFraction, centralHiFraction] of the
//     bbox, with at least one occupied bin on EACH side (a margin-
//     abutting empty run is NOT a qualifying gap).
//
// cutPos is in page-space coordinates (origin = bbox.X0 for axisX,
// bbox.Y0 for axisY) so the splitter helpers can use it directly.
func findGapAlong(events []textEvent, bbox Rect, ax axis) (float64, float64) {
	var lo, hi float64
	switch ax {
	case axisX:
		lo, hi = bbox.X0, bbox.X1
	case axisY:
		lo, hi = bbox.Y0, bbox.Y1
	}
	span := int(math.Round(hi - lo))
	if span <= 0 {
		return 0, 0
	}

	occupied := make([]bool, span)
	totalFontSize := 0.0
	for _, ev := range events {
		totalFontSize += ev.fontSize
		var b0, b1 float64
		switch ax {
		case axisX:
			b0 = ev.x
			b1 = ev.x + approxWidth(ev.text, ev.fontName, ev.fontSize, 0, 0)
		case axisY:
			// Approximate the line height: the rendered text
			// occupies (y, y + fontSize) — i.e. the baseline is
			// at y and ascenders climb fontSize points above.
			// This is the same conservative line-height model
			// the layout clusterer assumes downstream.
			b0 = ev.y
			b1 = ev.y + ev.fontSize
		}
		i0 := int(math.Floor(b0 - lo))
		i1 := int(math.Ceil(b1 - lo))
		if i0 < 0 {
			i0 = 0
		}
		if i1 > span {
			i1 = span
		}
		for i := i0; i < i1; i++ {
			occupied[i] = true
		}
	}

	meanFontSize := 12.0
	if len(events) > 0 {
		meanFontSize = totalFontSize / float64(len(events))
	}
	// Absolute floor: an axisY gap below 1.5× mean font size is
	// almost certainly normal paragraph leading, not a section
	// break. An axisX gap below 1.5× mean font size is almost
	// certainly inter-word spacing. Either way: don't cut.
	absMin := int(minAbsoluteGapFactor * meanFontSize)
	minGap := int(float64(span) * minGapFraction)
	if absMin > minGap {
		minGap = absMin
	}

	centralLo := int(float64(span) * centralLoFraction)
	centralHi := int(float64(span) * centralHiFraction)

	bestStart, bestLen := -1, 0
	for start, length := range zeroRuns(occupied) {
		if length < minGap {
			continue
		}
		center := start + length/2
		if center < centralLo || center > centralHi {
			continue
		}
		if !hasAnyTrue(occupied, 0, start) {
			continue
		}
		if !hasAnyTrue(occupied, start+length, span) {
			continue
		}
		if length > bestLen {
			bestStart = start
			bestLen = length
		}
	}
	if bestStart < 0 {
		return 0, 0
	}
	return lo + float64(bestStart+bestLen/2), float64(bestLen)
}

// splitEventsByY partitions `events` into a top set (events whose
// origin is at or above `gap`) and a bottom set (everything else).
// "Top" = higher Y in PDF coordinates (origin bottom-left).
//
// This mirrors splitEventsByX's contract but along the orthogonal
// axis. The relative order within each partition is preserved.
func splitEventsByY(events []textEvent, gap float64) (top, bottom []textEvent) {
	for _, ev := range events {
		if ev.y >= gap {
			top = append(top, ev)
		} else {
			bottom = append(bottom, ev)
		}
	}
	return top, bottom
}

// eventBBox returns the tightest bounding rectangle around every
// event in `events`. Each event's right edge is approximated via
// approxWidth (uses AFM metrics for core fonts, 0.5em-per-glyph
// otherwise — see widths.go).
//
// Returns the zero Rect when events is empty; callers should
// guard against degenerate bounds.
func eventBBox(events []textEvent) Rect {
	if len(events) == 0 {
		return Rect{}
	}
	out := Rect{
		X0: math.Inf(1), Y0: math.Inf(1),
		X1: math.Inf(-1), Y1: math.Inf(-1),
	}
	for _, ev := range events {
		w := approxWidth(ev.text, ev.fontName, ev.fontSize, 0, 0)
		if ev.x < out.X0 {
			out.X0 = ev.x
		}
		if ev.x+w > out.X1 {
			out.X1 = ev.x + w
		}
		if ev.y < out.Y0 {
			out.Y0 = ev.y
		}
		if ev.y+ev.fontSize > out.Y1 {
			out.Y1 = ev.y + ev.fontSize
		}
	}
	return out
}

// appendRegions concatenates two region lists. A trivial helper
// kept separate so xyCut's recursive returns read top-to-bottom
// without an explicit `append(a, b...)` ceremony on every call.
func appendRegions(a, b [][]textEvent) [][]textEvent {
	out := make([][]textEvent, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}
