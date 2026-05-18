package spdom

import (
	"strings"
	"testing"
)

// page = US Letter for every column test — the absolute width
// keeps the heuristic thresholds in the same regime as real
// documents.
var letter = Rect{X0: 0, Y0: 0, X1: 612, Y1: 792}

// makeEvent is a terse builder so column tests can spell out
// "this much text starting at x=N, baseline y=M" without
// stamping the same defaults at every call site.
func makeEvent(x, y, size float64, text string) textEvent {
	return textEvent{
		x:        x,
		y:        y,
		text:     text,
		fontName: "F1",
		fontSize: size,
	}
}

func TestDetectColumnGap_SingleColumnReturnsZero(t *testing.T) {
	// Three full-width lines stacked vertically. No vertical
	// gap inside the page — every column row is occupied.
	evs := []textEvent{
		makeEvent(72, 740, 12, strings.Repeat("a ", 100)),
		makeEvent(72, 720, 12, strings.Repeat("b ", 100)),
		makeEvent(72, 700, 12, strings.Repeat("c ", 100)),
		makeEvent(72, 680, 12, strings.Repeat("d ", 100)),
	}
	if gap := detectColumnGap(letter, evs); gap != 0 {
		t.Errorf("detectColumnGap(single-column) = %v, want 0", gap)
	}
}

func TestDetectColumnGap_TwoColumnsReturnCentralX(t *testing.T) {
	// Left column at x=72 width ~200; right column at x=340
	// width ~200. Gap centred near x=300, well inside the
	// central 60% of the page.
	const linesPerCol = 5
	evs := make([]textEvent, 0, linesPerCol*2)
	for i := 0; i < linesPerCol; i++ {
		y := 700.0 - float64(i)*16
		evs = append(evs, makeEvent(72, y, 12, "left column line "+strings.Repeat("x", 20)))
		evs = append(evs, makeEvent(340, y, 12, "right column line "+strings.Repeat("y", 20)))
	}
	gap := detectColumnGap(letter, evs)
	if gap == 0 {
		t.Fatalf("detectColumnGap = 0, expected a central column gap")
	}
	// Gap must lie strictly between the right edge of the left
	// column and the left edge of the right column.
	if gap < 200 || gap > 340 {
		t.Errorf("detectColumnGap = %v, want a value between 200 and 340", gap)
	}
}

func TestDetectColumnGap_RejectsMarginAsColumn(t *testing.T) {
	// All text is in the left half of the page — the right
	// half is a wide unmarked run but it's a MARGIN, not a
	// column gap. The heuristic's "central-60%" requirement
	// should reject it.
	evs := []textEvent{
		makeEvent(72, 700, 12, "left text "+strings.Repeat("a", 30)),
		makeEvent(72, 680, 12, "more left "+strings.Repeat("b", 30)),
		makeEvent(72, 660, 12, "still left "+strings.Repeat("c", 30)),
		makeEvent(72, 640, 12, "yet more left "+strings.Repeat("d", 30)),
	}
	if gap := detectColumnGap(letter, evs); gap != 0 {
		t.Errorf("detectColumnGap returned %v for a right-margin (not column) layout", gap)
	}
}

func TestDetectColumnGap_RejectsTooFewEvents(t *testing.T) {
	// A single line with a tab character produces a wide
	// internal gap — but with only one event the call should
	// bail out rather than mis-classify.
	evs := []textEvent{
		makeEvent(72, 700, 12, "tabbed"),
		makeEvent(400, 700, 12, "phrase"),
	}
	if gap := detectColumnGap(letter, evs); gap != 0 {
		t.Errorf("detectColumnGap with %d events = %v, want 0 (too few to call)", len(evs), gap)
	}
}

func TestSplitEventsByX_PartitionsByStartCoord(t *testing.T) {
	evs := []textEvent{
		makeEvent(72, 700, 12, "L"),
		makeEvent(340, 700, 12, "R"),
		makeEvent(80, 680, 12, "L"),
		makeEvent(350, 680, 12, "R"),
	}
	left, right := splitEventsByX(evs, 300)
	if len(left) != 2 || left[0].text != "L" || left[1].text != "L" {
		t.Errorf("left partition = %+v, want two L events", left)
	}
	if len(right) != 2 || right[0].text != "R" || right[1].text != "R" {
		t.Errorf("right partition = %+v, want two R events", right)
	}
}

func TestBuildBlocksColumnAware_TwoColumnReadingOrder(t *testing.T) {
	// Same y on both sides — under the single-pass clusterer
	// these baseline-matched runs would merge into one wide
	// line. Column-aware build keeps them separate AND emits
	// every left-column block before any right-column block
	// (reading order).
	//
	// Block count: depends on the line-spacing threshold inside
	// buildBlocksFromEvents (currently 1.5× font size), so we
	// assert column purity + ordering instead — those are the
	// invariants the column-aware build is responsible for.
	evs := []textEvent{
		makeEvent(72, 700, 12, "left line one "+strings.Repeat("x", 20)),
		makeEvent(340, 700, 12, "right line one "+strings.Repeat("y", 20)),
		makeEvent(72, 686, 12, "left line two "+strings.Repeat("x", 20)),
		makeEvent(340, 686, 12, "right line two "+strings.Repeat("y", 20)),
		makeEvent(72, 672, 12, "left line three "+strings.Repeat("x", 20)),
		makeEvent(340, 672, 12, "right line three "+strings.Repeat("y", 20)),
	}
	blocks := BuildBlocksColumnAware("p", letter, evs)
	if len(blocks) < 2 {
		t.Fatalf("got %d blocks, want at least 2 (left + right)", len(blocks))
	}

	collectText := func(b *Block) string {
		var sb strings.Builder
		for _, ln := range b.Lines {
			for _, r := range ln.Runs {
				sb.WriteString(r.Text)
				sb.WriteByte(' ')
			}
		}
		return sb.String()
	}

	// Every block must contain text from exactly ONE column
	// (no left+right merges) AND every left block must precede
	// every right block in the slice ordering.
	sawRight := false
	for i, b := range blocks {
		text := collectText(b)
		isLeft := strings.Contains(text, "left line")
		isRight := strings.Contains(text, "right line")
		if isLeft && isRight {
			t.Errorf("block[%d] mixes columns; text=%q", i, text)
		}
		if isRight {
			sawRight = true
			continue
		}
		if isLeft && sawRight {
			t.Errorf("block[%d] is left-column but a right-column block came first; reading-order broken", i)
		}
	}
}

func TestBuildBlocksColumnAware_PreservesSingleColumnBehaviour(t *testing.T) {
	// Single column input. The wrapper must produce a result
	// indistinguishable (up to ID prefix) from the direct
	// buildBlocksFromEvents call. If a regression elsewhere
	// makes the column detector trigger on a single column,
	// this test catches it.
	evs := []textEvent{
		makeEvent(72, 700, 12, "line one"),
		makeEvent(72, 680, 12, "line two"),
		makeEvent(72, 660, 12, "line three"),
		makeEvent(72, 640, 12, "line four"),
	}
	got := BuildBlocksColumnAware("p", letter, evs)
	want := buildBlocksFromEvents("p", letter, evs)
	if len(got) != len(want) {
		t.Errorf("column-aware build produced %d blocks, want %d (single-column should pass through)",
			len(got), len(want))
	}
}

func TestBuildBlocksColumnAware_RenumbersBlocksSequentially(t *testing.T) {
	// Block IDs must be {pageID}/block-1, {pageID}/block-2, …
	// across BOTH columns. Catches a bug where the renumberer
	// silently produces colliding "block-1" IDs.
	evs := []textEvent{
		makeEvent(72, 700, 12, "L line "+strings.Repeat("x", 20)),
		makeEvent(340, 700, 12, "R line "+strings.Repeat("y", 20)),
		makeEvent(72, 680, 12, "L line2 "+strings.Repeat("x", 20)),
		makeEvent(340, 680, 12, "R line2 "+strings.Repeat("y", 20)),
	}
	blocks := BuildBlocksColumnAware("p", letter, evs)
	seen := make(map[string]bool, len(blocks))
	for i, b := range blocks {
		if seen[b.ID] {
			t.Errorf("duplicate block ID %q at index %d", b.ID, i)
		}
		seen[b.ID] = true
		wantID := NodeID("p", "block", i+1)
		if b.ID != wantID {
			t.Errorf("block[%d].ID = %q, want %q", i, b.ID, wantID)
		}
	}
}

func TestLongestZeroRun_HandlesEdges(t *testing.T) {
	// All-false bitmap: the whole thing is one run.
	if s, l := longestZeroRun([]bool{false, false, false, false}); s != 0 || l != 4 {
		t.Errorf("longestZeroRun(all-false) = (%d, %d), want (0, 4)", s, l)
	}
	// All-true bitmap: no run.
	if _, l := longestZeroRun([]bool{true, true, true}); l != 0 {
		t.Errorf("longestZeroRun(all-true) length = %d, want 0", l)
	}
	// Trailing run of zeros — must be reported even though no
	// post-loop "true" terminates it.
	if s, l := longestZeroRun([]bool{true, false, false, false}); s != 1 || l != 3 {
		t.Errorf("longestZeroRun(trailing zeros) = (%d, %d), want (1, 3)", s, l)
	}
}
