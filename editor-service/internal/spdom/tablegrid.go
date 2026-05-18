package spdom

import (
	"math"
	"sort"
)

// Cell is one detected table cell. Coordinates are in PDF
// user space (points, origin bottom-left). Row 0 / Col 0 sits
// at the top-left of the detected grid.
type Cell struct {
	Row  int  `json:"row"`
	Col  int  `json:"col"`
	Rect Rect `json:"rect"`
}

// Tunables for the regularity heuristics. Picked to accept
// typical invoice / schedule / data tables while rejecting
// loose paragraphs that happen to share X anchors.
const (
	// rowPitchVarianceMax is the maximum (stdev / mean) ratio
	// of inter-row baseline gaps we accept as "regular".
	// 25% is roomy enough to absorb font-rendering rounding +
	// hand-laid spacing but tight enough that paragraph
	// blocks (with paragraph breaks 2× the line height) get
	// rejected.
	rowPitchVarianceMax = 0.25

	// columnAnchorTolerancePt is how far apart two events'
	// start-X can be and still belong to the same column
	// anchor. 4 points covers Helvetica's longest variable-
	// width-glyph delta without merging neighbouring columns.
	columnAnchorTolerancePt = 4.0

	// minRows / minCols below which a region is too sparse
	// to call "a table". A 1×N or N×1 region is technically a
	// list, not a table.
	minRows = 2
	minCols = 2

	// rowYToleranceFactor multiplied by mean fontSize defines
	// how close in Y two events must be to be considered
	// "same row". 0.4 means same line if their baselines are
	// within 40% of fontSize — tight enough to split distinct
	// rows in a tightly-leaded table (1.2× leading), loose
	// enough that font-internal baseline variation doesn't
	// produce phantom rows.
	rowYToleranceFactor = 0.4

	// columnCoverageMin is the fraction of rows that must
	// have an event at an X-cluster for it to count as a real
	// column anchor. 0.5 = at least half the rows must hit
	// the anchor — rejects single-row outliers (e.g. a
	// section header that bleeds into the table region).
	columnCoverageMin = 0.5
)

// DetectTableGrid runs the structural detection over the
// supplied content stream and returns the cells if the
// `region` looks like a regular table grid. Returns
// (nil, false) when the region is too sparse, too irregular,
// or the stream uses unsupported text-matrix shapes
// (rotated / skewed / mirrored — same `supported=false`
// posture as `extractTextEvents`).
//
// `fontMap` is the page's resource-name → BaseFont map
// (see extractPageFontMap). Pass nil if it's unavailable —
// the algorithm doesn't depend on font names for detection
// (it's a layout-only signal), but pre-resolving them lets
// downstream measurement reach pdfcpu's AFM tables for
// tighter cell-edge math.
//
// Public entry point. The pure-events sibling
// `detectTableGridFromEvents` is internal so tests can
// exercise the algorithm without round-tripping through a
// content-stream parser.
func DetectTableGrid(stream []byte, fontMap map[string]string, region Rect) ([]Cell, bool) {
	events, ok := extractTextEvents(stream, fontMap)
	if !ok {
		return nil, false
	}
	return detectTableGridFromEvents(events, region)
}

// detectTableGridFromEvents is the layout-only algorithm.
// Filters events to the region, clusters into rows, derives
// column anchors from the union of per-row event start-X's,
// validates regularity, and emits one Cell per (row, col).
//
// v0 limitations (tracked):
//
//   - Single-line-per-cell only. Multi-line wrapped cells
//     surface as N rows and the row-pitch check rejects them
//     (returns false). The wrap-aware variant would need to
//     fold tight-baseline neighbours into the same row.
//   - Horizontal-only. Rotated tables aren't considered —
//     they'd flow through orient != 0 and get filtered out.
//   - One missing cell per row is acceptable; two or more
//     reject the entire region as irregular. Sparse tables
//     are caller's responsibility to chunk before detection.
func detectTableGridFromEvents(events []textEvent, region Rect) ([]Cell, bool) {
	inside := filterEventsInRegion(events, region)
	if len(inside) == 0 {
		return nil, false
	}

	rows := clusterIntoRows(inside)
	if len(rows) < minRows {
		return nil, false
	}

	if !rowPitchIsRegular(rows) {
		return nil, false
	}

	anchors := detectColumnAnchors(rows)
	if len(anchors) < minCols {
		return nil, false
	}

	if !rowsHaveConsistentColumnHits(rows, anchors) {
		return nil, false
	}

	return emitCells(rows, anchors, region), true
}

// filterEventsInRegion keeps only horizontal events (orient
// == 0) whose baseline origin lies inside `region`. A baseline
// origin (x, y) is INSIDE when both axes are within the
// rect's inclusive bounds — boundary events count as
// in-region so a caller passing the exact page bbox
// doesn't accidentally exclude top-row content.
func filterEventsInRegion(events []textEvent, region Rect) []textEvent {
	if region.X1 <= region.X0 || region.Y1 <= region.Y0 {
		return nil
	}
	out := make([]textEvent, 0, len(events))
	for _, e := range events {
		if e.orient != 0 {
			continue
		}
		if e.x < region.X0 || e.x > region.X1 {
			continue
		}
		if e.y < region.Y0 || e.y > region.Y1 {
			continue
		}
		out = append(out, e)
	}
	return out
}

// rowCluster groups events that share a baseline within
// rowYToleranceFactor × meanFontSize.
type rowCluster struct {
	y      float64 // mean baseline of the cluster
	height float64 // mean fontSize → used for the cell Y extent
	events []textEvent
}

// clusterIntoRows walks events top-to-bottom (descending Y in
// PDF coordinates) and groups events whose Y is within
// tolerance of the current row's running mean. Tolerance is
// derived from the mean fontSize across the input, not a
// hard-coded number of points, so the algorithm adapts to
// the document's text size.
func clusterIntoRows(events []textEvent) []rowCluster {
	if len(events) == 0 {
		return nil
	}

	// Sort by Y descending so we walk top-first.
	sorted := make([]textEvent, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].y > sorted[j].y
	})

	meanFontSize := meanEventFontSize(sorted)
	tol := rowYToleranceFactor * meanFontSize
	if tol <= 0 {
		tol = 2.0 // safety net for malformed input
	}

	var rows []rowCluster
	current := rowCluster{
		y:      sorted[0].y,
		height: sorted[0].fontSize,
		events: []textEvent{sorted[0]},
	}
	for _, e := range sorted[1:] {
		if math.Abs(e.y-current.y) <= tol {
			current.events = append(current.events, e)
			// Running mean for the row Y to absorb baseline
			// jitter as we accumulate events.
			current.y = meanY(current.events)
			current.height = meanFontSize2(current.events)
			continue
		}
		rows = append(rows, current)
		current = rowCluster{
			y:      e.y,
			height: e.fontSize,
			events: []textEvent{e},
		}
	}
	rows = append(rows, current)
	return rows
}

// rowPitchIsRegular reports whether the gaps between
// consecutive rows look table-like — small stdev/mean ratio.
// A paragraph block (alternating tight-line / wide-paragraph
// gaps) fails this check.
func rowPitchIsRegular(rows []rowCluster) bool {
	if len(rows) < 2 {
		return false
	}
	gaps := make([]float64, 0, len(rows)-1)
	for i := 1; i < len(rows); i++ {
		// Rows are sorted top-first (descending Y); positive
		// gap is the absolute baseline-to-baseline distance.
		gaps = append(gaps, rows[i-1].y-rows[i].y)
	}
	mean := meanFloat(gaps)
	if mean <= 0 {
		return false
	}
	stdev := stdevFloat(gaps, mean)
	return (stdev / mean) <= rowPitchVarianceMax
}

// detectColumnAnchors derives the X-positions where columns
// start. Across all rows, take the set of every event's
// start-X; group them by `columnAnchorTolerancePt`; keep
// clusters that appear in ≥ columnCoverageMin × rowCount rows.
//
// The anchors are returned in ascending X order so caller
// code can iterate left-to-right.
func detectColumnAnchors(rows []rowCluster) []float64 {
	if len(rows) == 0 {
		return nil
	}

	// Gather distinct anchors per row (one per cluster of
	// events at similar X within the same row — handles a
	// cell with multiple text runs in the same column).
	type rowAnchors struct {
		xs []float64
	}
	rowSets := make([]rowAnchors, len(rows))
	for i, r := range rows {
		rowSets[i].xs = uniqueXClustersWithin(r.events, columnAnchorTolerancePt)
	}

	// Aggregate all anchors across rows; cluster again to
	// find the global column edges.
	var all []float64
	for _, rs := range rowSets {
		all = append(all, rs.xs...)
	}
	sort.Float64s(all)

	type clusterAcc struct {
		sum   float64
		count int
		rows  map[int]bool
	}
	var clusters []*clusterAcc
	var current *clusterAcc
	allByRow := make([][]float64, len(rowSets))
	for i, rs := range rowSets {
		allByRow[i] = rs.xs
	}
	// Walk aggregated X values; group neighbours within
	// tolerance.
	for _, x := range all {
		if current == nil || x-(current.sum/float64(current.count)) > columnAnchorTolerancePt {
			current = &clusterAcc{sum: x, count: 1, rows: map[int]bool{}}
			clusters = append(clusters, current)
		} else {
			current.sum += x
			current.count++
		}
	}

	// For each cluster, count how many rows contain an X
	// inside its tolerance band.
	for _, c := range clusters {
		mean := c.sum / float64(c.count)
		for ri, xs := range allByRow {
			for _, x := range xs {
				if math.Abs(x-mean) <= columnAnchorTolerancePt {
					c.rows[ri] = true
					break
				}
			}
		}
	}

	threshold := int(math.Ceil(columnCoverageMin * float64(len(rows))))
	if threshold < 1 {
		threshold = 1
	}
	var keep []float64
	for _, c := range clusters {
		if len(c.rows) >= threshold {
			keep = append(keep, c.sum/float64(c.count))
		}
	}
	sort.Float64s(keep)
	return keep
}

// rowsHaveConsistentColumnHits reports whether each row's
// events line up with the detected anchors. We allow at most
// one missing anchor per row (empty cell) but reject rows
// that hit fewer than (anchors-1).
func rowsHaveConsistentColumnHits(rows []rowCluster, anchors []float64) bool {
	if len(anchors) < minCols {
		return false
	}
	for _, r := range rows {
		hit := 0
		for _, a := range anchors {
			for _, e := range r.events {
				if math.Abs(e.x-a) <= columnAnchorTolerancePt {
					hit++
					break
				}
			}
		}
		if hit < len(anchors)-1 {
			return false
		}
	}
	return true
}

// emitCells turns the (rows × anchors) detection into a flat
// slice of Cell. Each cell's X range is the anchor to the
// next anchor (or region.X1 for the last column); Y range is
// the row's baseline up by 1.2× fontSize (typical leading)
// and down by 0.2× fontSize for descenders.
func emitCells(rows []rowCluster, anchors []float64, region Rect) []Cell {
	out := make([]Cell, 0, len(rows)*len(anchors))
	for ri, r := range rows {
		for ci, a := range anchors {
			var x1 float64
			if ci+1 < len(anchors) {
				x1 = anchors[ci+1]
			} else {
				x1 = region.X1
			}
			// PDF y grows upward; baseline y plus ~font-size
			// above the baseline covers the glyph ascender,
			// minus ~0.2 fontSize below covers descenders.
			top := r.y + r.height
			bot := r.y - 0.2*r.height
			out = append(out, Cell{
				Row:  ri,
				Col:  ci,
				Rect: Rect{X0: a, Y0: bot, X1: x1, Y1: top},
			})
		}
	}
	return out
}

// uniqueXClustersWithin returns the cluster means of event
// start-Xs that are within `tol` of each other. Used per-row
// so a cell containing multiple Tj runs collapses to one
// anchor.
func uniqueXClustersWithin(events []textEvent, tol float64) []float64 {
	if len(events) == 0 {
		return nil
	}
	xs := make([]float64, 0, len(events))
	for _, e := range events {
		xs = append(xs, e.x)
	}
	sort.Float64s(xs)
	var out []float64
	current := xs[0]
	count := 1
	sum := xs[0]
	for _, x := range xs[1:] {
		if x-current <= tol {
			sum += x
			count++
			current = sum / float64(count)
			continue
		}
		out = append(out, sum/float64(count))
		current = x
		sum = x
		count = 1
	}
	out = append(out, sum/float64(count))
	return out
}

// Helpers — small numeric utilities; deliberately not
// exported.

func meanEventFontSize(events []textEvent) float64 {
	if len(events) == 0 {
		return 0
	}
	var sum float64
	for _, e := range events {
		sum += e.fontSize
	}
	return sum / float64(len(events))
}

func meanFontSize2(events []textEvent) float64 { return meanEventFontSize(events) }

func meanY(events []textEvent) float64 {
	var sum float64
	for _, e := range events {
		sum += e.y
	}
	return sum / float64(len(events))
}

func meanFloat(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func stdevFloat(xs []float64, mean float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var sq float64
	for _, x := range xs {
		d := x - mean
		sq += d * d
	}
	return math.Sqrt(sq / float64(len(xs)-1))
}
