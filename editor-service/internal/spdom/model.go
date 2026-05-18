package spdom

import (
	"fmt"

	"github.com/google/uuid"
)

// Rect is an axis-aligned bounding box in PDF user-space coordinates
// (origin at bottom-left, 1 unit = 1/72 inch).
type Rect struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// Width returns x1 - x0 in points.
func (r Rect) Width() float64 { return r.X1 - r.X0 }

// Height returns y1 - y0 in points.
func (r Rect) Height() float64 { return r.Y1 - r.Y0 }

// Document is the top of the sPDOM tree (plan §5.2 L4).
type Document struct {
	ID         string  `json:"id"`
	PDFVersion string  `json:"pdfVersion,omitempty"`
	Title      string  `json:"title,omitempty"`
	Author     string  `json:"author,omitempty"`
	Producer   string  `json:"producer,omitempty"`
	PageCount  int     `json:"pageCount"`
	Pages      []*Page `json:"pages"`
}

// LayoutPass marks which sPDOM layers have populated this Page.
//
//	0 = geometry only (L1) — `Blocks` is empty
//	1 = text-content extracted (L2.5) — `Blocks` contains one text block
//	    per page with `Lines/Runs` holding text but no bboxes
//	2 = full position-aware layout (L3) — `Blocks`/`Lines`/`Runs` have
//	    real `BBox`es and reading order
//
// Consumers (frontend selection geometry, AI extractors, the future
// editor write-back) can branch on this value rather than guessing at
// what's populated. Values are monotonic — a higher number means
// everything lower is also present.
type LayoutPass int

const (
	LayoutPassGeometry LayoutPass = 0
	LayoutPassText     LayoutPass = 1
	LayoutPassFull     LayoutPass = 2
)

// Page is one PDF page with its bounding geometry and (depending on
// `LayoutPass`) the layout-reconstructed `Blocks`.
type Page struct {
	ID         string     `json:"id"`
	Number     int        `json:"number"` // 1-indexed
	MediaBox   Rect       `json:"mediaBox"`
	CropBox    Rect       `json:"cropBox,omitempty"`
	Rotation   int        `json:"rotation"` // 0, 90, 180, 270
	LayoutPass LayoutPass `json:"layoutPass"`
	Blocks     []*Block   `json:"blocks"`
}

// OrientationSkewed marks a Block whose text uses a non-orthogonal
// (skewed, mirrored, or non-uniform-scaled) Tm matrix. The Block's
// BBox is the page-space AABB of the projected text-space rect, but
// the actual rendered baseline is not axis-aligned. Use as a
// sentinel — orthogonal Blocks use Orientation ∈ {0, 90, 180, 270}.
const OrientationSkewed = -1

// BlockType enumerates the high-level kinds of content a Block can hold.
type BlockType string

const (
	BlockText   BlockType = "text"
	BlockImage  BlockType = "image"
	BlockTable  BlockType = "table"
	BlockFigure BlockType = "figure"
)

// Block is a layout-detected region on a Page: a text column, a figure, a
// table, etc. Lines are populated only for `BlockText`.
type Block struct {
	ID    string    `json:"id"`
	Type  BlockType `json:"type"`
	BBox  Rect      `json:"bbox"`
	// Orientation is the on-page rotation of the block's text in
	// degrees: one of 0 (horizontal — the default and the
	// omitted-from-JSON value), 90 (text reads bottom-to-top),
	// 180 (upside-down), 270 (text reads top-to-bottom), or
	// OrientationSkewed (-1) for non-orthogonal Tm matrices
	// (italic shears, non-uniform scale, mirrors). For skewed
	// blocks, BBox is still a correct page-space AABB derived
	// from the projected text-space corners, but the rendered
	// baseline direction is not axis-aligned — consumers
	// drawing precise selection geometry should treat the bbox
	// as approximate and a full-transform field (tracked v2)
	// for cleaner selection rendering.
	//
	// BBox is always the axis-aligned bounding box of the
	// rendered glyphs in page space — the projection of the
	// rotated text-space rectangle through Tm. Consumers that
	// render the block (or compute insertion points) must read
	// Orientation to know which axis is the baseline.
	Orientation int     `json:"orientation,omitempty"`
	Lines       []*Line `json:"lines,omitempty"`
}

// Line is one baseline-aligned text line inside a Block.
type Line struct {
	ID   string `json:"id"`
	BBox Rect   `json:"bbox"`
	Runs []*Run `json:"runs"`
}

// Run is a contiguous span of text that shares a font, size, and color
// across its glyphs. Plain-text editing replaces Run.Text in place.
type Run struct {
	ID     string  `json:"id"`
	Text   string  `json:"text"`
	Font   string  `json:"font,omitempty"`
	SizePt float64 `json:"sizePt,omitempty"`
	BBox   Rect    `json:"bbox"`
}

// Glyph is the leaf node — one positioned glyph cluster. Most editor ops
// don't need glyph granularity, but the type exists for write-back
// (insertion that overflows a run) and for precise selection geometry.
type Glyph struct {
	ID   string  `json:"id"`
	Cid  string  `json:"cid"`            // glyph index in the font (hex)
	Unic string  `json:"unic,omitempty"` // ToUnicode mapping, if known
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	AdvX float64 `json:"advX"`
}

// nodeNamespace is the UUIDv5 namespace for sPDOM node IDs. Fixed so that
// IDs are stable across processes and re-parses.
var nodeNamespace = uuid.MustParse("00000000-0000-5fd0-0000-000000000005")

// NodeID returns a deterministic UUIDv5 derived from the parent ID and an
// ordinal index. Re-parsing the same PDF produces the same tree, so the
// frontend (and collab CRDT) can address nodes stably across reloads.
//
// Note: this is the *position-derived* anchor. When L2 content-stream
// parsing lands, runs and glyphs will additionally carry a
// *content-hash*-derived ID so that minor reorderings don't disturb stable
// addressing. The position ID stays as a fallback.
func NodeID(parent string, kind string, index int) string {
	return uuid.NewSHA1(nodeNamespace, []byte(fmt.Sprintf("%s|%s|%d", parent, kind, index))).String()
}
