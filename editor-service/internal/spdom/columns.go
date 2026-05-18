package spdom

import (
	"math"
)

// BuildBlocksColumnAware is the reading-order-aware wrapper around
// [buildBlocksFromEvents]. For single-column pages it produces an
// identical result; for multi-column or otherwise partitioned
// layouts it runs the recursive XY-cut algorithm
// ([xyCut] in xycut.go) to slice the page into reading-order
// sub-regions and clusters each region's events independently.
//
// XY-cut handles:
//   - 2-column papers (one vertical cut → left, right).
//   - 3+ column layouts (recursive vertical cuts).
//   - Header banner + 2-column body (horizontal cut, then
//     vertical cut on the lower half).
//   - Two-up reports (vertical cut between the page halves).
//   - Sidebars / asides (vertical cut leaving a thin column).
//
// The previous single-pass clusterer mis-merged lines from
// different columns at the same baseline into one line; routing
// each region's events through their own clusterer fixes that
// without changing the L4 model shape.
//
// When XY-cut returns a single region (no qualifying gap on
// either axis), the function short-circuits to the single-pass
// build — no overhead for the common single-column case.
func BuildBlocksColumnAware(pageID string, pageMedia Rect, events []textEvent) []*Block {
	regions := xyCut(events, 0)
	if len(regions) <= 1 {
		return buildBlocksFromEvents(pageID, pageMedia, events)
	}
	// Run the line/block clusterer once per region, then
	// concatenate with stable, non-colliding NodeIDs.
	out := make([]*Block, 0, len(regions))
	idx := 1
	for _, region := range regions {
		if len(region) == 0 {
			continue
		}
		blocks := buildBlocksFromEvents(pageID, pageMedia, region)
		for _, b := range blocks {
			out = append(out, renumberBlock(b, pageID, idx))
			idx++
		}
	}
	return out
}

// detectColumnGap returns the X coordinate (in page space) of a
// vertical whitespace strip that splits the page into two
// columns. Returns 0 if no qualifying gap exists.
//
// Algorithm:
//  1. Bucket page-space X-coordinates into 1pt-resolution bins
//     spanning [MediaBox.X0, MediaBox.X1]. For each text event,
//     mark every bin its glyph cluster's bbox covers.
//  2. Find the longest run of unmarked bins.
//  3. Accept the run as a column gap if:
//     - it is at least 5% of page width wide (cuts noise);
//     - its centre lies in the central 60% of the page (rejects
//     margins, which are also unmarked runs).
//
// We deliberately don't try to detect 3+ columns from a single
// bitmap — distinguishing 3 columns from "2 columns plus a margin
// figure" needs the recursive XY-cut algorithm proper. 2 columns
// covers the vast majority of academic / report layouts and
// matches plan §5.4 step 6's pragmatic shipping target.
func detectColumnGap(pageMedia Rect, events []textEvent) float64 {
	if len(events) < 4 {
		// Not enough text on the page to make a reliable
		// column call. A single line with a wide tab gap could
		// otherwise be misclassified.
		return 0
	}
	width := int(math.Round(pageMedia.X1 - pageMedia.X0))
	if width <= 0 {
		return 0
	}
	occupied := make([]bool, width)
	for _, ev := range events {
		w := approxWidth(ev.text, ev.fontName, ev.fontSize, 0, 0)
		// Convert page-space coords to bitmap-space bins.
		x0 := int(math.Floor(ev.x - pageMedia.X0))
		x1 := int(math.Ceil(ev.x + w - pageMedia.X0))
		if x0 < 0 {
			x0 = 0
		}
		if x1 > width {
			x1 = width
		}
		for x := x0; x < x1; x++ {
			occupied[x] = true
		}
	}

	// Heuristic thresholds — tuned empirically against the
	// usual paper sizes (US Letter, A4) at common margins.
	minGap := width / 20
	centralLo := width * 20 / 100
	centralHi := width * 80 / 100

	// Walk EVERY zero-run, not just the longest. The longest
	// is usually a page margin (text fills one half, the other
	// half is empty); a real column gap is narrower but
	// satisfies the "text on both sides" + "centred" tests.
	bestStart, bestLen := -1, 0
	for start, length := range zeroRuns(occupied) {
		if length < minGap {
			continue
		}
		center := start + length/2
		if center < centralLo || center > centralHi {
			continue
		}
		// Confirm there's at least one occupied bin on EACH
		// side of the gap — otherwise this is a margin abutting
		// content, not a column separator.
		if !hasAnyTrue(occupied, 0, start) {
			continue
		}
		if !hasAnyTrue(occupied, start+length, len(occupied)) {
			continue
		}
		if length > bestLen {
			bestStart = start
			bestLen = length
		}
	}
	if bestStart < 0 {
		return 0
	}
	return pageMedia.X0 + float64(bestStart+bestLen/2)
}

// zeroRuns returns every maximal run of false values in `occ` as
// a sequence of (start, length) pairs visited via the yield
// function. Using range-func style keeps the caller's loop a
// simple `for start, length := range zeroRuns(occ)` instead of
// an index-management dance.
func zeroRuns(occ []bool) func(yield func(int, int) bool) {
	return func(yield func(int, int) bool) {
		curStart := -1
		for x, o := range occ {
			if !o {
				if curStart < 0 {
					curStart = x
				}
				continue
			}
			if curStart >= 0 {
				if !yield(curStart, x-curStart) {
					return
				}
				curStart = -1
			}
		}
		if curStart >= 0 {
			yield(curStart, len(occ)-curStart)
		}
	}
}

// hasAnyTrue reports whether occ[lo:hi] contains at least one
// true bin. Used to check that a candidate column gap has text
// flanking it on both sides — a margin only has text on one
// side, which is how we tell margins from real column gaps.
func hasAnyTrue(occ []bool, lo, hi int) bool {
	if lo < 0 {
		lo = 0
	}
	if hi > len(occ) {
		hi = len(occ)
	}
	for i := lo; i < hi; i++ {
		if occ[i] {
			return true
		}
	}
	return false
}

// longestZeroRun scans the bitmap for the longest contiguous run
// of false values. Returns (startIdx, length). length==0 means
// the bitmap was all true (no gaps at all).
func longestZeroRun(occupied []bool) (int, int) {
	bestStart, bestLen := 0, 0
	curStart := -1
	for x, occ := range occupied {
		if !occ {
			if curStart < 0 {
				curStart = x
			}
			continue
		}
		if curStart >= 0 {
			if runLen := x - curStart; runLen > bestLen {
				bestLen = runLen
				bestStart = curStart
			}
			curStart = -1
		}
	}
	if curStart >= 0 {
		if runLen := len(occupied) - curStart; runLen > bestLen {
			bestLen = runLen
			bestStart = curStart
		}
	}
	return bestStart, bestLen
}

// splitEventsByX partitions `events` into a left set (events
// whose starting X is below `gap`) and a right set (everything
// else). The relative order within each partition is preserved.
//
// Splitting by *start* x rather than by glyph-centroid is the
// simpler call and works because real PDFs never stretch a single
// Tj across a column gap — the producer breaks the line into one
// run per column.
func splitEventsByX(events []textEvent, gap float64) (left, right []textEvent) {
	for _, ev := range events {
		if ev.x < gap {
			left = append(left, ev)
		} else {
			right = append(right, ev)
		}
	}
	return left, right
}

// renumberBlock returns a copy of `b` whose ID + line/run IDs
// are rewritten with the given sequence number. The block's
// content (text, bboxes, font info) is preserved verbatim.
//
// We reconstruct the IDs rather than walking + patching the
// old strings: the existing NodeID helper is the single source
// of truth for the format ("{parent}/block-N", "{parent}/line-M",
// etc.) and re-running it keeps any future format change in one
// place.
func renumberBlock(b *Block, pageID string, idx int) *Block {
	blockID := NodeID(pageID, "block", idx)
	newLines := make([]*Line, len(b.Lines))
	for li, ln := range b.Lines {
		lineID := NodeID(blockID, "line", li+1)
		newRuns := make([]*Run, len(ln.Runs))
		for ri, r := range ln.Runs {
			cp := *r
			cp.ID = NodeID(lineID, "run", ri+1)
			newRuns[ri] = &cp
		}
		newLines[li] = &Line{
			ID:   lineID,
			BBox: ln.BBox,
			Runs: newRuns,
		}
	}
	return &Block{
		ID:    blockID,
		Type:  b.Type,
		BBox:  b.BBox,
		Lines: newLines,
	}
}

