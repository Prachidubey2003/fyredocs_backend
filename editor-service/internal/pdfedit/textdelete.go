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

// ErrTextDeleteNoOverlap — the text.delete op's rect didn't
// overlap any text-show op. Surfaced as 400 INVALID_INPUT so
// the caller knows the rect is empty (or wrong coordinates).
var ErrTextDeleteNoOverlap = errors.New("pdfedit: text.delete rect contains no text to remove")

// DeleteTextInRect scrubs every text-show op (Tj, ', ", TJ)
// whose rendered glyph bbox overlaps `rect`, emitting the result
// as an incremental revision. Identical to RedactArea EXCEPT no
// visual overlay is appended — text.delete is the editing
// primitive (just remove the bytes), while redact.apply is the
// privacy primitive (remove + visually mark the removed area).
//
// Same scrub mechanics as RedactArea: any-glyph-overlap
// semantics, per-item TJ surgery (only overlapping array
// strings get emptied; surviving neighbours close ranks),
// rotated/skewed/mirrored text scrubbed correctly via the
// 4-corner Tm projection. See spdom.ScrubRect.
//
// v0 constraints (same as RedactArea):
//   - Page's /Contents must be a single indirect-ref stream.
//   - Stream must be uncompressed.
//   - No re-flow: emptying a Tj eliminates its width-advance,
//     so non-deleted text later on the same line may shift
//     slightly.
//
// Returns ErrTextDeleteNoOverlap if no op overlapped the rect.
// Callers map this to 400 INVALID_INPUT so the user knows the
// coordinates were wrong / the cell was empty.
func DeleteTextInRect(original []byte, pageNum int, rect AnnotRect) ([]byte, error) {
	if pageNum < 1 {
		return nil, fmt.Errorf("pdfedit: pageNum %d must be >= 1", pageNum)
	}
	if rect.X1 <= rect.X0 || rect.Y1 <= rect.Y0 {
		return nil, fmt.Errorf("pdfedit: degenerate text.delete rect %+v", rect)
	}

	ctx, err := api.ReadContext(bytes.NewReader(original), nil)
	if err != nil {
		return nil, fmt.Errorf("pdfedit: read source PDF: %w", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("pdfedit: resolve page count: %w", err)
	}
	if pageNum > ctx.PageCount {
		return nil, fmt.Errorf("pdfedit: pageNum %d out of range [1, %d]", pageNum, ctx.PageCount)
	}

	pageDict, _, _, err := ctx.PageDict(pageNum, false)
	if err != nil {
		return nil, fmt.Errorf("pdfedit: lookup page %d: %w", pageNum, err)
	}

	contentsObj, ok := pageDict.Find("Contents")
	if !ok {
		return nil, fmt.Errorf("%w: page has no /Contents", ErrContentsNotInline)
	}
	contentsRef, ok := contentsObj.(types.IndirectRef)
	if !ok {
		return nil, fmt.Errorf("%w (got %T)", ErrContentsNotInline, contentsObj)
	}
	contentsObjNum := contentsRef.ObjectNumber.Value()

	entry, found := ctx.XRefTable.FindTableEntry(contentsObjNum, 0)
	if !found || entry.Free || entry.Offset == nil {
		return nil, fmt.Errorf("pdfedit: /Contents object %d has no xref entry", contentsObjNum)
	}
	startOff := int(*entry.Offset)
	dictBytes, streamBytes, err := extractStreamObject(original, startOff)
	if err != nil {
		return nil, err
	}
	if dictHasFilter(dictBytes) {
		return nil, ErrStreamFiltered
	}

	scrubbed, changed := spdom.ScrubRectChanged(streamBytes, spdom.Rect{
		X0: rect.X0, Y0: rect.Y0, X1: rect.X1, Y1: rect.Y1,
	})
	if !changed {
		return nil, ErrTextDeleteNoOverlap
	}

	newBody := buildStreamBody(dictBytes, scrubbed)
	var u pdfwriter.Update
	u.Set(contentsObjNum, newBody)
	return u.Bytes(original)
}
