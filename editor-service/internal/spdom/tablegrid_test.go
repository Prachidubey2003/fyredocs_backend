package spdom

import (
	"math"
	"testing"
)

// ev is a tiny constructor for textEvent fixtures so each
// test stays readable. Only the fields the detection cares
// about (orient, x, y, fontSize) need defaults; the others
// stay zero.
func ev(x, y, size float64) textEvent {
	return textEvent{
		x:        x,
		y:        y,
		fontSize: size,
		orient:   0,
	}
}

func TestDetectTableGridFromEvents_RecognisesSimple3x3Grid(t *testing.T) {
	// 3 rows × 3 cols. Row pitch 20pt, column starts at
	// x=100, 200, 300. Each cell has one event at the
	// column's start-x.
	events := []textEvent{
		ev(100, 700, 12), ev(200, 700, 12), ev(300, 700, 12),
		ev(100, 680, 12), ev(200, 680, 12), ev(300, 680, 12),
		ev(100, 660, 12), ev(200, 660, 12), ev(300, 660, 12),
	}
	cells, ok := detectTableGridFromEvents(events, Rect{X0: 50, Y0: 600, X1: 500, Y1: 750})
	if !ok {
		t.Fatalf("expected ok=true for a regular 3x3 grid")
	}
	if len(cells) != 9 {
		t.Errorf("got %d cells, want 9", len(cells))
	}
	// First cell is row 0, col 0 — top-left.
	if cells[0].Row != 0 || cells[0].Col != 0 {
		t.Errorf("first cell = (row %d, col %d), want (0, 0)", cells[0].Row, cells[0].Col)
	}
	// Last cell is row 2, col 2 — bottom-right.
	last := cells[len(cells)-1]
	if last.Row != 2 || last.Col != 2 {
		t.Errorf("last cell = (row %d, col %d), want (2, 2)", last.Row, last.Col)
	}
}

func TestDetectTableGridFromEvents_RejectsSingleRow(t *testing.T) {
	// 1 row × 3 cols. Not a table by our definition.
	events := []textEvent{
		ev(100, 700, 12), ev(200, 700, 12), ev(300, 700, 12),
	}
	_, ok := detectTableGridFromEvents(events, Rect{X0: 0, Y0: 0, X1: 500, Y1: 800})
	if ok {
		t.Error("expected ok=false for single-row region")
	}
}

func TestDetectTableGridFromEvents_RejectsSingleColumn(t *testing.T) {
	// 3 rows × 1 col → list, not a table.
	events := []textEvent{
		ev(100, 700, 12),
		ev(100, 680, 12),
		ev(100, 660, 12),
	}
	_, ok := detectTableGridFromEvents(events, Rect{X0: 0, Y0: 0, X1: 500, Y1: 800})
	if ok {
		t.Error("expected ok=false for single-column region")
	}
}

func TestDetectTableGridFromEvents_RejectsIrregularRowPitch(t *testing.T) {
	// Three rows at y=700, 680, 600. Pitch: 20pt, then 80pt.
	// Mean 50, stdev 30 → ratio 60% — well past the 25% cap.
	events := []textEvent{
		ev(100, 700, 12), ev(200, 700, 12),
		ev(100, 680, 12), ev(200, 680, 12),
		ev(100, 600, 12), ev(200, 600, 12),
	}
	_, ok := detectTableGridFromEvents(events, Rect{X0: 0, Y0: 0, X1: 500, Y1: 800})
	if ok {
		t.Error("expected ok=false for irregular row pitch")
	}
}

func TestDetectTableGridFromEvents_AllowsOneMissingCellPerRow(t *testing.T) {
	// 3x3 grid, but row 1 has no entry at col 1 (empty cell).
	// The relaxed coverage rule should still detect this as a
	// table; the empty cell still emits with the bounds of
	// the missing column.
	events := []textEvent{
		ev(100, 700, 12), ev(200, 700, 12), ev(300, 700, 12),
		ev(100, 680, 12) /* col 1 missing */, ev(300, 680, 12),
		ev(100, 660, 12), ev(200, 660, 12), ev(300, 660, 12),
	}
	cells, ok := detectTableGridFromEvents(events, Rect{X0: 0, Y0: 0, X1: 500, Y1: 800})
	if !ok {
		t.Fatal("expected ok=true with one missing cell")
	}
	// Should still produce 9 cells (3 rows × 3 cols) — the
	// empty cell gets a rect even though no event sat in it.
	if len(cells) != 9 {
		t.Errorf("got %d cells, want 9", len(cells))
	}
}

func TestDetectTableGridFromEvents_RejectsRowsWithTwoMissingCells(t *testing.T) {
	// 3x4 grid; row 1 misses cols 1 AND 2 (2 missing). Per
	// the heuristic, missing-count >= len(anchors) - 1 means
	// reject the region.
	events := []textEvent{
		ev(100, 700, 12), ev(200, 700, 12), ev(300, 700, 12), ev(400, 700, 12),
		ev(100, 680, 12) /*, missing 200,300*/, ev(400, 680, 12),
		ev(100, 660, 12), ev(200, 660, 12), ev(300, 660, 12), ev(400, 660, 12),
	}
	_, ok := detectTableGridFromEvents(events, Rect{X0: 0, Y0: 0, X1: 500, Y1: 800})
	if ok {
		t.Error("expected ok=false when a row is too sparse")
	}
}

func TestDetectTableGridFromEvents_FiltersOutOfRegionEvents(t *testing.T) {
	// A table sits at y=700/680. Two stray events outside the
	// region (header at y=750, footer at y=600) must NOT
	// participate.
	events := []textEvent{
		ev(100, 750, 12), // header — outside region
		ev(100, 700, 12), ev(200, 700, 12),
		ev(100, 680, 12), ev(200, 680, 12),
		ev(100, 600, 12), // footer — outside region
	}
	region := Rect{X0: 50, Y0: 670, X1: 300, Y1: 720}
	cells, ok := detectTableGridFromEvents(events, region)
	if !ok {
		t.Fatal("expected ok=true for in-region 2x2 grid")
	}
	if len(cells) != 4 {
		t.Errorf("got %d cells, want 4 (region filter applied)", len(cells))
	}
}

func TestDetectTableGridFromEvents_SkipsRotatedEvents(t *testing.T) {
	// One row of horizontal events; one row of 90°-rotated
	// events at the same Y. The rotated events MUST be
	// filtered out — they're not part of the horizontal
	// table.
	events := []textEvent{
		ev(100, 700, 12), ev(200, 700, 12),
	}
	rotated := ev(100, 680, 12)
	rotated.orient = 90
	events = append(events, rotated, textEvent{x: 200, y: 680, fontSize: 12, orient: 90})

	// Only 1 horizontal row → single-row region → reject.
	_, ok := detectTableGridFromEvents(events, Rect{X0: 0, Y0: 0, X1: 500, Y1: 800})
	if ok {
		t.Error("expected ok=false; the rotated row must be filtered out leaving only 1 horizontal row")
	}
}

func TestDetectTableGridFromEvents_EmitsExpectedCellGeometry(t *testing.T) {
	// 2 rows × 2 cols. Cell rects:
	//   col 0 spans [100, 200), col 1 spans [200, region.X1].
	// Row Y heights derive from fontSize.
	events := []textEvent{
		ev(100, 700, 12), ev(200, 700, 12),
		ev(100, 680, 12), ev(200, 680, 12),
	}
	region := Rect{X0: 80, Y0: 600, X1: 400, Y1: 720}
	cells, ok := detectTableGridFromEvents(events, region)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(cells) != 4 {
		t.Fatalf("got %d cells, want 4", len(cells))
	}
	// Pull each cell by (row, col).
	get := func(row, col int) Cell {
		for _, c := range cells {
			if c.Row == row && c.Col == col {
				return c
			}
		}
		t.Fatalf("no cell at (row %d, col %d)", row, col)
		return Cell{}
	}
	c00 := get(0, 0)
	if math.Abs(c00.Rect.X0-100) > 0.5 {
		t.Errorf("c00.Rect.X0 = %f, want ~100", c00.Rect.X0)
	}
	// Col 0 extends to col 1's anchor.
	if math.Abs(c00.Rect.X1-200) > 0.5 {
		t.Errorf("c00.Rect.X1 = %f, want ~200", c00.Rect.X1)
	}
	c01 := get(0, 1)
	// Last col extends to the region right edge.
	if math.Abs(c01.Rect.X1-400) > 0.5 {
		t.Errorf("c01.Rect.X1 = %f, want region right edge ~400", c01.Rect.X1)
	}
}

func TestDetectTableGridFromEvents_AcceptsRowsThatJitterWithinTolerance(t *testing.T) {
	// Real-world tables have ±0.5pt baseline jitter from
	// font rendering. The Y-tolerance must accept that.
	events := []textEvent{
		ev(100, 700.0, 12), ev(200, 700.3, 12), // same row
		ev(100, 680.1, 12), ev(200, 679.8, 12), // same row
	}
	_, ok := detectTableGridFromEvents(events, Rect{X0: 0, Y0: 0, X1: 500, Y1: 800})
	if !ok {
		t.Error("baseline jitter within tolerance should NOT prevent detection")
	}
}

func TestDetectTableGridFromEvents_NoEventsReturnsFalse(t *testing.T) {
	_, ok := detectTableGridFromEvents(nil, Rect{X0: 0, Y0: 0, X1: 500, Y1: 800})
	if ok {
		t.Error("empty events: expected ok=false")
	}
}

func TestDetectTableGridFromEvents_DegenerateRegionReturnsFalse(t *testing.T) {
	events := []textEvent{
		ev(100, 700, 12), ev(200, 700, 12),
		ev(100, 680, 12), ev(200, 680, 12),
	}
	// X1 <= X0
	_, ok := detectTableGridFromEvents(events, Rect{X0: 200, Y0: 0, X1: 100, Y1: 800})
	if ok {
		t.Error("expected ok=false for degenerate region")
	}
}

// ---- DetectTableGrid public entry point ----

func TestDetectTableGrid_ParsesStreamAndDetectsGrid(t *testing.T) {
	// Stream renders a 2x2 grid using direct Tj at fixed
	// positions. Tests the integration with extractTextEvents.
	stream := []byte(
		"BT /F1 12 Tf 100 700 Td (A) Tj ET\n" +
			"BT /F1 12 Tf 200 700 Td (B) Tj ET\n" +
			"BT /F1 12 Tf 100 680 Td (C) Tj ET\n" +
			"BT /F1 12 Tf 200 680 Td (D) Tj ET\n",
	)
	cells, ok := DetectTableGrid(stream, nil, Rect{X0: 50, Y0: 600, X1: 400, Y1: 750})
	if !ok {
		t.Fatal("expected ok=true for 2x2 grid in real content stream")
	}
	if len(cells) != 4 {
		t.Errorf("got %d cells, want 4", len(cells))
	}
}

func TestDetectTableGrid_ReturnsFalseForUnsupportedTextMatrix(t *testing.T) {
	// A skewed Tm makes extractTextEvents return
	// supported=false; the public entry point must surface
	// that as ok=false rather than panicking or returning a
	// half-detection.
	stream := []byte("BT /F1 12 Tf 1.5 0.3 0.2 1.5 100 700 Tm (X) Tj ET\n")
	_, ok := DetectTableGrid(stream, nil, Rect{X0: 0, Y0: 0, X1: 500, Y1: 800})
	if ok {
		t.Error("expected ok=false for unsupported text matrix")
	}
}
