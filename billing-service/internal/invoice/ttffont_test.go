package invoice

import (
	"errors"
	"strings"
	"testing"
)

// sampleMetrics returns a realistic-but-tiny TTFFontMetrics
// the tests share. Values borrowed from DejaVu Sans for
// plausibility — they don't need to match a real font byte-
// for-byte since this layer is the PDF-emit wrapper, not a
// TTF parser.
func sampleMetrics() TTFFontMetrics {
	return TTFFontMetrics{
		UnitsPerEm:    1000,
		FontBBox:      [4]float64{-50, -200, 1100, 900},
		Ascent:        780,
		Descent:       -220,
		CapHeight:     708,
		ItalicAngle:   0,
		StemV:         110,
		Flags:         1 << 5, // Nonsymbolic (PDF body text)
		DefaultWidth:  500,
		CmapRuneToGid: map[rune]uint16{'H': 43, 'i': 76, 'A': 36},
		Widths:        map[uint16]uint16{43: 750, 76: 280, 36: 684},
	}
}

func TestNewTTFFont_RejectsEmptyPostScriptName(t *testing.T) {
	_, err := NewTTFFont("", []byte("ttf-bytes"), sampleMetrics())
	if !errors.Is(err, ErrEmptyPostScriptName) {
		t.Errorf("got %v, want ErrEmptyPostScriptName", err)
	}
	_, err = NewTTFFont("   ", []byte("ttf-bytes"), sampleMetrics())
	if !errors.Is(err, ErrEmptyPostScriptName) {
		t.Errorf("whitespace-only name: got %v, want ErrEmptyPostScriptName", err)
	}
}

func TestNewTTFFont_RejectsEmptyData(t *testing.T) {
	_, err := NewTTFFont("DejaVuSans", nil, sampleMetrics())
	if !errors.Is(err, ErrEmptyFontData) {
		t.Errorf("got %v, want ErrEmptyFontData", err)
	}
	_, err = NewTTFFont("DejaVuSans", []byte{}, sampleMetrics())
	if !errors.Is(err, ErrEmptyFontData) {
		t.Errorf("zero-length slice: got %v, want ErrEmptyFontData", err)
	}
}

func TestNewTTFFont_RejectsNeitherSymbolicNorNonsymbolic(t *testing.T) {
	// PDF 1.7 §9.8.2: the Flags bitmask MUST set exactly one
	// of Symbolic (1<<2) or Nonsymbolic (1<<5). Setting
	// neither leaves the reader guessing which encoding to
	// apply — readers reject the FontDescriptor.
	m := sampleMetrics()
	m.Flags = 0
	_, err := NewTTFFont("X", []byte("y"), m)
	if !errors.Is(err, ErrInvalidFlags) {
		t.Errorf("flags=0: got %v, want ErrInvalidFlags", err)
	}
}

func TestNewTTFFont_RejectsBothSymbolicAndNonsymbolic(t *testing.T) {
	m := sampleMetrics()
	m.Flags = (1 << 2) | (1 << 5)
	_, err := NewTTFFont("X", []byte("y"), m)
	if !errors.Is(err, ErrInvalidFlags) {
		t.Errorf("flags=both: got %v, want ErrInvalidFlags", err)
	}
}

func TestNewTTFFont_RejectsEmptyCmap(t *testing.T) {
	m := sampleMetrics()
	m.CmapRuneToGid = map[rune]uint16{}
	_, err := NewTTFFont("X", []byte("y"), m)
	if !errors.Is(err, ErrEmptyCmap) {
		t.Errorf("got %v, want ErrEmptyCmap", err)
	}
}

func TestNewTTFFont_AppliesDefaultsForUnitsPerEmAndDefaultWidth(t *testing.T) {
	// Real TTF parsers always set UnitsPerEm > 0; a 0 here
	// indicates a parser bug. The constructor falls back to
	// 1000 so emit math stays sane. Same logic for /DW:
	// missing → 500 (the most common width).
	m := sampleMetrics()
	m.UnitsPerEm = 0
	m.DefaultWidth = 0
	f, err := NewTTFFont("X", []byte("y"), m)
	if err != nil {
		t.Fatalf("NewTTFFont: %v", err)
	}
	if f.Metrics.UnitsPerEm != 1000 {
		t.Errorf("UnitsPerEm fallback = %d, want 1000", f.Metrics.UnitsPerEm)
	}
	if f.Metrics.DefaultWidth != 500 {
		t.Errorf("DefaultWidth fallback = %d, want 500", f.Metrics.DefaultWidth)
	}
}

func TestEmitObjects_HasFourCorrectlyLinkedObjects(t *testing.T) {
	f, err := NewTTFFont("DejaVuSans", []byte("ttf-bytes-placeholder"), sampleMetrics())
	if err != nil {
		t.Fatalf("NewTTFFont: %v", err)
	}
	const startObj = 10
	const toUnicodeObj = 99
	objects := f.EmitObjects(startObj, toUnicodeObj)
	if len(objects) != 4 {
		t.Fatalf("expected 4 objects (Type0, CIDFontType2, FontDescriptor, FontFile2); got %d", len(objects))
	}

	// Obj 10: Type0 wrapping CIDFontType2 (obj 11) + ToUnicode (obj 99).
	type0 := objects[0]
	for _, want := range []string{
		"10 0 obj",
		"/Type /Font",
		"/Subtype /Type0",
		"/BaseFont /DejaVuSans",
		"/Encoding /Identity-H",
		"/DescendantFonts [11 0 R]",
		"/ToUnicode 99 0 R",
	} {
		if !strings.Contains(type0, want) {
			t.Errorf("Type0 obj missing %q\nfull: %s", want, type0)
		}
	}

	// Obj 11: CIDFontType2 pointing at FontDescriptor (obj 12).
	cidFont := objects[1]
	for _, want := range []string{
		"11 0 obj",
		"/Subtype /CIDFontType2",
		"/BaseFont /DejaVuSans",
		"/CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >>",
		"/FontDescriptor 12 0 R",
		"/CIDToGIDMap /Identity",
		"/DW 500",
	} {
		if !strings.Contains(cidFont, want) {
			t.Errorf("CIDFontType2 obj missing %q\nfull: %s", want, cidFont)
		}
	}

	// Obj 12: FontDescriptor pointing at FontFile2 (obj 13).
	desc := objects[2]
	for _, want := range []string{
		"12 0 obj",
		"/Type /FontDescriptor",
		"/FontName /DejaVuSans",
		"/Flags 32", // 1 << 5 = Nonsymbolic
		"/FontBBox [-50 -200 1100 900]",
		"/ItalicAngle 0",
		"/Ascent 780",
		"/Descent -220",
		"/CapHeight 708",
		"/StemV 110",
		"/FontFile2 13 0 R",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("FontDescriptor obj missing %q\nfull: %s", want, desc)
		}
	}

	// Obj 13: FontFile2 with /Length + /Length1 BOTH equal to
	// the raw byte length. /Length1 is mandatory for TTFs per
	// PDF 1.7 §9.9 — readers use it to find the end of the
	// font program inside the stream.
	fontFile := objects[3]
	for _, want := range []string{
		"13 0 obj",
		"/Length 21",  // len("ttf-bytes-placeholder")
		"/Length1 21",
		"stream\nttf-bytes-placeholder\nendstream",
	} {
		if !strings.Contains(fontFile, want) {
			t.Errorf("FontFile2 obj missing %q\nfull: %s", want, fontFile)
		}
	}
}

func TestEmitObjects_WidthsArrayIsDeterministicByCID(t *testing.T) {
	// Different runtime hash-seed orderings of Metrics.Widths
	// must produce byte-identical /W output, so callers can
	// hash the PDF bytes for cache keys.
	m := sampleMetrics()
	m.Widths = map[uint16]uint16{43: 750, 76: 280, 36: 684, 12: 900}
	f, _ := NewTTFFont("X", []byte("y"), m)
	objects := f.EmitObjects(1, 99)
	cidFont := objects[1]
	// Expected /W array in ascending-CID order: 12, 36, 43, 76.
	expected := "/W [12 [900] 36 [684] 43 [750] 76 [280]]"
	if !strings.Contains(cidFont, expected) {
		t.Errorf("widths array not in CID-ascending order\nwant: %s\nfull: %s", expected, cidFont)
	}
}

func TestEmitObjects_EmptyWidthsRendersEmptyArray(t *testing.T) {
	// Defensive case — a caller whose subset has no widths
	// (every glyph uses /DW). The /W entry must still be
	// well-formed JSON-style array, not omitted.
	m := sampleMetrics()
	m.Widths = nil
	f, _ := NewTTFFont("X", []byte("y"), m)
	objects := f.EmitObjects(1, 99)
	if !strings.Contains(objects[1], "/W []") {
		t.Errorf("expected `/W []` for empty widths map; got %s", objects[1])
	}
}

func TestAdvanceWidth_UsesCmapAndWidthsAt1000Em(t *testing.T) {
	f, _ := NewTTFFont("X", []byte("y"), sampleMetrics())
	// 'H' → gid 43 → width 750 in 1000-em frame.
	// At 12pt: 750 * 12 / 1000 = 9pt.
	got := f.AdvanceWidth('H', 12)
	if got != 9 {
		t.Errorf("AdvanceWidth('H', 12) = %v, want 9", got)
	}
	// 'i' → gid 76 → width 280. 280 * 12 / 1000 = 3.36.
	got = f.AdvanceWidth('i', 12)
	if got != 3.36 {
		t.Errorf("AdvanceWidth('i', 12) = %v, want 3.36", got)
	}
}

func TestAdvanceWidth_ScalesByUnitsPerEm(t *testing.T) {
	// A 2048-em font (Microsoft TrueType convention) with the
	// same numerical width values must produce ~half the
	// advance of a 1000-em font.
	m := sampleMetrics()
	m.UnitsPerEm = 2048
	f, _ := NewTTFFont("X", []byte("y"), m)
	// 750 * 12 / 2048 ≈ 4.3945.
	got := f.AdvanceWidth('H', 12)
	if got < 4.39 || got > 4.40 {
		t.Errorf("AdvanceWidth('H', 12) at 2048em = %v, want ≈4.395", got)
	}
}

func TestAdvanceWidth_ReturnsZeroForUnknownRune(t *testing.T) {
	// CJK glyph against a Latin-only cmap — the reader will
	// substitute .notdef at render time, but the metric math
	// shouldn't lie about the advance.
	f, _ := NewTTFFont("X", []byte("y"), sampleMetrics())
	if got := f.AdvanceWidth('漢', 12); got != 0 {
		t.Errorf("unknown rune: got %v, want 0", got)
	}
}

func TestToUnicodeCMap_HasIdentityHCodespaceAndBfchar(t *testing.T) {
	cmap := map[rune]uint16{'A': 36, 'B': 37, 'C': 38}
	out := ToUnicodeCMap(cmap)
	for _, want := range []string{
		"/CIDInit /ProcSet findresource begin",
		"begincmap",
		"/Registry (Adobe) /Ordering (UCS) /Supplement 0",
		"/CMapName /Adobe-Identity-UCS def",
		"/CMapType 2 def",
		// 2-byte codespace covering all CIDs — critical for
		// Identity-H since glyph codes are 16-bit.
		"1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange",
		"beginbfchar",
		"<0024> <0041>", // gid 36 → 'A'
		"<0025> <0042>", // gid 37 → 'B'
		"<0026> <0043>", // gid 38 → 'C'
		"endbfchar",
		"endcmap",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("CMap missing %q", want)
		}
	}
}

func TestToUnicodeCMap_SortsByGIDForDeterministicOutput(t *testing.T) {
	// Two callers with the same rune→gid map but different
	// insertion orders MUST get byte-identical CMap output.
	// Pin via emit-order check.
	cmap1 := map[rune]uint16{'A': 36, 'Z': 90, 'M': 50}
	cmap2 := map[rune]uint16{'Z': 90, 'M': 50, 'A': 36}
	if ToUnicodeCMap(cmap1) != ToUnicodeCMap(cmap2) {
		t.Error("CMap output depends on map iteration order — must sort by gid")
	}
	out := ToUnicodeCMap(cmap1)
	idxA := strings.Index(out, "<0024>")
	idxM := strings.Index(out, "<0032>")
	idxZ := strings.Index(out, "<005A>")
	if idxA < 0 || idxM < 0 || idxZ < 0 {
		t.Fatalf("expected all three gids in output: %s", out)
	}
	if !(idxA < idxM && idxM < idxZ) {
		t.Errorf("entries not in ascending gid order: A@%d M@%d Z@%d", idxA, idxM, idxZ)
	}
}

func TestFormatNum_TrimsTrailingZeros(t *testing.T) {
	// Whole-number floats render as integers (cleaner PDF
	// output); fractional values keep up to 4 decimals with
	// trailing zeros stripped.
	cases := []struct {
		in   float64
		want string
	}{
		{718, "718"},
		{718.0, "718"},
		{-220, "-220"},
		{12.5, "12.5"},
		{0, "0"},
		{0.001, "0.001"},
	}
	for _, c := range cases {
		got := formatNum(c.in)
		if got != c.want {
			t.Errorf("formatNum(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
