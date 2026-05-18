package pdfedit

import (
	"bytes"
	"fmt"
	"io"

	"editor-service/internal/pdfwriter"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// RotatePage returns the bytes of a new revision in which page `pageNum`
// (1-indexed) is rotated to `degrees`. The original bytes are preserved
// verbatim — only an incremental update is appended.
//
// Allowed rotations per ISO 32000-1 §14.8.4: a multiple of 90 in the
// range [0, 270]. The /Rotate entry on a Page is *inheritable* from
// ancestor Pages nodes, so we always write it on the leaf Page dict —
// that way the rotation takes effect for this page regardless of what
// the inherited value was.
//
// degrees=0 still writes an explicit /Rotate 0 on the page. We chose
// this over "if 0, drop /Rotate" because the latter would *unmask* an
// inherited rotation from a parent Pages node, surprising the caller
// who said "rotate to 0".
//
// Errors are returned for an invalid rotation, a page number out of
// range, an unreadable PDF, or an xref-stream document (the v0
// incremental writer cannot extend xref-stream files yet — see
// pdfwriter/doc.go).
func RotatePage(original []byte, pageNum, degrees int) ([]byte, error) {
	if degrees%90 != 0 || degrees < 0 || degrees > 270 {
		return nil, fmt.Errorf("pdfedit: rotation %d not allowed (must be 0/90/180/270)", degrees)
	}

	ctx, err := api.ReadContext(bytes.NewReader(original), nil)
	if err != nil {
		return nil, fmt.Errorf("pdfedit: read source PDF: %w", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("pdfedit: resolve page count: %w", err)
	}
	if pageNum < 1 || pageNum > ctx.PageCount {
		return nil, fmt.Errorf("pdfedit: pageNum %d out of range [1, %d]", pageNum, ctx.PageCount)
	}

	// PageDict resolves the leaf Page dictionary plus its IndirectRef
	// (object number + generation). `consolidateRes=false` keeps the
	// dict's surface keys intact — we don't want pdfcpu to inline
	// inherited Resources into the page, just to surface the on-page
	// keys so we can re-emit them faithfully.
	pageDict, pageRef, _, err := ctx.PageDict(pageNum, false)
	if err != nil {
		return nil, fmt.Errorf("pdfedit: lookup page %d: %w", pageNum, err)
	}
	if pageDict == nil || pageRef == nil {
		return nil, fmt.Errorf("pdfedit: page %d has no resolvable dict", pageNum)
	}

	// Replace or insert /Rotate. types.Dict is a map so Update is the
	// right primitive — Insert would no-op if /Rotate were already set.
	pageDict.Update("Rotate", types.Integer(degrees))

	body := pageDict.PDFString()

	var u pdfwriter.Update
	u.Set(pageRef.ObjectNumber.Value(), []byte(body))
	return u.Bytes(original)
}

// RotatePageTo writes the rotated revision directly to `w`. It exists
// for callers that stream the new revision to disk (or to an HTTP
// response) without holding two copies in memory.
func RotatePageTo(w io.Writer, original []byte, pageNum, degrees int) error {
	out, err := RotatePage(original, pageNum, degrees)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}
