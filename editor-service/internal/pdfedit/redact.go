package pdfedit

import (
	"bytes"
	"fmt"
	"strconv"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"editor-service/internal/pdfwriter"
	"editor-service/internal/spdom"
)

// RedactArea redacts a rectangular region on `pageNum`. The op:
//  1. Walks the page's content stream and scrubs every text-show
//     operator (Tj, ', ", TJ) whose baseline origin is inside
//     `rect` — the string operand is replaced with the empty
//     literal `()` (or `[]` for TJ arrays), so the underlying
//     glyphs no longer round-trip through copy-paste or text
//     extraction.
//  2. Appends a black-filled rectangle drawn over `rect` as a
//     visual overlay so the user can see WHERE the redaction
//     happened. The overlay is wrapped in `q…Q` so it doesn't
//     leak graphics state into subsequent content.
//  3. Emits the result as an incremental revision via pdfwriter
//     (original bytes preserved verbatim; any prior signature
//     stays valid).
//
// Scrub semantics (v0):
//   - "Inside" is judged by the baseline ORIGIN of each text-show
//     op. A long line whose Tj starts inside the rect is scrubbed
//     atomically; a line that starts outside is left alone even
//     if its glyphs would visually cross into the rect (the
//     overlay still covers them). Per-glyph partial-overlap
//     scrubbing is a tracked follow-up (plan §5.5).
//   - Rotated / skewed / mirrored / non-uniform-scaled text is
//     left UNSCRUBBED because the v0 text-state machine can't
//     reliably localise it. The overlay still hides it visually,
//     but the bytes are recoverable.
//   - Emptying a Tj eliminates its width-advance; non-redacted
//     text later on the same line may shift slightly. Acceptable
//     for v0 — the common UX is full-line / full-block redaction.
//
// Same stream constraints as ReplaceText:
//   - Page's /Contents must be a single indirect-ref stream
//     (ErrContentsNotInline).
//   - That stream must be uncompressed; /FlateDecode handling is
//     a follow-up (ErrStreamFiltered).
//
// Sentinel errors are shared with ReplaceText so the editops
// classifier can map them to 400 in one place.
func RedactArea(original []byte, pageNum int, rect AnnotRect) ([]byte, error) {
	if pageNum < 1 {
		return nil, fmt.Errorf("pdfedit: pageNum %d must be >= 1", pageNum)
	}
	if rect.X1 <= rect.X0 || rect.Y1 <= rect.Y0 {
		return nil, fmt.Errorf("pdfedit: degenerate redact rect %+v", rect)
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

	// 1. Scrub text-show ops whose origin sits inside the rect.
	scrubbed := spdom.ScrubRect(streamBytes, spdom.Rect{
		X0: rect.X0, Y0: rect.Y0, X1: rect.X1, Y1: rect.Y1,
	})

	// 2. Append the black-fill visual overlay (wrapped in q…Q so
	// the previous graphics state survives).
	overlay := buildRedactOverlay(rect)
	newStream := make([]byte, 0, len(scrubbed)+len(overlay))
	newStream = append(newStream, scrubbed...)
	newStream = append(newStream, overlay...)

	// 3. Emit as an incremental revision.
	newBody := buildStreamBody(dictBytes, newStream)
	var u pdfwriter.Update
	u.Set(contentsObjNum, newBody)
	return u.Bytes(original)
}

// buildRedactOverlay returns the content-stream snippet that draws
// a black-filled rectangle covering `rect`. The snippet is wrapped
// in q…Q so its color + line-state changes don't bleed into ops
// that come after the appended bytes.
//
// Format: `\nq\n0 0 0 rg\n<x> <y> <w> <h> re\nf\nQ\n` — standard
// PDF graphics ops per ISO 32000-1 §8.5.
func buildRedactOverlay(rect AnnotRect) []byte {
	var b bytes.Buffer
	b.WriteString("\nq\n0 0 0 rg\n")
	b.WriteString(formatRectFloat(rect.X0))
	b.WriteByte(' ')
	b.WriteString(formatRectFloat(rect.Y0))
	b.WriteByte(' ')
	b.WriteString(formatRectFloat(rect.X1 - rect.X0))
	b.WriteByte(' ')
	b.WriteString(formatRectFloat(rect.Y1 - rect.Y0))
	b.WriteString(" re\nf\nQ\n")
	return b.Bytes()
}

// formatRectFloat renders a float64 as a minimal PDF-friendly
// number literal. We avoid scientific notation (PDF parsers
// generally reject it inside content streams).
func formatRectFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
