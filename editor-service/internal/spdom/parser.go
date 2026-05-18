package spdom

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// Parse reads a PDF and returns its sPDOM.
//
// Layers exercised today:
//   - L1 (bytes → objects): pdfcpu reads the PDF and exposes page count +
//     per-page geometry.
//   - L2.5 / L3 (content stream → text / positions): for each page, we
//     pull the content-stream bytes and try the position-aware extractor
//     first (LayoutPassFull). If a page uses rotated/skewed text matrices
//     we cannot reason about with the conservative implementation in
//     layout.go, we fall back to the plain-text extractor and emit
//     LayoutPassText — bboxes on the fallback path use the page mediabox
//     and should not be trusted at run granularity.
//
// What remains for full L3 coverage (tracked Phase 1 follow-up):
//   - Real rotation/skew bbox math (currently rotated / skewed /
//     mirrored / non-uniform-scaled text is silently dropped from L4;
//     pages with any horizontal text still land at LayoutPassFull).
//   - Glyph-level capture for write-back overflow handling.
//
// Recursive XY-cut already ships (xycut.go): the parser slices each
// page into reading-order regions on BOTH axes — handles 3+ columns,
// header banners over a 2-column body, two-up reports, and sidebars
// in one algorithm. Tuned with a font-size-relative absolute minimum
// (1.5× mean font size) so tight sub-regions can't mis-detect normal
// line leading as a section break.
//
// Per-glyph widths from font metrics already ship: the resource-name →
// BaseFont map built by extractPageFontMap routes Tf operands through
// pdfcpu's AFM tables for the 14 PDF core fonts (see widths.go).
// Opaque / non-core fonts still fall back to the 0.5em-per-glyph
// average inside CharAdvance.
//
// Uniform-scale text matrices (a == d > 0, b == c == 0) ARE handled:
// the scale factor is folded into the effective font size on emit, so
// scale-via-Tm renders the same width math as scale-via-Tf.
//
// The L4 data shape is stable, so adding those layers won't change the
// public API — only the populated fields per Page.LayoutPass.
//
// `id` is the caller-supplied document id (typically the database row's
// UUID). It seeds the deterministic NodeID derivation so node IDs are
// stable across re-parses of the same file.
func Parse(id string, r io.Reader) (*Document, error) {
	if id == "" {
		return nil, fmt.Errorf("spdom: empty document id")
	}
	if r == nil {
		return nil, fmt.Errorf("spdom: nil reader")
	}

	// pdfcpu wants an io.ReadSeeker. Buffer in memory — sPDOM parsing is
	// not in the hot path; callers stream-write to disk before invoking us.
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("spdom: read input: %w", err)
	}
	if len(buf) == 0 {
		return nil, fmt.Errorf("spdom: empty input")
	}

	ctx, err := api.ReadContext(bytes.NewReader(buf), nil)
	if err != nil {
		return nil, fmt.Errorf("spdom: parse PDF: %w", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("spdom: resolve page count: %w", err)
	}

	doc := &Document{
		ID:         id,
		PDFVersion: ctx.HeaderVersion.String(),
		PageCount:  ctx.PageCount,
		Pages:      make([]*Page, 0, ctx.PageCount),
	}

	// pdfcpu exposes per-page dimensions in PDF user-space points.
	// PageDims returns 1-indexed values; we honor that by walking 1..N.
	dims, err := ctx.PageDims()
	if err != nil {
		// Non-fatal: emit pages without dimensions rather than refuse to
		// build any sPDOM at all.
		dims = nil
	}

	for i := 1; i <= ctx.PageCount; i++ {
		page := &Page{
			ID:         NodeID(doc.ID, "page", i),
			Number:     i,
			LayoutPass: LayoutPassGeometry,
			Blocks:     []*Block{},
		}
		if dims != nil && i-1 < len(dims) {
			d := dims[i-1]
			page.MediaBox = Rect{X0: 0, Y0: 0, X1: d.Width, Y1: d.Height}
		}

		// L3 / L2.5 — content-stream extraction. Best-effort; a page with
		// no /Contents (e.g., the minimal fixture) or unreadable content
		// is fine and leaves the page at LayoutPassGeometry.
		if stream, ok := extractPageStream(ctx, i); ok && len(stream) > 0 {
			fontMap := extractPageFontMap(ctx, i)
			events, supported := extractTextEvents(stream, fontMap)
			if supported && len(events) > 0 {
				page.LayoutPass = LayoutPassFull
				// Column-aware build: 2-column pages get a
				// vertical-gap split before per-column line
				// clustering, so left + right baselines don't
				// merge into one line. Single-column pages
				// fall through to the identical single-pass
				// result.
				page.Blocks = BuildBlocksColumnAware(page.ID, page.MediaBox, events)
			} else {
				text := stripControlBytes(extractText(stream))
				if strings.TrimSpace(text) != "" {
					page.LayoutPass = LayoutPassText
					page.Blocks = buildTextBlocks(page.ID, page.MediaBox, text)
				}
			}
		}

		doc.Pages = append(doc.Pages, page)
	}

	return doc, nil
}

// extractPageStream returns the decoded content-stream bytes of page `n`
// (1-indexed). Returns ok=false when pdfcpu cannot resolve a content
// stream for the page (common for empty pages and for pages with
// image-only content; not an error).
func extractPageStream(ctx *model.Context, n int) ([]byte, bool) {
	rd, err := pdfcpu.ExtractPageContent(ctx, n)
	if err != nil || rd == nil {
		return nil, false
	}
	stream, err := io.ReadAll(rd)
	if err != nil || len(stream) == 0 {
		return nil, false
	}
	return stream, true
}

// extractPageFontMap walks the page's `/Resources/Font` dictionary and
// returns a map of content-stream resource name (e.g. "F1") to canonical
// font name (e.g. "Helvetica") taken from each font's `/BaseFont`. Used
// by the L3 extractor to resolve opaque Tf operands so approxWidth /
// CharAdvance can reach pdfcpu's AFM tables for the 14 PDF core fonts.
//
// Returns nil (not an error) on any failure — every caller treats the
// map as best-effort context. A missing entry just means we fall back
// to the historical 0.5em-per-glyph average for that font.
//
// BaseFont may carry the subset-tag prefix specified by ISO 32000-1
// §9.6.4 ("six uppercase letters + '+'", e.g. `BCDEEE+Helvetica`). We
// strip it so subset-embedded copies of the core fonts still match the
// font.IsCoreFont check.
func extractPageFontMap(ctx *model.Context, pageNr int) map[string]string {
	if ctx == nil {
		return nil
	}
	_, _, inhPAttrs, err := ctx.PageDict(pageNr, false)
	if err != nil || inhPAttrs == nil || inhPAttrs.Resources == nil {
		return nil
	}
	fontsRaw, found := inhPAttrs.Resources.Find("Font")
	if !found {
		return nil
	}
	fontsDict, err := ctx.DereferenceDict(fontsRaw)
	if err != nil || fontsDict == nil {
		return nil
	}
	out := make(map[string]string, len(fontsDict))
	for resourceName, obj := range fontsDict {
		fontDict, err := ctx.DereferenceDict(obj)
		if err != nil || fontDict == nil {
			continue
		}
		baseFont := fontDict.NameEntry("BaseFont")
		if baseFont == nil {
			continue
		}
		out[resourceName] = stripSubsetTag(*baseFont)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stripSubsetTag removes the ISO 32000-1 §9.6.4 subset tag prefix from
// a BaseFont name. The tag is exactly six uppercase ASCII letters
// followed by '+' (e.g. `BCDEEE+Helvetica` → `Helvetica`). Anything not
// matching that exact shape is returned unchanged.
func stripSubsetTag(name string) string {
	if len(name) < 8 || name[6] != '+' {
		return name
	}
	for i := 0; i < 6; i++ {
		c := name[i]
		if c < 'A' || c > 'Z' {
			return name
		}
	}
	return name[7:]
}

// buildTextBlocks turns a page's extracted plaintext into one BlockText
// containing one Line per non-empty source line. Each Line contains one
// Run with the line's text — there is no font/size grouping yet because
// we don't have that information at this layer.
//
// BBoxes are zero (geometry pending). Consumers should branch on
// Page.LayoutPass to decide whether bboxes are trustworthy.
func buildTextBlocks(pageID string, mediaBox Rect, text string) []*Block {
	rawLines := strings.Split(text, "\n")
	lines := make([]*Line, 0, len(rawLines))
	lineIdx := 0
	for _, raw := range rawLines {
		trimmed := strings.TrimRight(raw, " \t\r")
		if trimmed == "" {
			continue
		}
		lineIdx++
		blockID := NodeID(pageID, "block", 1)
		lineID := NodeID(blockID, "line", lineIdx)
		runID := NodeID(lineID, "run", 1)
		lines = append(lines, &Line{
			ID: lineID,
			Runs: []*Run{
				{ID: runID, Text: trimmed},
			},
		})
	}
	if len(lines) == 0 {
		return []*Block{}
	}
	blockID := NodeID(pageID, "block", 1)
	return []*Block{{
		ID:    blockID,
		Type:  BlockText,
		BBox:  mediaBox, // best approximation until the position-aware pass lands
		Lines: lines,
	}}
}
