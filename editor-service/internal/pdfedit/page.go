package pdfedit

import (
	"bytes"
	"fmt"

	"editor-service/internal/pdfwriter"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// DeletePage returns the bytes of a new revision in which page
// `pageNum` (1-indexed) has been removed from the document.
//
// What's emitted:
//
//   - A rewrite of the parent Pages-node dict with the page's
//     /IndirectRef stripped from /Kids and /Count decremented by 1.
//   - Walks up the page tree, decrementing /Count on each ancestor
//     all the way to the root (ISO 32000-1 §7.7.3.2 — every Pages
//     node's /Count is the number of leaf pages it contains).
//
// What's NOT emitted:
//
//   - The page object itself is not removed from the xref. Per
//     §7.5.6 incremental updates are append-only; an unreferenced
//     object simply becomes garbage that future generation-bump
//     deletions (or a future full rewrite via pdfcpu's optimise
//     pass) can sweep. Readers don't care — they only look at what's
//     reachable from /Catalog.
//
// Constraints:
//
//   - Deleting the last page is refused (would orphan the catalog
//     and produce an invalid PDF).
//   - v0 supports flat page trees and nested trees; we walk up via
//     /Parent until a node has none. Inheritance of /MediaBox or
//     /Resources from a deleted page's ancestor still resolves
//     because we don't touch those attributes.
func DeletePage(original []byte, pageNum int) ([]byte, error) {
	if pageNum < 1 {
		return nil, fmt.Errorf("pdfedit: pageNum %d must be >= 1", pageNum)
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
	if ctx.PageCount == 1 {
		// A document must have at least one page to be valid — refuse
		// to produce an invalid PDF rather than let the caller pick up
		// the pieces.
		return nil, fmt.Errorf("pdfedit: refusing to delete the last page (document would have 0 pages)")
	}

	pageDict, pageRef, _, err := ctx.PageDict(pageNum, false)
	if err != nil {
		return nil, fmt.Errorf("pdfedit: lookup page %d: %w", pageNum, err)
	}
	if pageDict == nil || pageRef == nil {
		return nil, fmt.Errorf("pdfedit: page %d has no resolvable dict", pageNum)
	}

	parentObj, ok := pageDict.Find("Parent")
	if !ok {
		return nil, fmt.Errorf("pdfedit: page %d has no /Parent — corrupt page tree", pageNum)
	}
	parentRef, ok := parentObj.(types.IndirectRef)
	if !ok {
		return nil, fmt.Errorf("pdfedit: page %d /Parent is %T, want IndirectRef", pageNum, parentObj)
	}

	var u pdfwriter.Update

	// Walk up the page tree, rewriting each ancestor's dict. The
	// leaf's /Parent is the immediate Pages node — that's where we
	// remove the /Kids entry. Every ancestor only sees a /Count
	// decrement.
	current := parentRef
	stripFromKids := true
	for {
		dict, err := ctx.DereferenceDict(current)
		if err != nil {
			return nil, fmt.Errorf("pdfedit: dereference page-tree node %d: %w",
				current.ObjectNumber, err)
		}
		if dict == nil {
			return nil, fmt.Errorf("pdfedit: page-tree node %d resolves to nil",
				current.ObjectNumber)
		}

		if stripFromKids {
			kidsObj, ok := dict.Find("Kids")
			if !ok {
				return nil, fmt.Errorf("pdfedit: parent Pages node %d has no /Kids",
					current.ObjectNumber)
			}
			kids, ok := kidsObj.(types.Array)
			if !ok {
				return nil, fmt.Errorf("pdfedit: parent Pages /Kids is %T, want Array",
					kidsObj)
			}
			newKids := make(types.Array, 0, len(kids))
			removed := false
			for _, k := range kids {
				if ref, ok := k.(types.IndirectRef); ok &&
					ref.ObjectNumber == pageRef.ObjectNumber &&
					ref.GenerationNumber == pageRef.GenerationNumber {
					removed = true
					continue
				}
				newKids = append(newKids, k)
			}
			if !removed {
				return nil, fmt.Errorf("pdfedit: page %d (obj %d) not found in parent /Kids",
					pageNum, pageRef.ObjectNumber)
			}
			dict.Update("Kids", newKids)
			stripFromKids = false // only the immediate parent loses a child
		}

		// Decrement /Count on every ancestor including the immediate
		// parent.
		newCount, err := decrementCount(dict)
		if err != nil {
			return nil, fmt.Errorf("pdfedit: page-tree node %d: %w",
				current.ObjectNumber, err)
		}
		if newCount < 0 {
			return nil, fmt.Errorf("pdfedit: page-tree node %d /Count would go negative",
				current.ObjectNumber)
		}

		u.Set(int(current.ObjectNumber), []byte(dict.PDFString()))

		// Climb to the next ancestor; stop at the root.
		parentObj, ok := dict.Find("Parent")
		if !ok {
			break
		}
		nextRef, ok := parentObj.(types.IndirectRef)
		if !ok {
			return nil, fmt.Errorf("pdfedit: page-tree node %d /Parent is %T, want IndirectRef",
				current.ObjectNumber, parentObj)
		}
		current = nextRef
	}

	return u.Bytes(original)
}

// decrementCount decrements the dict's /Count by one and returns the
// new value. If /Count is missing or non-integer the dict is treated
// as malformed.
func decrementCount(d types.Dict) (int, error) {
	obj, ok := d.Find("Count")
	if !ok {
		return 0, fmt.Errorf("missing /Count")
	}
	n, ok := obj.(types.Integer)
	if !ok {
		return 0, fmt.Errorf("/Count is %T, want Integer", obj)
	}
	newN := int(n) - 1
	d.Update("Count", types.Integer(newN))
	return newN, nil
}

// incrementCount increments the dict's /Count by one and returns the
// new value. Symmetric companion to [decrementCount].
func incrementCount(d types.Dict) (int, error) {
	obj, ok := d.Find("Count")
	if !ok {
		return 0, fmt.Errorf("missing /Count")
	}
	n, ok := obj.(types.Integer)
	if !ok {
		return 0, fmt.Errorf("/Count is %T, want Integer", obj)
	}
	newN := int(n) + 1
	d.Update("Count", types.Integer(newN))
	return newN, nil
}

// InsertBlankPage returns the bytes of a new revision with a fresh
// blank page inserted *after* `afterPage` (1-indexed). Use
// `afterPage = 0` to insert before the first page.
//
// What's emitted:
//
//   - A new indirect /Page object at the next-free object number,
//     with /Type /Page, /Parent pointing at the immediate Pages
//     node, /MediaBox copied from the reference page (or US Letter
//     if no reference exists), and empty /Resources. No /Contents —
//     the page is truly blank.
//   - A rewritten parent Pages-node dict with the new page's
//     IndirectRef inserted into /Kids at the right position and
//     /Count incremented.
//   - Every ancestor up to the root gets /Count incremented so the
//     document's notion of total page count stays consistent (ISO
//     32000-1 §7.7.3.2).
//
// Constraints:
//
//   - `afterPage` must be in [0, ctx.PageCount]. Out-of-range errors
//     map cleanly to 400 via editops.
//   - v0 always inserts a US-Letter-sized page when `afterPage = 0`
//     and the document has no pages (which the editor never actually
//     allows — DeletePage refuses to leave a 0-page doc — but the
//     defensive default keeps the failure shape clean).
//   - The new page's reference is placed into the *immediate parent*
//     Pages node of the reference page. With a flat tree (the common
//     case) that's the root Pages node. With a nested tree the new
//     page sits alongside its sibling reference page.
func InsertBlankPage(original []byte, afterPage int) ([]byte, error) {
	if afterPage < 0 {
		return nil, fmt.Errorf("pdfedit: afterPage %d must be >= 0", afterPage)
	}

	ctx, err := api.ReadContext(bytes.NewReader(original), nil)
	if err != nil {
		return nil, fmt.Errorf("pdfedit: read source PDF: %w", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("pdfedit: resolve page count: %w", err)
	}
	if afterPage > ctx.PageCount {
		return nil, fmt.Errorf("pdfedit: afterPage %d out of range [0, %d]", afterPage, ctx.PageCount)
	}

	// Pick the reference page whose /MediaBox + /Parent we inherit
	// from. afterPage=0 borrows page 1; otherwise we use afterPage
	// itself. The "no pages at all" branch is the defensive fallback
	// described above.
	refPageNum := afterPage
	if refPageNum == 0 {
		refPageNum = 1
	}

	var (
		mediaBox  types.Object
		parentRef types.IndirectRef
	)
	if ctx.PageCount > 0 {
		refDict, _, _, err := ctx.PageDict(refPageNum, false)
		if err != nil {
			return nil, fmt.Errorf("pdfedit: lookup reference page %d: %w", refPageNum, err)
		}
		if refDict == nil {
			return nil, fmt.Errorf("pdfedit: reference page %d has no resolvable dict", refPageNum)
		}
		// MediaBox might be inherited from the parent — try the leaf
		// first, fall back to US Letter if absent. (pdfcpu can
		// consolidate, but we asked for the leaf-only view above.)
		if mb, ok := refDict.Find("MediaBox"); ok {
			mediaBox = mb
		} else {
			mediaBox = defaultMediaBox()
		}
		pObj, ok := refDict.Find("Parent")
		if !ok {
			return nil, fmt.Errorf("pdfedit: reference page %d has no /Parent — corrupt page tree", refPageNum)
		}
		ref, ok := pObj.(types.IndirectRef)
		if !ok {
			return nil, fmt.Errorf("pdfedit: reference page %d /Parent is %T, want IndirectRef", refPageNum, pObj)
		}
		parentRef = ref
	} else {
		return nil, fmt.Errorf("pdfedit: cannot insert into a document with no pages")
	}

	// Reserve a new object number for the page we're about to
	// synthesise. Claim it in pdfcpu's table so no concurrent helper
	// hands the same slot to another mutation.
	if ctx.XRefTable.Size == nil {
		return nil, fmt.Errorf("pdfedit: xref table has no Size")
	}
	newPageObjNum := *ctx.XRefTable.Size
	*ctx.XRefTable.Size++

	// Build the new page dict body. Hand-rolled (rather than via
	// types.Dict.PDFString) to keep the output compact + the field
	// ordering predictable for tests.
	newPageBody := buildBlankPageBody(parentRef, mediaBox)

	// Determine where the new page goes in the parent's /Kids array.
	// Find the index of the reference page (or use 0 when afterPage=0).
	parentDict, err := ctx.DereferenceDict(parentRef)
	if err != nil {
		return nil, fmt.Errorf("pdfedit: dereference parent %d: %w", parentRef.ObjectNumber, err)
	}
	kidsObj, ok := parentDict.Find("Kids")
	if !ok {
		return nil, fmt.Errorf("pdfedit: parent Pages node %d has no /Kids", parentRef.ObjectNumber)
	}
	kids, ok := kidsObj.(types.Array)
	if !ok {
		return nil, fmt.Errorf("pdfedit: parent Pages /Kids is %T, want Array", kidsObj)
	}

	newRef := types.IndirectRef{
		ObjectNumber:     types.Integer(newPageObjNum),
		GenerationNumber: types.Integer(0),
	}

	var insertAt int
	if afterPage == 0 {
		insertAt = 0
	} else {
		// Find the reference page in /Kids. With a nested tree the
		// reference page might be a grandchild, but PageDict gave us
		// its IndirectRef so we can look for that.
		_, refPageRef, _, err := ctx.PageDict(refPageNum, false)
		if err != nil || refPageRef == nil {
			return nil, fmt.Errorf("pdfedit: re-resolve reference page %d: %w", refPageNum, err)
		}
		found := -1
		for i, k := range kids {
			if r, ok := k.(types.IndirectRef); ok &&
				r.ObjectNumber == refPageRef.ObjectNumber &&
				r.GenerationNumber == refPageRef.GenerationNumber {
				found = i
				break
			}
		}
		if found < 0 {
			return nil, fmt.Errorf("pdfedit: reference page %d not found in parent /Kids (nested page tree?)", refPageNum)
		}
		insertAt = found + 1
	}

	// Splice newRef into /Kids at insertAt.
	newKids := make(types.Array, 0, len(kids)+1)
	newKids = append(newKids, kids[:insertAt]...)
	newKids = append(newKids, newRef)
	newKids = append(newKids, kids[insertAt:]...)
	parentDict.Update("Kids", newKids)

	var u pdfwriter.Update
	u.Set(newPageObjNum, []byte(newPageBody))

	// Walk up the page tree from the immediate parent, incrementing
	// /Count at every level. The immediate parent is mutated here,
	// every other ancestor only sees a /Count bump.
	current := parentRef
	for {
		dict, err := ctx.DereferenceDict(current)
		if err != nil {
			return nil, fmt.Errorf("pdfedit: dereference page-tree node %d: %w",
				current.ObjectNumber, err)
		}
		if dict == nil {
			return nil, fmt.Errorf("pdfedit: page-tree node %d resolves to nil",
				current.ObjectNumber)
		}
		if _, err := incrementCount(dict); err != nil {
			return nil, fmt.Errorf("pdfedit: page-tree node %d: %w",
				current.ObjectNumber, err)
		}
		u.Set(int(current.ObjectNumber), []byte(dict.PDFString()))

		parentObj, ok := dict.Find("Parent")
		if !ok {
			break
		}
		nextRef, ok := parentObj.(types.IndirectRef)
		if !ok {
			return nil, fmt.Errorf("pdfedit: page-tree node %d /Parent is %T, want IndirectRef",
				current.ObjectNumber, parentObj)
		}
		current = nextRef
	}

	return u.Bytes(original)
}

// defaultMediaBox returns the standard US Letter rectangle in PDF
// user-space points: [0 0 612 792]. Used when the reference page
// doesn't carry an explicit /MediaBox (rare in practice but possible
// when the entry is inherited from an ancestor in a way our
// non-consolidating PageDict view doesn't surface).
func defaultMediaBox() types.Array {
	return types.Array{
		types.Integer(0),
		types.Integer(0),
		types.Integer(612),
		types.Integer(792),
	}
}

// buildBlankPageBody returns the PDF body bytes for a new blank
// /Page object: /Type /Page, /Parent <ref>, /MediaBox <…>, empty
// /Resources. No /Contents — the page renders white.
func buildBlankPageBody(parent types.IndirectRef, mediaBox types.Object) string {
	mb := serialiseMediaBox(mediaBox)
	return fmt.Sprintf(
		"<< /Type /Page /Parent %d %d R /MediaBox %s /Resources << >> >>",
		parent.ObjectNumber, parent.GenerationNumber, mb,
	)
}

// serialiseMediaBox formats the /MediaBox value. We accept both the
// canonical types.Array form and any other pdfcpu value that has a
// PDFString() method; if neither applies we fall back to US Letter.
func serialiseMediaBox(o types.Object) string {
	if a, ok := o.(types.Array); ok {
		return a.PDFString()
	}
	if s, ok := o.(interface{ PDFString() string }); ok {
		return s.PDFString()
	}
	return defaultMediaBox().PDFString()
}
