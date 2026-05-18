package invoice

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Why this file exists.
//
// The invoice PDF renderer ships with WinAnsi-encoded Helvetica
// (see render_pdf.go + tounicode.go). That covers ASCII +
// Latin-1 + the curly-quote / em-dash / euro family — fine for
// the vast majority of subscription invoices. Glyphs OUTSIDE
// WinAnsi (CJK ideographs, emoji, custom-logo characters) don't
// have a code-point in WinAnsi at all and render as
// glyph-not-found in strict readers.
//
// The PDF answer for arbitrary Unicode is a **composite font**:
// a Type0 font that wraps a CIDFontType2 descendant whose
// glyphs come from an embedded TrueType file. This file
// implements every PDF-side object that wire-format requires.
//
// What this file does NOT do:
//   - Parse a TTF. The TrueType binary format has a few dozen
//     mandatory tables (cmap, glyf, loca, hhea, hmtx, head,
//     maxp, name, OS/2, post, …) and a real parser is ~1000
//     lines + a real test corpus. golang.org/x/image/font/sfnt
//     is the canonical Go implementation; we deliberately
//     DON'T pull it in here so the invoice library keeps its
//     "pure stdlib synthesis, no external library" stance
//     documented at the top of render_pdf.go. A follow-up
//     cycle wires that dep in.
//   - Subset the font. The current `EmitObjects` writes the
//     full font bytes the caller supplies — pass an already-
//     subsetted byte slice if you care about output size.
//   - Choose a font. The caller owns the source-of-truth
//     TrueType file. Bundling a default ships in a separate
//     cycle (probably DejaVu Sans / Noto Sans for Latin +
//     Cyrillic + Greek, or a CJK-specific font for invoices
//     billed in Asian markets).
//
// What this file DOES do:
//   - Owns the public API the caller plugs into when the
//     parsed-metrics + raw-bytes pair becomes available.
//   - Emits the four PDF objects (Type0 font, CIDFontType2,
//     FontDescriptor, FontFile2 stream) in the correct wire
//     shape per PDF 1.7 §9.7.
//   - Generates the CID→Unicode ToUnicode CMap from the
//     supplied `CmapRuneToGid` map.
//   - Validates inputs so a misconfigured caller fails at
//     NewTTFFont, not at PDF render time.

// TTFFontMetrics is the caller-supplied bundle of TrueType
// metadata the PDF emit code needs. A future TTF parser
// populates this struct from the parsed `head`, `hhea`, `OS/2`,
// `cmap`, and `hmtx` tables.
//
// All "1000-em" fields are expressed in glyph-coordinate units
// scaled so that 1000 units == 1 em (PDF convention). The
// `UnitsPerEm` field is what the TTF's `head` table reports;
// it's how the caller's parser converted glyph-space deltas
// to the PDF 1000-em frame. Carrying it lets the emit code
// re-scale glyph widths if `Widths` is in raw glyph units
// rather than 1000-em.
type TTFFontMetrics struct {
	// UnitsPerEm from the TTF `head` table. Almost always 1000
	// (Adobe) or 2048 (Microsoft); some old fonts use 512 or
	// 1024. Required for the /FontMatrix in the CIDFontType2
	// dict.
	UnitsPerEm uint16

	// FontBBox in 1000-em units: [llx, lly, urx, ury]. Used
	// for the FontDescriptor /FontBBox entry. From the TTF
	// `head` table's xMin/yMin/xMax/yMax.
	FontBBox [4]float64

	// Ascent / Descent / CapHeight in 1000-em units.
	// Required by FontDescriptor. From OS/2.sTypoAscender
	// (or hhea.ascent as fallback), OS/2.sTypoDescender, and
	// OS/2.sCapHeight.
	Ascent    float64
	Descent   float64
	CapHeight float64

	// ItalicAngle (degrees, 0 for upright). From post.italicAngle.
	ItalicAngle float64

	// StemV in 1000-em units. TTFs don't carry an explicit
	// StemV; pick a sensible default (≈80 for thin, 110 for
	// regular, 180 for bold).
	StemV float64

	// Flags is the FontDescriptor /Flags bitmask per PDF 1.7
	// §9.8.2. Typical values:
	//   FixedPitch = 1 << 0
	//   Serif      = 1 << 1
	//   Symbolic   = 1 << 2
	//   Script     = 1 << 3
	//   Nonsymbolic= 1 << 5
	//   Italic     = 1 << 6
	//   AllCap     = 1 << 16
	//   SmallCap   = 1 << 17
	//   ForceBold  = 1 << 18
	// For Latin/CJK body text use Nonsymbolic (1<<5). The
	// emitter validates that EXACTLY ONE of Symbolic /
	// Nonsymbolic is set — readers reject the dict otherwise.
	Flags uint32

	// CmapRuneToGid maps Unicode codepoints to TTF glyph IDs.
	// From the TTF's `cmap` table (typically the platform 3,
	// encoding 1 unicode subtable). The full map is what the
	// emitter walks to write the ToUnicode CMap; supplying
	// only the glyphs the document actually uses (a subset
	// cmap) keeps the CMap stream compact.
	CmapRuneToGid map[rune]uint16

	// Widths maps GID → glyph advance width in 1000-em units.
	// From hmtx.advanceWidth[gid]. The emitter writes the /W
	// array as a sparse `[ cid [w w w w] ]` shape, which is
	// the most compact PDF-1.7 form for the typical case where
	// many consecutive CIDs have distinct widths.
	Widths map[uint16]uint16

	// DefaultWidth is the /DW entry — the width used for CIDs
	// not present in /W. Pick the most common width from the
	// font (often the .notdef glyph's width) or use 500 as a
	// safe default.
	DefaultWidth uint16
}

// TTFFont is the runtime handle for an embedded TrueType font.
// Constructed via NewTTFFont; passed to RenderPDF in a future
// cycle once that wiring lands. For now the type is consumed
// only by EmitObjects + AdvanceWidth.
type TTFFont struct {
	// ResourceName is the /F<N> name the content stream uses
	// to reference this font. The renderer assigns it; the
	// caller doesn't pick it directly.
	ResourceName string

	// PostScriptName is the canonical /BaseFont entry — the
	// font's name from the TTF `name` table (e.g.
	// "NotoSansCJKsc-Regular"). PDF readers display this in
	// "View → Document Properties → Fonts".
	PostScriptName string

	// Data is the raw TrueType byte slice that lands in the
	// /FontFile2 stream. May be a subset.
	Data []byte

	// Metrics carries the parsed TTF table data the emitter
	// needs.
	Metrics TTFFontMetrics
}

// ErrEmptyFontData is returned when NewTTFFont is called with
// no bytes — likely a parsing bug at the caller.
var ErrEmptyFontData = errors.New("invoice: TTFFont requires non-empty font data")

// ErrEmptyPostScriptName is returned when the caller forgets to
// pass the font's PostScript name. The PDF dictionary requires
// it as /BaseFont; an empty value makes the resulting PDF
// non-conforming.
var ErrEmptyPostScriptName = errors.New("invoice: TTFFont requires a non-empty PostScript name")

// ErrInvalidFlags is returned when Metrics.Flags doesn't have
// exactly one of Symbolic / Nonsymbolic set. PDF readers reject
// the FontDescriptor otherwise.
var ErrInvalidFlags = errors.New(
	"invoice: TTFFont metrics must set exactly one of Symbolic (1<<2) or Nonsymbolic (1<<5)",
)

// ErrEmptyCmap is returned when Metrics.CmapRuneToGid is nil or
// empty. Without any rune→GID mappings, the ToUnicode CMap
// would be empty and no glyph would round-trip through copy/
// paste.
var ErrEmptyCmap = errors.New("invoice: TTFFont metrics require a non-empty CmapRuneToGid")

// NewTTFFont validates inputs and returns a font handle. Call
// once per `(font, subset)` pair the invoice references.
func NewTTFFont(psName string, data []byte, m TTFFontMetrics) (*TTFFont, error) {
	if strings.TrimSpace(psName) == "" {
		return nil, ErrEmptyPostScriptName
	}
	if len(data) == 0 {
		return nil, ErrEmptyFontData
	}
	const symbolic = uint32(1 << 2)
	const nonsymbolic = uint32(1 << 5)
	symBit := m.Flags&symbolic != 0
	nonSymBit := m.Flags&nonsymbolic != 0
	if symBit == nonSymBit {
		return nil, ErrInvalidFlags
	}
	if len(m.CmapRuneToGid) == 0 {
		return nil, ErrEmptyCmap
	}
	if m.UnitsPerEm == 0 {
		// Adobe convention is 1000; missing means a parser
		// bug. Fall back to the same to keep emit math sane.
		m.UnitsPerEm = 1000
	}
	if m.DefaultWidth == 0 {
		m.DefaultWidth = 500
	}
	return &TTFFont{
		PostScriptName: psName,
		Data:           data,
		Metrics:        m,
	}, nil
}

// EmitObjects writes the four PDF objects that compose this
// embedded font:
//
//	objN+0 — Type0 (composite) font dict
//	objN+1 — CIDFontType2 descendant font
//	objN+2 — FontDescriptor
//	objN+3 — FontFile2 stream (the raw TTF bytes)
//
// The objects reference each other via indirect refs derived
// from `startObjNum`. The caller is responsible for
// concatenating the returned slice into its own xref table.
//
// Independently of EmitObjects, the caller MUST also emit a
// /ToUnicode CMap stream (one object) and reference it from
// the Type0 dict. Use ToUnicodeCMap(t.Metrics.CmapRuneToGid)
// + assign that object's number, then pass it via
// `toUnicodeRef`.
func (t *TTFFont) EmitObjects(startObjNum int, toUnicodeRef int) []string {
	type0 := startObjNum
	cidFont := startObjNum + 1
	descriptor := startObjNum + 2
	fontFile := startObjNum + 3

	objects := make([]string, 0, 4)

	// 1) Type0 (composite) font dict. /Encoding is the
	// well-known predefined CMap `Identity-H` — maps Unicode
	// codepoints directly to CIDs via the descendant font's
	// /CIDToGIDMap. /BaseFont must match the descendant's
	// /BaseFont per PDF 1.7 §9.7.4.2.
	objects = append(objects, fmt.Sprintf(
		"%d 0 obj\n<< /Type /Font /Subtype /Type0 /BaseFont /%s "+
			"/Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>\nendobj\n",
		type0, t.PostScriptName, cidFont, toUnicodeRef,
	))

	// 2) CIDFontType2 descendant — wraps the TTF as a CID-
	// keyed font where each CID == GID directly.
	// /CIDToGIDMap /Identity means CID N renders glyph N
	// (the most compact mapping for a font where we control
	// the GID space).
	widths := encodeWidthsArray(t.Metrics.Widths)
	objects = append(objects, fmt.Sprintf(
		"%d 0 obj\n<< /Type /Font /Subtype /CIDFontType2 /BaseFont /%s "+
			"/CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> "+
			"/FontDescriptor %d 0 R /CIDToGIDMap /Identity /DW %d /W %s >>\nendobj\n",
		cidFont, t.PostScriptName, descriptor, t.Metrics.DefaultWidth, widths,
	))

	// 3) FontDescriptor — physical font metadata. Per PDF
	// 1.7 §9.8.1, the dict MUST contain Flags / FontBBox /
	// ItalicAngle / Ascent / Descent / CapHeight / StemV.
	// /FontFile2 references the TTF stream below.
	bbox := t.Metrics.FontBBox
	objects = append(objects, fmt.Sprintf(
		"%d 0 obj\n<< /Type /FontDescriptor /FontName /%s /Flags %d "+
			"/FontBBox [%s %s %s %s] /ItalicAngle %s "+
			"/Ascent %s /Descent %s /CapHeight %s /StemV %s "+
			"/FontFile2 %d 0 R >>\nendobj\n",
		descriptor, t.PostScriptName, t.Metrics.Flags,
		formatNum(bbox[0]), formatNum(bbox[1]), formatNum(bbox[2]), formatNum(bbox[3]),
		formatNum(t.Metrics.ItalicAngle),
		formatNum(t.Metrics.Ascent), formatNum(t.Metrics.Descent),
		formatNum(t.Metrics.CapHeight), formatNum(t.Metrics.StemV),
		fontFile,
	))

	// 4) FontFile2 — the raw TTF bytes. /Length1 is the
	// uncompressed length of the embedded font program,
	// REQUIRED for TrueType per PDF 1.7 §9.9 (PDF readers
	// use it to find the end of the font program without
	// scanning the stream).
	//
	// We write the bytes verbatim — no FlateDecode filter.
	// The invoice renderer's `assemblePDF` doesn't use
	// streams with filters and pdfcpu's round-trip check
	// would reject anything that lied about /Length1.
	objects = append(objects, fmt.Sprintf(
		"%d 0 obj\n<< /Length %d /Length1 %d >>\nstream\n%s\nendstream\nendobj\n",
		fontFile, len(t.Data), len(t.Data), string(t.Data),
	))

	return objects
}

// AdvanceWidth returns the horizontal advance for `r` at
// `sizePt` points. Returns 0 for runes the cmap doesn't
// cover — caller should fall back to DefaultWidth in that
// case (or render the .notdef tofu, which the PDF reader
// does automatically).
func (t *TTFFont) AdvanceWidth(r rune, sizePt float64) float64 {
	gid, ok := t.Metrics.CmapRuneToGid[r]
	if !ok {
		return 0
	}
	w, ok := t.Metrics.Widths[gid]
	if !ok {
		w = t.Metrics.DefaultWidth
	}
	// Convert 1000-em units to points: w * sizePt / UnitsPerEm.
	// Most TTFs report widths in 1000-em already (matching PDF
	// convention) — keep UnitsPerEm in the formula so a font
	// with 2048-em metrics still renders at the right size.
	return float64(w) * sizePt / float64(t.Metrics.UnitsPerEm)
}

// ToUnicodeCMap returns the bytes of a /ToUnicode CMap stream
// that maps every GID present in `cmap` back to its source
// Unicode codepoint. Pass it through to the caller's
// /ToUnicode object body.
//
// CMap shape mirrors the WinAnsi flavour in tounicode.go but
// with a 4-byte (16-bit hex) codespace because Identity-H maps
// 16-bit CIDs.
func ToUnicodeCMap(cmap map[rune]uint16) string {
	type pair struct {
		gid uint16
		r   rune
	}
	pairs := make([]pair, 0, len(cmap))
	for r, gid := range cmap {
		pairs = append(pairs, pair{gid, r})
	}
	// Deterministic order — both for test stability and so
	// pdf-diff tooling produces clean diffs when the same
	// font is re-embedded.
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].gid < pairs[j].gid })

	var entries strings.Builder
	for _, p := range pairs {
		fmt.Fprintf(&entries, "<%04X> <%04X>\n", p.gid, p.r)
	}

	chunks := splitCMapEntries(entries.String(), 100)

	var b strings.Builder
	b.Grow(1024 + len(entries.String()))
	b.WriteString("/CIDInit /ProcSet findresource begin\n")
	b.WriteString("12 dict begin\nbegincmap\n")
	b.WriteString("/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n")
	b.WriteString("/CMapName /Adobe-Identity-UCS def\n")
	b.WriteString("/CMapType 2 def\n")
	// 2-byte codespace for the Identity-H Type0 encoding.
	b.WriteString("1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n")
	for _, chunk := range chunks {
		n := strings.Count(chunk, "\n")
		fmt.Fprintf(&b, "%d beginbfchar\n", n)
		b.WriteString(chunk)
		b.WriteString("endbfchar\n")
	}
	b.WriteString("endcmap\n")
	b.WriteString("CMapName currentdict /CMap defineresource pop\n")
	b.WriteString("end\nend\n")
	return b.String()
}

// encodeWidthsArray renders the /W array per PDF 1.7 §9.7.4.3
// format-1: `[ cid [w w w ...] cid [w w] ... ]`. We emit one
// `cid [w]` per glyph for simplicity; future optimisation can
// fold runs of equal widths into the range form `[ cidFirst
// cidLast w ]` for compactness.
//
// Sorted by CID so output bytes are deterministic.
func encodeWidthsArray(widths map[uint16]uint16) string {
	if len(widths) == 0 {
		return "[]"
	}
	cids := make([]uint16, 0, len(widths))
	for c := range widths {
		cids = append(cids, c)
	}
	sort.Slice(cids, func(i, j int) bool { return cids[i] < cids[j] })

	var b strings.Builder
	b.Grow(len(cids) * 12)
	b.WriteByte('[')
	for i, cid := range cids {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d [%d]", cid, widths[cid])
	}
	b.WriteByte(']')
	return b.String()
}

// formatNum trims trailing zeros from float-formatted numbers
// for compactness (PDF parsers are lenient about float vs int
// in dict entries, but emitting `718` is cleaner than `718.0`).
func formatNum(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
}
