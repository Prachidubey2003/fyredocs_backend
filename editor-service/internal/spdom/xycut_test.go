package spdom

import (
	"strings"
	"testing"
)

// US Letter is the canonical fixture page for all XY-cut tests —
// the absolute width keeps heuristic thresholds in the same
// regime as real-world documents.
var xycutLetter = Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}

// xyEvent is shorthand for building a textEvent at (x, y) with
// "F1" font @ 12pt. Tests pass enough text to give the event a
// believable bbox (approxWidth ≈ 6pt per glyph under the 0.5em
// fallback for the unknown F1 resource name).
func xyEvent(x, y float64, text string) textEvent {
	return textEvent{x: x, y: y, fontName: "F1", fontSize: 12, text: text}
}

func TestXYCut_SingleRegionWhenNoQualifyingGap(t *testing.T) {
	// A tightly-packed, narrow blob of events — no qualifying
	// strip on either axis. xyCut must emit one region.
	evs := []textEvent{
		xyEvent(72, 720, "line one with content"),
		xyEvent(72, 706, "line two with content"),
		xyEvent(72, 692, "line three"),
		xyEvent(72, 678, "line four"),
	}
	regions := xyCut(evs, 0)
	if len(regions) != 1 {
		t.Fatalf("len(regions) = %d, want 1 (no qualifying gap)", len(regions))
	}
	if len(regions[0]) != len(evs) {
		t.Errorf("region size = %d, want %d", len(regions[0]), len(evs))
	}
}

func TestXYCut_TwoColumnsProduceLeftThenRight(t *testing.T) {
	// Same shape as the 2-column test in columns_test.go, but
	// driven directly through xyCut so we can confirm reading
	// order is left → right.
	var evs []textEvent
	for i := 0; i < 5; i++ {
		y := 700.0 - float64(i)*16
		evs = append(evs, xyEvent(72, y, "left "+strings.Repeat("x", 20)))
		evs = append(evs, xyEvent(340, y, "right "+strings.Repeat("y", 20)))
	}
	regions := xyCut(evs, 0)
	if len(regions) != 2 {
		t.Fatalf("len(regions) = %d, want 2 (vertical cut)", len(regions))
	}
	for _, ev := range regions[0] {
		if ev.x >= 300 {
			t.Errorf("first region should be the LEFT column; got event at x=%v", ev.x)
		}
	}
	for _, ev := range regions[1] {
		if ev.x < 300 {
			t.Errorf("second region should be the RIGHT column; got event at x=%v", ev.x)
		}
	}
}

func TestXYCut_ThreeColumns(t *testing.T) {
	// Three columns at x=72, x=240, x=420. Each column has 5
	// lines, ~16pt apart. The recursive XY-cut should produce
	// three regions in left-to-right order.
	var evs []textEvent
	cols := []float64{72, 240, 420}
	for _, x := range cols {
		for i := 0; i < 5; i++ {
			y := 700.0 - float64(i)*16
			evs = append(evs, xyEvent(x, y, "col "+strings.Repeat("z", 18)))
		}
	}
	regions := xyCut(evs, 0)
	if len(regions) != 3 {
		t.Fatalf("len(regions) = %d, want 3 (three vertical cuts)", len(regions))
	}
	// Reading order: every event in regions[0] should be the
	// LEFT-most column, regions[1] the middle, regions[2] the
	// right-most. Within each region, X is roughly constant.
	mins := []float64{
		minX(regions[0]),
		minX(regions[1]),
		minX(regions[2]),
	}
	if !(mins[0] < mins[1] && mins[1] < mins[2]) {
		t.Errorf("regions should be ordered left-to-right; got minX = %v", mins)
	}
}

func TestXYCut_HeaderThenTwoColumnBody(t *testing.T) {
	// One wide header at y=750 (full width) + a 2-column body
	// at y=600..680. The widest qualifying gap on the FIRST cut
	// should be horizontal (the band of whitespace between
	// y=680 and y=750), then the lower half gets a vertical cut.
	var evs []textEvent
	// Header banner: 6 wide events at y=750.
	evs = append(evs, xyEvent(72, 750, "HEADER "+strings.Repeat("h", 60)))
	// Two-column body: 5 left-col lines + 5 right-col lines at
	// y=680..600.
	for i := 0; i < 5; i++ {
		y := 680.0 - float64(i)*16
		evs = append(evs, xyEvent(72, y, "left "+strings.Repeat("x", 22)))
		evs = append(evs, xyEvent(340, y, "right "+strings.Repeat("y", 22)))
	}
	regions := xyCut(evs, 0)
	if len(regions) != 3 {
		t.Fatalf("len(regions) = %d, want 3 (header + left + right)", len(regions))
	}
	// First region must be the header (the highest-Y region).
	if !hasEventAtY(regions[0], 750) {
		t.Errorf("first region should contain the header (y=750); got region %v", summarise(regions[0]))
	}
	// Subsequent regions are left then right columns of the body.
	leftIdx, rightIdx := -1, -1
	for i, r := range regions[1:] {
		if hasEventNearX(r, 72) {
			leftIdx = i + 1
		}
		if hasEventNearX(r, 340) {
			rightIdx = i + 1
		}
	}
	if leftIdx == -1 || rightIdx == -1 {
		t.Fatalf("did not find both body columns: regions=%v", summariseAll(regions))
	}
	if leftIdx > rightIdx {
		t.Errorf("body left column (region %d) should come before right column (region %d)", leftIdx, rightIdx)
	}
}

func TestXYCut_RespectsMaxDepth(t *testing.T) {
	// Pathological input: an event at every other column, with
	// gaps that would individually qualify. Without a depth cap
	// the recursion would split N times. Cap is maxXYCutDepth.
	var evs []textEvent
	for i := 0; i < 20; i++ {
		evs = append(evs, xyEvent(72+float64(i)*30, 700, "x"))
	}
	regions := xyCut(evs, 0)
	if len(regions) > 1<<maxXYCutDepth {
		t.Errorf("regions=%d exceeds 2^maxXYCutDepth=%d", len(regions), 1<<maxXYCutDepth)
	}
	// Total events across all regions must still equal len(evs).
	total := 0
	for _, r := range regions {
		total += len(r)
	}
	if total != len(evs) {
		t.Errorf("event count lost during partition: %d → %d", len(evs), total)
	}
}

func TestXYCut_TinyInputReturnsOneRegion(t *testing.T) {
	// minEventsPerRegion guard: below the threshold, xyCut
	// shouldn't try to cut. Caller (BuildBlocksColumnAware)
	// falls through to single-pass build.
	evs := []textEvent{
		xyEvent(72, 700, "a"),
		xyEvent(340, 700, "b"),
	}
	regions := xyCut(evs, 0)
	if len(regions) != 1 {
		t.Errorf("len(regions) = %d, want 1 for tiny input", len(regions))
	}
}

func TestBuildBlocksColumnAware_ThreeColumnOrdering(t *testing.T) {
	// Black-box test on the public API. Three columns of events,
	// all the way through BuildBlocksColumnAware. Expect three
	// non-merged columns in reading order.
	var evs []textEvent
	cols := []float64{72, 240, 420}
	for ci, x := range cols {
		marker := []string{"left", "mid", "right"}[ci]
		for i := 0; i < 5; i++ {
			y := 700.0 - float64(i)*14
			evs = append(evs, xyEvent(x, y, marker+" "+strings.Repeat("z", 18)))
		}
	}
	blocks := BuildBlocksColumnAware("p", xycutLetter, evs)
	if len(blocks) < 3 {
		t.Fatalf("len(blocks) = %d, want at least 3", len(blocks))
	}
	// Check that each marker shows up only after the previous
	// one — i.e., column ordering survives clustering.
	sawMid, sawRight := false, false
	for i, b := range blocks {
		text := flattenBlockText(b)
		isLeft := strings.Contains(text, "left ")
		isMid := strings.Contains(text, "mid ")
		isRight := strings.Contains(text, "right ")
		if isLeft && (sawMid || sawRight) {
			t.Errorf("block[%d] is 'left' but mid/right already seen — reading order broken", i)
		}
		if isMid {
			if sawRight {
				t.Errorf("block[%d] is 'mid' but right already seen", i)
			}
			sawMid = true
		}
		if isRight {
			sawRight = true
		}
	}
}

// --- helpers ---------------------------------------------------------------

func minX(evs []textEvent) float64 {
	m := evs[0].x
	for _, ev := range evs[1:] {
		if ev.x < m {
			m = ev.x
		}
	}
	return m
}

func hasEventAtY(evs []textEvent, y float64) bool {
	for _, ev := range evs {
		if ev.y == y {
			return true
		}
	}
	return false
}

func hasEventNearX(evs []textEvent, x float64) bool {
	const tol = 5
	for _, ev := range evs {
		if ev.x >= x-tol && ev.x <= x+tol {
			return true
		}
	}
	return false
}

func summarise(evs []textEvent) string {
	if len(evs) == 0 {
		return "<empty>"
	}
	var sb strings.Builder
	for i, ev := range evs {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(ev.text)
	}
	return sb.String()
}

func summariseAll(regions [][]textEvent) []string {
	out := make([]string, len(regions))
	for i, r := range regions {
		out[i] = summarise(r)
	}
	return out
}

func flattenBlockText(b *Block) string {
	var sb strings.Builder
	for _, ln := range b.Lines {
		for _, r := range ln.Runs {
			sb.WriteString(r.Text)
			sb.WriteByte(' ')
		}
	}
	return sb.String()
}
