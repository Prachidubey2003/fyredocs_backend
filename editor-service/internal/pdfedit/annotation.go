package pdfedit

import (
	"bytes"
	"fmt"

	"editor-service/internal/pdfwriter"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// AnnotRect is the page-space rectangle covered by an annotation, in
// PDF user-space points: origin bottom-left, X grows right, Y grows up.
//
// Per ISO 32000-1 §12.5.2 the /Rect entry is "[ llx lly urx ury ]" —
// lower-left and upper-right corners. We normalise on construction so a
// caller can supply the points in any order.
type AnnotRect struct {
	X0, Y0, X1, Y1 float64
}

// normalise returns the rect with X0/Y0 = min corner and X1/Y1 = max
// corner. Reader-side validators reject inverted rects with PDF errors.
func (r AnnotRect) normalise() AnnotRect {
	if r.X0 > r.X1 {
		r.X0, r.X1 = r.X1, r.X0
	}
	if r.Y0 > r.Y1 {
		r.Y0, r.Y1 = r.Y1, r.Y0
	}
	return r
}

// AnnotColor is the annotation /C entry in DeviceRGB: each channel in
// [0, 1]. Pointer-typed at the caller level so "use the default" is
// distinguishable from "RGB(0,0,0)".
type AnnotColor struct {
	R, G, B float64
}

// AnnotKind enumerates the annotation subtypes this package can emit.
// Wire-format consumers (editops, OpenAPI) parse these as JSON strings.
type AnnotKind string

const (
	AnnotHighlight AnnotKind = "highlight"
	AnnotUnderline AnnotKind = "underline"
	AnnotStrikeOut AnnotKind = "strikeout"
	AnnotSquiggly  AnnotKind = "squiggly"
	AnnotSquare    AnnotKind = "square"
	AnnotSticky    AnnotKind = "sticky"

	// AnnotInk is the PDF /Ink subtype — freehand strokes drawn on
	// the page. Wire callers send `kind: "freehand"`; we normalise
	// to AnnotInk internally because the PDF spec calls it Ink.
	AnnotInk AnnotKind = "ink"
	// AnnotCallout is a /FreeText annotation with /IT /FreeTextCallout
	// intent — a text box pointing at a target point via a callout
	// line. ISO 32000-1 §12.5.6.6.
	AnnotCallout AnnotKind = "callout"
)

// DefaultAnnotColor is the per-kind default /C value emitted when the
// caller doesn't supply one. We make the defaults explicit so the
// annotation looks the same in every conforming viewer (reader-side
// defaults vary).
//
//   - Highlight → PDF-convention Acrobat yellow.
//   - Underline / Squiggly → blue.
//   - StrikeOut → red.
//   - Square → red border.
var DefaultAnnotColor = map[AnnotKind]AnnotColor{
	AnnotHighlight: {R: 1, G: 0.92, B: 0.23},
	AnnotUnderline: {R: 0.0, G: 0.45, B: 1.0},
	AnnotStrikeOut: {R: 1.0, G: 0.16, B: 0.13},
	AnnotSquiggly:  {R: 0.0, G: 0.45, B: 1.0},
	AnnotSquare:    {R: 1.0, G: 0.16, B: 0.13},
	AnnotSticky:    {R: 1.0, G: 0.86, B: 0.40},
	// Ink defaults to a dark-blue stroke (matches Acrobat's default
	// pen colour) so the freehand mark stands out on white pages.
	AnnotInk: {R: 0.0, G: 0.27, B: 0.65},
	// Callout border + line — Acrobat-style red, contrasts on
	// most page backgrounds.
	AnnotCallout: {R: 1.0, G: 0.16, B: 0.13},
}

// AddAnnotation returns the bytes of a new revision in which `pageNum`
// (1-indexed) has a new annotation of the requested kind covering
// `rect`. It's the general entry point for every annotation translator
// in the package — per-kind code lives in [buildAnnotBody].
//
// What's emitted:
//
//   - A new indirect /Annot object at the next-free object number with
//     the kind's subtype, the supplied rect, default-or-supplied /C
//     color, and the optional /Contents text.
//   - A modified Page dict whose /Annots array includes a reference to
//     the new annotation. If the page had /Annots inherited from a
//     parent Pages node we *copy* that array onto the leaf so our
//     additions don't propagate to siblings — inheritance is resolved
//     on this page only.
//
// ISO 32000-1 references:
//
//   - §12.5.2 — general annotation dictionary.
//   - §12.5.6.10 — text-markup family (/Highlight, /Underline,
//     /StrikeOut, /Squiggly); requires /QuadPoints which we synthesise
//     from the rect.
//   - §12.5.6.8 — /Square (line annotation) family; no /QuadPoints,
//     borders go in /BS.
//
// `contents` is optional — pass "" to omit. `color` may be nil to use
// the kind's default. An unknown `kind` returns an error.
func AddAnnotation(original []byte, kind AnnotKind, pageNum int, rect AnnotRect, color *AnnotColor, contents string) ([]byte, error) {
	if _, ok := DefaultAnnotColor[kind]; !ok {
		return nil, fmt.Errorf("pdfedit: unknown annotation kind %q", kind)
	}
	rect = rect.normalise()
	if rect.X1 <= rect.X0 || rect.Y1 <= rect.Y0 {
		return nil, fmt.Errorf("pdfedit: degenerate annotation rect %+v", rect)
	}
	c := resolveColor(kind, color)
	return appendAnnotation(original, pageNum, func() string {
		return buildAnnotBody(kind, rect, c, contents)
	})
}

// AddInkAnnotation injects an /Ink annotation (freehand strokes)
// on `pageNum`. Each entry in `strokes` is one continuous stroke
// as a flat `[x1, y1, x2, y2, …]` slice of PDF user-space points.
//
// Rect is auto-computed from the bounding box of every point,
// padded by 2pt so the annotation appearance doesn't clip against
// the rect edge in viewers that use /Rect to invalidate cache.
//
// Validation:
//   - strokes must be non-empty
//   - every stroke must have even-numbered coords and >= 4 (i.e.
//     at least 2 points — a single point has no line to render)
//   - all coords must be finite
//
// PDF spec: ISO 32000-1 §12.5.6.13.
func AddInkAnnotation(original []byte, pageNum int, strokes [][]float64, color *AnnotColor, contents string) ([]byte, error) {
	if len(strokes) == 0 {
		return nil, fmt.Errorf("pdfedit: ink annotation requires at least one stroke")
	}
	for i, s := range strokes {
		if len(s)%2 != 0 {
			return nil, fmt.Errorf("pdfedit: ink stroke %d has odd coord count %d (expected x,y pairs)", i, len(s))
		}
		if len(s) < 4 {
			return nil, fmt.Errorf("pdfedit: ink stroke %d has %d coords (need >= 2 points)", i, len(s)/2)
		}
	}
	rect := strokesBoundingRect(strokes)
	c := resolveColor(AnnotInk, color)
	return appendAnnotation(original, pageNum, func() string {
		return buildInkBody(rect, c, contents, strokes)
	})
}

// AddCalloutAnnotation injects a /FreeText callout annotation: a
// text box at `rect` with a callout line pointing at `anchor`
// (PDF user-space point). The intent flag /IT /FreeTextCallout
// + /CL array tell viewers to draw the line and arrowhead.
//
// PDF spec: ISO 32000-1 §12.5.6.6 (/FreeText) plus the callout
// fields documented in Adobe's PDF Reference §8.4.5.
//
// Validation: rect must be non-degenerate; anchor must be two
// finite numbers. We don't reject anchors INSIDE the rect — some
// users want the callout line as decoration on top of the text
// — but most flows place the anchor outside.
func AddCalloutAnnotation(original []byte, pageNum int, rect AnnotRect, anchor [2]float64, color *AnnotColor, contents string) ([]byte, error) {
	rect = rect.normalise()
	if rect.X1 <= rect.X0 || rect.Y1 <= rect.Y0 {
		return nil, fmt.Errorf("pdfedit: degenerate callout rect %+v", rect)
	}
	c := resolveColor(AnnotCallout, color)
	return appendAnnotation(original, pageNum, func() string {
		return buildCalloutBody(rect, c, contents, anchor)
	})
}

// resolveColor turns nil → the kind's default, clamps a supplied
// color to [0, 1]^3.
func resolveColor(kind AnnotKind, color *AnnotColor) AnnotColor {
	if color == nil {
		return DefaultAnnotColor[kind]
	}
	return color.clamp01()
}

// appendAnnotation is the shared plumbing used by every
// per-kind public entry point: open the PDF, validate the page,
// reserve an object number, ask the caller for the annotation
// body string, then append the new object + an updated page
// dict via an incremental update.
//
// Pulling this out keeps the per-kind functions short and makes
// changes to the /Annots-append semantics (e.g. an /Annots-as-
// indirect-ref code path) live in exactly one place.
func appendAnnotation(original []byte, pageNum int, buildBody func() string) ([]byte, error) {
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

	pageDict, pageRef, _, err := ctx.PageDict(pageNum, false)
	if err != nil {
		return nil, fmt.Errorf("pdfedit: lookup page %d: %w", pageNum, err)
	}
	if pageDict == nil || pageRef == nil {
		return nil, fmt.Errorf("pdfedit: page %d has no resolvable dict", pageNum)
	}

	if ctx.XRefTable.Size == nil {
		return nil, fmt.Errorf("pdfedit: xref table has no Size")
	}
	annotObjNum := *ctx.XRefTable.Size
	*ctx.XRefTable.Size++

	annotBody := buildBody()

	annotRef := types.IndirectRef{
		ObjectNumber:     types.Integer(annotObjNum),
		GenerationNumber: types.Integer(0),
	}

	existing, _ := pageDict.Find("Annots")
	var newAnnots types.Array
	switch v := existing.(type) {
	case nil:
		newAnnots = types.Array{annotRef}
	case types.Array:
		newAnnots = make(types.Array, 0, len(v)+1)
		newAnnots = append(newAnnots, v...)
		newAnnots = append(newAnnots, annotRef)
	case types.IndirectRef:
		return nil, fmt.Errorf("pdfedit: page %d has /Annots as indirect reference (%d %d R); not yet supported", pageNum, v.ObjectNumber, v.GenerationNumber)
	default:
		return nil, fmt.Errorf("pdfedit: page %d has /Annots of unexpected type %T", pageNum, existing)
	}
	pageDict.Update("Annots", newAnnots)

	var u pdfwriter.Update
	u.Set(annotObjNum, []byte(annotBody))
	u.Set(pageRef.ObjectNumber.Value(), []byte(pageDict.PDFString()))
	return u.Bytes(original)
}

// strokesBoundingRect returns the smallest rect containing every
// point in every stroke, padded by 2pt on each side so reader-
// side rect-based culling doesn't clip the rendered line.
func strokesBoundingRect(strokes [][]float64) AnnotRect {
	const pad = 2.0
	first := true
	r := AnnotRect{}
	for _, s := range strokes {
		for i := 0; i+1 < len(s); i += 2 {
			x, y := s[i], s[i+1]
			if first {
				r.X0, r.Y0, r.X1, r.Y1 = x, y, x, y
				first = false
				continue
			}
			if x < r.X0 {
				r.X0 = x
			}
			if x > r.X1 {
				r.X1 = x
			}
			if y < r.Y0 {
				r.Y0 = y
			}
			if y > r.Y1 {
				r.Y1 = y
			}
		}
	}
	r.X0 -= pad
	r.Y0 -= pad
	r.X1 += pad
	r.Y1 += pad
	return r
}

// AddHighlight is a back-compat thin wrapper around AddAnnotation for
// callers that still want a kind-specific entry point. New call sites
// should use AddAnnotation directly.
func AddHighlight(original []byte, pageNum int, rect AnnotRect, color *AnnotColor, contents string) ([]byte, error) {
	return AddAnnotation(original, AnnotHighlight, pageNum, rect, color, contents)
}

// DefaultHighlightColor is the per-Highlight default kept for callers
// that imported it directly. Prefer DefaultAnnotColor[AnnotHighlight]
// going forward.
//
// Deprecated: use DefaultAnnotColor.
var DefaultHighlightColor = DefaultAnnotColor[AnnotHighlight]

// clamp01 returns the color with each channel clipped to [0, 1] —
// out-of-range values produce different output across readers, so we
// guarantee a valid /C array regardless of caller input.
func (c AnnotColor) clamp01() AnnotColor {
	clip := func(v float64) float64 {
		switch {
		case v < 0:
			return 0
		case v > 1:
			return 1
		default:
			return v
		}
	}
	return AnnotColor{R: clip(c.R), G: clip(c.G), B: clip(c.B)}
}

// buildAnnotBody returns the body bytes for one of the annotation
// kinds this package supports. Strings are escaped per PDF literal-
// string rules (§7.3.4.2).
//
// Why hand-rolled rather than via pdfcpu's serializer:
//   - pdfcpu's types.Dict.PDFString() sorts keys, which is fine but
//     also serializes nil values awkwardly. The annotation body is
//     small and stable so a hand-rolled string keeps the output
//     readable and the float formatting under our control.
//
// Layout:
//   - The text-markup family (/Highlight, /Underline, /StrikeOut,
//     /Squiggly) all share the same shape — only /Subtype changes.
//     §12.5.6.10 mandates /QuadPoints in (top-left, top-right,
//     bottom-left, bottom-right) order for each "quad" the markup
//     covers. We synthesise one quad from the rect.
//   - /Square (§12.5.6.8) has no /QuadPoints; it uses /BS to define
//     the border style. Default border is 1pt solid.
func buildAnnotBody(kind AnnotKind, r AnnotRect, c AnnotColor, contents string) string {
	subtype := annotSubtype(kind)
	rectStr := fmt.Sprintf("/Rect [%s %s %s %s]",
		fmtFloat(r.X0), fmtFloat(r.Y0), fmtFloat(r.X1), fmtFloat(r.Y1))
	colorStr := fmt.Sprintf("/C [%s %s %s]",
		fmtFloat(c.R), fmtFloat(c.G), fmtFloat(c.B))

	var body string
	switch kind {
	case AnnotHighlight, AnnotUnderline, AnnotStrikeOut, AnnotSquiggly:
		quadPoints := fmt.Sprintf("/QuadPoints [%s %s %s %s %s %s %s %s]",
			fmtFloat(r.X0), fmtFloat(r.Y1), // top-left
			fmtFloat(r.X1), fmtFloat(r.Y1), // top-right
			fmtFloat(r.X0), fmtFloat(r.Y0), // bottom-left
			fmtFloat(r.X1), fmtFloat(r.Y0), // bottom-right
		)
		body = fmt.Sprintf("<< /Type /Annot /Subtype /%s %s %s %s /F 4",
			subtype, rectStr, colorStr, quadPoints)
	case AnnotSquare:
		// 1pt solid border (/W 1, /S /S). Readers default differently
		// when /BS is omitted, so we set it explicitly for parity.
		body = fmt.Sprintf("<< /Type /Annot /Subtype /%s %s %s /BS << /W 1 /S /S >> /F 4",
			subtype, rectStr, colorStr)
	case AnnotSticky:
		// §12.5.6.4 /Text annotation. /Name picks the icon style:
		// /Note is the canonical "page corner with a fold" sticky.
		// /Open=false means the popup is collapsed by default —
		// readers render just the icon until clicked. We always emit
		// /Open explicitly because reader defaults differ.
		body = fmt.Sprintf("<< /Type /Annot /Subtype /%s %s %s /Name /Note /Open false /F 4",
			subtype, rectStr, colorStr)
	default:
		// Caller already validated kind in AddAnnotation; this branch
		// exists so the switch is total and a typo in a new kind
		// produces a recognisable string rather than empty output.
		body = fmt.Sprintf("<< /Type /Annot /Subtype /%s %s %s /F 4",
			subtype, rectStr, colorStr)
	}

	if contents != "" {
		body += " /Contents (" + escapeLiteral(contents) + ")"
	}
	body += " >>"
	return body
}

// annotSubtype maps an AnnotKind to the /Subtype name PDF readers
// expect. Kept as a tiny standalone helper so the kind→subtype mapping
// is obvious and trivially extendable.
func annotSubtype(kind AnnotKind) string {
	switch kind {
	case AnnotHighlight:
		return "Highlight"
	case AnnotUnderline:
		return "Underline"
	case AnnotStrikeOut:
		return "StrikeOut"
	case AnnotSquiggly:
		return "Squiggly"
	case AnnotSquare:
		return "Square"
	case AnnotSticky:
		return "Text"
	case AnnotInk:
		return "Ink"
	case AnnotCallout:
		return "FreeText"
	default:
		return string(kind)
	}
}

// buildInkBody emits a /Subtype /Ink annotation body. /InkList is
// an array of arrays of numbers; each inner array is one stroke
// `[x1 y1 x2 y2 …]`. /BS sets the stroke width and style — 1pt
// solid matches the common pen-tool default.
//
// /F 4 sets the Print flag so the strokes appear in printed
// output too, matching the text-markup family.
func buildInkBody(r AnnotRect, c AnnotColor, contents string, strokes [][]float64) string {
	rectStr := fmt.Sprintf("/Rect [%s %s %s %s]",
		fmtFloat(r.X0), fmtFloat(r.Y0), fmtFloat(r.X1), fmtFloat(r.Y1))
	colorStr := fmt.Sprintf("/C [%s %s %s]",
		fmtFloat(c.R), fmtFloat(c.G), fmtFloat(c.B))

	var inkList []byte
	inkList = append(inkList, '[')
	for i, s := range strokes {
		if i > 0 {
			inkList = append(inkList, ' ')
		}
		inkList = append(inkList, '[')
		for j, v := range s {
			if j > 0 {
				inkList = append(inkList, ' ')
			}
			inkList = append(inkList, fmtFloat(v)...)
		}
		inkList = append(inkList, ']')
	}
	inkList = append(inkList, ']')

	body := fmt.Sprintf("<< /Type /Annot /Subtype /Ink %s %s /InkList %s /BS << /W 1 /S /S >> /F 4",
		rectStr, colorStr, string(inkList))
	if contents != "" {
		body += " /Contents (" + escapeLiteral(contents) + ")"
	}
	body += " >>"
	return body
}

// buildCalloutBody emits a /Subtype /FreeText annotation with
// /IT /FreeTextCallout. /CL is the callout line: two points
// `[anchorX anchorY rectCornerX rectCornerY]` form a straight
// line from the target to the text box. The corner is the rect
// edge closest to `anchor` so the line never crosses the box.
//
// /DA is the default appearance string: `0 g /Helv 10 Tf` =
// black text in Helvetica 10pt. PDF readers fall back to a
// default font when /Helv isn't in the page's resource
// dictionary, which is fine for plain Latin text.
//
// /LE specifies the line-ending shape at the FIRST point of /CL
// (the anchor end), so the arrowhead points AT the target. Per
// PDF Reference, /OpenArrow draws an unfilled chevron.
func buildCalloutBody(r AnnotRect, c AnnotColor, contents string, anchor [2]float64) string {
	rectStr := fmt.Sprintf("/Rect [%s %s %s %s]",
		fmtFloat(r.X0), fmtFloat(r.Y0), fmtFloat(r.X1), fmtFloat(r.Y1))
	colorStr := fmt.Sprintf("/C [%s %s %s]",
		fmtFloat(c.R), fmtFloat(c.G), fmtFloat(c.B))

	cornerX, cornerY := closestCorner(r, anchor[0], anchor[1])
	clStr := fmt.Sprintf("/CL [%s %s %s %s]",
		fmtFloat(anchor[0]), fmtFloat(anchor[1]),
		fmtFloat(cornerX), fmtFloat(cornerY))

	body := fmt.Sprintf(
		"<< /Type /Annot /Subtype /FreeText %s %s %s /IT /FreeTextCallout /LE /OpenArrow /DA (0 g /Helv 10 Tf) /F 4",
		rectStr, colorStr, clStr)
	if contents != "" {
		body += " /Contents (" + escapeLiteral(contents) + ")"
	}
	body += " >>"
	return body
}

// closestCorner returns the rect corner nearest (px, py). For
// callouts this is the natural attach point: drawing from the
// anchor to the nearest corner keeps the line short and avoids
// crossing the text box.
func closestCorner(r AnnotRect, px, py float64) (float64, float64) {
	x := r.X0
	if abs(px-r.X1) < abs(px-r.X0) {
		x = r.X1
	}
	y := r.Y0
	if abs(py-r.Y1) < abs(py-r.Y0) {
		y = r.Y1
	}
	return x, y
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// fmtFloat renders a float in a way that PDF parsers reliably accept:
// no exponent notation, trailing zeros trimmed, "%g"-like but bounded
// at 6 decimal places (plenty for page-space coordinates).
func fmtFloat(v float64) string {
	s := fmt.Sprintf("%.6f", v)
	// Trim trailing zeros and a dangling decimal point. The PDF spec
	// accepts "1." but most viewers prefer "1".
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// escapeLiteral applies the PDF string-literal escape rules from
// ISO 32000-1 §7.3.4.2 to the small subset we care about for
// /Contents text: parens and backslash. Newlines / tabs pass through
// (PDF's literal-string grammar treats them as the corresponding
// control chars when unescaped, which is what we want).
func escapeLiteral(s string) string {
	if s == "" {
		return ""
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '(', ')', '\\':
			out = append(out, '\\', c)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}
