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

// ErrEmptyTextInsert — the text.insert op was called with an
// empty `text`. Surfaced as 400 INVALID_INPUT so the caller
// knows to provide content.
var ErrEmptyTextInsert = errors.New("pdfedit: text.insert requires non-empty `text`")

// ErrEmptyFontResource — the text.insert op was called with no
// font resource name. v0 doesn't infer a default; callers must
// pick a resource that already lives in the page's /Resources/Font.
var ErrEmptyFontResource = errors.New("pdfedit: text.insert requires `font` resource name")

// ErrNonPositiveFontSize — sizePt must be > 0 for the appended
// Tf to render anything.
var ErrNonPositiveFontSize = errors.New("pdfedit: text.insert requires sizePt > 0")

// InsertText appends a fresh BT/ET block drawing `text` at
// page-space (x, y) in the supplied font/size to `pageNum`'s
// content stream, then emits the result as an incremental
// revision.
//
// The appended block is built via spdom.BuildTextBlock so its
// byte format stays in lockstep with the scrub-and-replace path
// (table.cell.edit). Nothing in the original bytes is modified —
// only an append.
//
// v0 constraints (callers map sentinels to 400 INVALID_INPUT):
//   - `text` must be non-empty.
//   - `fontResource` must be non-empty AND must already exist in
//     the page's /Resources/Font dict (we don't add fonts in v0).
//     If it doesn't, the PDF still parses but the appended glyphs
//     won't render — the call still succeeds because verifying
//     /Resources requires a deeper traversal that's tracked as a
//     v1 follow-up.
//   - `sizePt` must be > 0.
//   - Same stream constraints as ReplaceText: page's /Contents
//     must be a single indirect-ref stream, uncompressed.
//   - No reflow / clipping: a long `text` at (x, y) near the right
//     edge will run past the page bbox. AFM-width validation is
//     a tracked follow-up.
func InsertText(original []byte, pageNum int, x, y float64, text, fontResource string, sizePt float64) ([]byte, error) {
	if pageNum < 1 {
		return nil, fmt.Errorf("pdfedit: pageNum %d must be >= 1", pageNum)
	}
	if text == "" {
		return nil, ErrEmptyTextInsert
	}
	if fontResource == "" {
		return nil, ErrEmptyFontResource
	}
	if sizePt <= 0 {
		return nil, ErrNonPositiveFontSize
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

	insert := spdom.BuildTextBlock(fontResource, sizePt, x, y, text)
	newStream := make([]byte, 0, len(streamBytes)+len(insert))
	newStream = append(newStream, streamBytes...)
	newStream = append(newStream, insert...)

	newBody := buildStreamBody(dictBytes, newStream)
	var u pdfwriter.Update
	u.Set(contentsObjNum, newBody)
	return u.Bytes(original)
}
