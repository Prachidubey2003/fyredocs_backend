package pdfedit

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"editor-service/internal/pdfwriter"
	"editor-service/internal/spdom"
)

// ErrCellEmpty — the table.cell.edit rect didn't overlap any
// text-show op on the page. Surfaced as 400 INVALID_INPUT so the
// caller knows the cell coordinates were wrong (or empty).
var ErrCellEmpty = errors.New("pdfedit: cell rect contains no text to replace")

// ErrGridNotDetected — EditTableCellByCoord ran spdom.DetectTableGrid
// over the supplied region and didn't find a regular table
// structure (too sparse, too irregular, < 2 rows or < 2 cols).
// The caller falls back to the rect form OR re-asks the user to
// widen the region.
var ErrGridNotDetected = errors.New("pdfedit: region is not a recognisable table grid")

// ErrCoordOutOfRange — a {row, col} request landed past the
// detected grid bounds. Distinct from ErrGridNotDetected so the
// caller can distinguish "table found but you asked for cell
// (5, 7) of a 3×4 grid" (UI bug — clamp + retry) from "no
// table at all" (region selection bug).
var ErrCoordOutOfRange = errors.New("pdfedit: (row, col) is outside the detected grid")

// pageStream bundles the dict + stream bytes plus the object
// number of one page's /Contents stream. Returned by
// loadPageStream so EditTableCell + EditTableCellByCoord share
// the same parse path without each re-opening the PDF.
type pageStream struct {
	objNum      int
	dictBytes   []byte
	streamBytes []byte
}

// loadPageStream resolves `pageNum`'s /Contents indirect-ref
// and returns the underlying dict + stream byte ranges from
// the original PDF. Surfaces the same content-stream sentinels
// the rest of pdfedit uses (ErrContentsNotInline for nested
// arrays / direct streams, ErrStreamFiltered for /FlateDecode
// + friends) so a caller can map them to 400 INVALID_INPUT
// without unwrapping the upstream pdfcpu error.
func loadPageStream(original []byte, pageNum int) (pageStream, error) {
	if pageNum < 1 {
		return pageStream{}, fmt.Errorf("pdfedit: pageNum %d must be >= 1", pageNum)
	}
	ctx, err := api.ReadContext(bytes.NewReader(original), nil)
	if err != nil {
		return pageStream{}, fmt.Errorf("pdfedit: read source PDF: %w", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return pageStream{}, fmt.Errorf("pdfedit: resolve page count: %w", err)
	}
	if pageNum > ctx.PageCount {
		return pageStream{}, fmt.Errorf("pdfedit: pageNum %d out of range [1, %d]", pageNum, ctx.PageCount)
	}

	pageDict, _, _, err := ctx.PageDict(pageNum, false)
	if err != nil {
		return pageStream{}, fmt.Errorf("pdfedit: lookup page %d: %w", pageNum, err)
	}

	contentsObj, ok := pageDict.Find("Contents")
	if !ok {
		return pageStream{}, fmt.Errorf("%w: page has no /Contents", ErrContentsNotInline)
	}
	contentsRef, ok := contentsObj.(types.IndirectRef)
	if !ok {
		return pageStream{}, fmt.Errorf("%w (got %T)", ErrContentsNotInline, contentsObj)
	}
	contentsObjNum := contentsRef.ObjectNumber.Value()

	entry, found := ctx.XRefTable.FindTableEntry(contentsObjNum, 0)
	if !found || entry.Free || entry.Offset == nil {
		return pageStream{}, fmt.Errorf("pdfedit: /Contents object %d has no xref entry", contentsObjNum)
	}
	startOff := int(*entry.Offset)
	dictBytes, streamBytes, err := extractStreamObject(original, startOff)
	if err != nil {
		return pageStream{}, err
	}
	if dictHasFilter(dictBytes) {
		return pageStream{}, ErrStreamFiltered
	}
	return pageStream{
		objNum:      contentsObjNum,
		dictBytes:   dictBytes,
		streamBytes: streamBytes,
	}, nil
}

// EditTableCell rewrites the page's content stream so that the
// text-show ops whose glyph rect overlaps `rect` are scrubbed,
// and a fresh BT block drawing `newText` is appended at the
// anchor of the FIRST scrubbed op in its active font.
//
// v1 behaviour (this version):
//
//   - Cell text is gone from the byte stream (not just visually
//     covered). Copy/paste and text extraction won't surface it.
//   - Replacement text appears at the original cell's reading
//     position in the original font.
//   - **Multi-line wrapping**: if `newText` is wider than the
//     cell (rect.X1 minus the captured anchor's x), the
//     replacement is wrapped on whitespace boundaries and
//     emitted as N stacked BT/ET blocks separated by
//     1.2 × fontSize. Words longer than the cell's width are
//     emitted on their own line without mid-word breaks —
//     we don't hyphenate (would corrupt copy/paste).
//
// Still-out-of-scope (tracked):
//
//   - Grid auto-detection: the caller still supplies the rect.
//     The follow-up will derive it from the sPDOM layout pass.
//   - Cell-height clamping: lines that fall below rect.Y0 are
//     still emitted. Silent truncation would be a data-loss
//     hazard; visible overflow signals the caller that the
//     cell isn't tall enough.
//   - Font / size still come from the FIRST overlapping op
//     only. Mixed-font cells get a uniform replacement.
//   - Same stream constraints as ReplaceText (single
//     indirect-ref /Contents, uncompressed).
//
// Returns ErrCellEmpty if no text-show op overlapped the rect —
// the caller maps this to 400 INVALID_INPUT.
func EditTableCell(original []byte, pageNum int, rect AnnotRect, newText string) ([]byte, error) {
	if rect.X1 <= rect.X0 || rect.Y1 <= rect.Y0 {
		return nil, fmt.Errorf("pdfedit: degenerate cell rect %+v", rect)
	}
	ps, err := loadPageStream(original, pageNum)
	if err != nil {
		return nil, err
	}
	return applyCellEdit(original, ps, spdom.Rect{
		X0: rect.X0, Y0: rect.Y0, X1: rect.X1, Y1: rect.Y1,
	}, newText)
}

// EditTableCellByCoord locates cell (row, col) inside `region`
// via spdom.DetectTableGrid and replaces its contents with
// `newText` — same end state as EditTableCell, but cell
// selection is done by row/col instead of a caller-computed
// rect.
//
// Use this when the caller knows the table's overall bounding
// box (a tap inside the table on mobile, a user-drawn region
// on web) but doesn't want to compute per-cell rects. The
// detection algorithm — heuristics + thresholds — lives in
// [`spdom.DetectTableGrid`](../../spdom/tablegrid.go);
// EditTableCellByCoord is the wire-up that exposes it at the
// pdfedit layer.
//
// Errors:
//   - ErrGridNotDetected: `region` doesn't look like a table.
//     Caller falls back to rect form OR widens the region.
//   - ErrCoordOutOfRange: (row, col) is past the detected
//     grid's bounds. Distinct from ErrGridNotDetected so the
//     caller can clamp + retry vs re-ask for a region.
//   - ErrCellEmpty: cell was detected but its rect contains
//     no text to replace (edge case — typically means the
//     detection over-extended the rect).
//   - Same content-stream constraints as EditTableCell
//     (single indirect-ref /Contents, uncompressed).
func EditTableCellByCoord(original []byte, pageNum int, region AnnotRect, row, col int, newText string) ([]byte, error) {
	if row < 0 || col < 0 {
		return nil, fmt.Errorf("pdfedit: row=%d col=%d must both be >= 0", row, col)
	}
	if region.X1 <= region.X0 || region.Y1 <= region.Y0 {
		return nil, fmt.Errorf("pdfedit: degenerate region rect %+v", region)
	}
	ps, err := loadPageStream(original, pageNum)
	if err != nil {
		return nil, err
	}

	// fontMap=nil keeps EditTableCellByCoord free of pdfcpu's
	// font-resource resolver. DetectTableGrid is documented as
	// "layout-only — doesn't depend on font names"; passing
	// nil costs us ~half-em precision on column edges (the
	// fallback advance), which is well within the 4pt anchor
	// tolerance the algorithm already accepts.
	cells, ok := spdom.DetectTableGrid(ps.streamBytes, nil, spdom.Rect{
		X0: region.X0, Y0: region.Y0, X1: region.X1, Y1: region.Y1,
	})
	if !ok {
		return nil, ErrGridNotDetected
	}

	cell, found := findCellByCoord(cells, row, col)
	if !found {
		return nil, ErrCoordOutOfRange
	}
	return applyCellEdit(original, ps, cell.Rect, newText)
}

// applyCellEdit is the shared "scrub + replace + emit
// incremental update" tail used by both the rect and coord
// entry points. Takes the already-loaded page stream + the
// target rect in spdom coordinates.
func applyCellEdit(original []byte, ps pageStream, rect spdom.Rect, newText string) ([]byte, error) {
	// lineLeadingFactor=0 → spdom defaults to 1.2 × fontSize,
	// which is conventional body-text leading. A future cycle
	// could plumb a real leading value through from the cell's
	// source TL operator when present.
	newStream, hit := spdom.ReplaceRectFirstAnchorWrapped(ps.streamBytes, rect, newText, 0)
	if !hit {
		return nil, ErrCellEmpty
	}
	newBody := buildStreamBody(ps.dictBytes, newStream)
	var u pdfwriter.Update
	u.Set(ps.objNum, newBody)
	return u.Bytes(original)
}

// findCellByCoord scans cells for the one matching (row, col).
// Returns false when (row, col) is outside the detected grid.
// Linear scan; the cell count is bounded (row × col, both
// typically < 50) so this is faster than building a map for
// the single lookup.
func findCellByCoord(cells []spdom.Cell, row, col int) (spdom.Cell, bool) {
	for _, c := range cells {
		if c.Row == row && c.Col == col {
			return c, true
		}
	}
	return spdom.Cell{}, false
}
