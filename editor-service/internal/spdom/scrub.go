package spdom

import (
	"bytes"
	"strconv"
)

// ScrubRect rewrites `stream` so that every text-show operator
// (Tj, ', ", TJ) whose baseline origin lies inside `rect` emits
// no glyphs. The string operand of a Tj/'/" is replaced with the
// empty literal `()` byte-for-byte; a TJ array is replaced with
// `[]`. Everything outside the targeted byte ranges is preserved
// verbatim — including the trailing operator keyword, surrounding
// whitespace, and the rest of the content stream.
//
// "Inside" is judged by the baseline origin: the (e, f) of the
// text matrix at the moment the operator runs. A text run that
// starts inside the rect is scrubbed atomically even if it extends
// past the right edge; a run that starts outside is left alone
// even if it crosses INTO the rect. Per-glyph partial-overlap
// scrubbing is a tracked follow-up (plan §5.5).
//
// Same orientation constraints as extractTextEvents: rotated,
// skewed, mirrored, or non-uniform-scaled text is silently left
// alone because we can't reliably localise it without proper
// rotation handling. v0's redact UX intentionally surfaces this
// limitation — partial coverage is documented at the translator
// layer (pdfedit.RedactArea).
//
// Returns the original slice if no operators match. Otherwise
// allocates a new buffer.
func ScrubRect(stream []byte, rect Rect) []byte {
	out, _ := ScrubRectChanged(stream, rect)
	return out
}

// ScrubRectChanged is ScrubRect with an explicit "did anything
// match" signal. Returns (stream, false) if no text-show op
// overlapped `rect`; (newBytes, true) otherwise. Callers (e.g.
// pdfedit.DeleteTextInRect) need this to surface a 400 when the
// caller's rect doesn't actually contain any text.
func ScrubRectChanged(stream []byte, rect Rect) ([]byte, bool) {
	if rect.X1 <= rect.X0 || rect.Y1 <= rect.Y0 {
		return stream, false
	}
	scrubs, _ := findScrubRanges(stream, rect)
	if len(scrubs) == 0 {
		return stream, false
	}
	return applyScrubs(stream, scrubs), true
}

// applyScrubs is the byte-splice that ScrubRect and ReplaceRect
// share — copy `stream` through, replacing each `[start, end)` byte
// range with the supplied bytes. Ranges are guaranteed
// non-overlapping + in-order by findScrubRanges. Returns the
// original slice when no ranges match.
func applyScrubs(stream []byte, scrubs []scrubRange) []byte {
	if len(scrubs) == 0 {
		return stream
	}
	var out bytes.Buffer
	out.Grow(len(stream))
	cursor := 0
	for _, r := range scrubs {
		out.Write(stream[cursor:r.start])
		out.Write(r.replacement)
		cursor = r.end
	}
	out.Write(stream[cursor:])
	return out.Bytes()
}

// ReplaceRectFirstAnchor is "ScrubRect + append a fresh BT block
// drawing `newText` at the first scrubbed op's anchor in its
// active font". The core primitive behind table.cell.edit and any
// future rect-targeted text-replacement op.
//
// The returned bytes contain the scrubbed prefix verbatim followed
// by a freshly-emitted block of the form:
//
//	BT
//	/<fontResource> <fontSize> Tf
//	<x> <y> Td
//	(<newText>) Tj
//	ET
//
// where `fontResource`, `fontSize`, `x`, `y` come from the first
// text-show op whose glyph rect overlapped `rect`. The PDF
// content-stream parser only needs the appended block at the END
// of the stream (no surrounding `q…Q` needed — nothing runs after
// it).
//
// ok=false means no text-show op overlapped `rect`. Callers should
// treat that as INVALID_INPUT for table.cell.edit.
//
// v0 limitations (tracked):
//   - Replacement is drawn horizontally regardless of the
//     original orientation. A cell whose text was 90°-rotated
//     gets a horizontal replacement.
//   - Font / size come from the FIRST overlapping op only —
//     mixed-font cells get a uniform replacement.
//   - No re-flow: if the new text is wider than the cell, it
//     extends past the right edge. Re-flow + multi-line cell
//     handling is the v1 follow-up.
func ReplaceRectFirstAnchor(stream []byte, rect Rect, newText string) (out []byte, ok bool) {
	if rect.X1 <= rect.X0 || rect.Y1 <= rect.Y0 {
		return stream, false
	}
	scrubs, first := findScrubRanges(stream, rect)
	if !first.found {
		return stream, false
	}
	scrubbed := applyScrubs(stream, scrubs)
	bt := buildReplacementBT(first, newText)
	merged := make([]byte, 0, len(scrubbed)+len(bt))
	merged = append(merged, scrubbed...)
	merged = append(merged, bt...)
	return merged, true
}

// ReplaceRectFirstAnchorWrapped is the multi-line counterpart of
// ReplaceRectFirstAnchor. Identical scrub behaviour; the
// replacement is wrapped to the cell's available width (rect.X1
// minus the captured anchor's x) and emitted as N stacked
// BT/ET blocks separated by `lineLeadingFactor * fontSize` in
// PDF Y units.
//
// Why a separate function vs. extending ReplaceRectFirstAnchor:
// the original primitive ships as a stable contract for any
// caller that needs single-line replacement (e.g. signature
// stamps, free-form text inserts). Wrapping changes the
// visible-text behaviour for the same inputs, so callers
// opt in by reaching for this function explicitly.
//
// Behaviour:
//
//   - Wrap target width is `rect.X1 - anchor.x`. If anchor.x
//     sits at the cell's left edge (the common case), this is
//     the cell's full width. If the first scrubbed op started
//     mid-cell (rare; usually means the cell was right-aligned
//     or had leading whitespace), the wrap target is narrower
//     and the replacement starts at the anchor.
//   - Line height defaults to 1.2 × fontSize when the caller
//     passes lineLeadingFactor <= 0. Matches typical body-text
//     leading; a follow-up could pull real leading from the
//     source TL operator when the cell carried one.
//   - Lines that fall below `rect.Y0` are STILL emitted (no
//     truncation). Visible overflow gives the caller a clear
//     signal that the cell isn't tall enough — silent
//     truncation would be a data-loss hazard.
//   - Rotated / scaled cells use the captured 2×2 Tm for line
//     N's matrix, with line N's y offset projected through the
//     same matrix so wrapped lines ride the cell's orientation.
//
// ok=false when no text-show op overlapped `rect`, mirroring
// ReplaceRectFirstAnchor.
func ReplaceRectFirstAnchorWrapped(stream []byte, rect Rect, newText string, lineLeadingFactor float64) (out []byte, ok bool) {
	if rect.X1 <= rect.X0 || rect.Y1 <= rect.Y0 {
		return stream, false
	}
	scrubs, first := findScrubRanges(stream, rect)
	if !first.found {
		return stream, false
	}
	scrubbed := applyScrubs(stream, scrubs)

	// Compute wrap target. The anchor's logical x in text-space
	// is 0 (BT resets Tm); rect.X1 - first.x is the right-side
	// distance in PAGE space. For an identity 2×2 cell, page
	// units == text-space units, so this is the wrap width in
	// the glyph-advance frame approxWidth speaks. For rotated
	// cells we'd need to map back — but rotated cells in the
	// non-identity path already use Tm form where text-space
	// advance follows the rotated baseline, so the same width
	// applies along that direction.
	maxWidth := rect.X1 - first.x
	if maxWidth <= 0 {
		// Defensive: an anchor sitting at or past the right
		// edge means the rect contained text that started
		// outside the cell. Fall back to single-line emit
		// (the v0 behaviour) so we don't degenerate to zero
		// lines.
		bt := buildReplacementBT(first, newText)
		merged := make([]byte, 0, len(scrubbed)+len(bt))
		merged = append(merged, scrubbed...)
		merged = append(merged, bt...)
		return merged, true
	}

	lines := WrapTextToWidth(newText, first.fontName, first.fontSize, maxWidth)
	if len(lines) == 0 {
		// Empty replacement → emit nothing past the scrub.
		// The cell is left empty, which is the caller's
		// "delete cell contents" path.
		return scrubbed, true
	}
	if len(lines) == 1 {
		// Fits on one line — use the existing single-line
		// emitter for byte-identical output with the
		// non-wrap path.
		bt := buildReplacementBT(first, lines[0])
		merged := make([]byte, 0, len(scrubbed)+len(bt))
		merged = append(merged, scrubbed...)
		merged = append(merged, bt...)
		return merged, true
	}

	leading := lineLeadingFactor
	if leading <= 0 {
		leading = 1.2
	}
	lineGap := leading * first.fontSize

	merged := make([]byte, 0, len(scrubbed)+len(lines)*64)
	merged = append(merged, scrubbed...)
	for i, line := range lines {
		// In text-space, line N starts at (0, -i·lineGap) —
		// PDF Y axis grows upward, so subsequent lines move
		// "down". Project through the captured 2×2 to map to
		// page-space deltas.
		dx := -float64(i) * lineGap * first.tmC
		dy := -float64(i) * lineGap * first.tmD
		anchor := firstScrubAnchor{
			found:    true,
			fontName: first.fontName,
			fontSize: first.fontSize,
			x:        first.x + dx,
			y:        first.y + dy,
			tmA:      first.tmA,
			tmB:      first.tmB,
			tmC:      first.tmC,
			tmD:      first.tmD,
		}
		merged = append(merged, buildReplacementBT(anchor, line)...)
	}
	return merged, true
}

// buildReplacementBT emits the content-stream snippet that draws
// `newText` at `anchor`. Uses Td form when the captured Tm 2×2 is
// the identity (horizontal cell, no scale); falls through to the
// full Tm form when it isn't — preserves rotation AND any
// uniform scale that was baked into the original cell.
//
// Why uniform scale matters even for "horizontal" cells: a
// content stream that says `/F1 12 Tf 2 0 0 2 100 100 Tm (X) Tj`
// renders X at 24pt because the Tm scales text-space by 2×. If
// we emitted Td (which inherits the prior text matrix from BT's
// identity reset), the replacement would render at the bare 12pt
// — half-size relative to the original. The Tm form re-applies
// the captured scale so the replacement matches.
func buildReplacementBT(anchor firstScrubAnchor, newText string) []byte {
	if isIdentity2x2(anchor.tmA, anchor.tmB, anchor.tmC, anchor.tmD) {
		return BuildTextBlock(anchor.fontName, anchor.fontSize, anchor.x, anchor.y, newText)
	}
	return BuildOrientedTextBlock(
		anchor.fontName, anchor.fontSize,
		anchor.tmA, anchor.tmB, anchor.tmC, anchor.tmD,
		anchor.x, anchor.y,
		newText,
	)
}

// isIdentity2x2 reports whether (a, b, c, d) is the identity
// matrix [[1, 0], [0, 1]]. Used to pick between the Td and Tm
// emit paths; we tolerate float-printing noise with a tight
// epsilon, but in practice the values come straight from a
// content-stream parse so they're exact when identity.
func isIdentity2x2(a, b, c, d float64) bool {
	const eps = 1e-9
	abs := func(v float64) float64 {
		if v < 0 {
			return -v
		}
		return v
	}
	return abs(a-1) < eps && abs(b) < eps && abs(c) < eps && abs(d-1) < eps
}

// BuildTextBlock returns the content-stream bytes that draw `text`
// at page-space (x, y) in the given font resource at `sizePt`
// points. The output is one fresh BT/ET pair with no inherited
// state, so it can be safely appended to any content stream.
//
// Format mirrors the PDF spec for an identity-orientation text run:
//
//	BT
//	/<font> <sizePt> Tf
//	<x> <y> Td
//	(<text>) Tj
//	ET
//
// `font` is the raw resource name (the operand of Tf in the source
// stream, e.g. "F1"). Caller is responsible for the resource
// existing in the page's /Resources/Font dict — if it doesn't, the
// PDF parses but the glyph subset is empty.
//
// `text` is escaped via encodeLiteral so PDF literal-string
// specials (parens, backslash) round-trip safely. Use this as the
// canonical "draw text" emitter for text.insert and the scrub +
// replace path (table.cell.edit) so both produce byte-identical
// blocks when the inputs match.
func BuildTextBlock(font string, sizePt, x, y float64, text string) []byte {
	var b bytes.Buffer
	b.WriteString("\nBT\n/")
	b.WriteString(font)
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(sizePt, 'f', -1, 64))
	b.WriteString(" Tf\n")
	b.WriteString(strconv.FormatFloat(x, 'f', -1, 64))
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(y, 'f', -1, 64))
	b.WriteString(" Td\n")
	b.Write(encodeLiteral(text))
	b.WriteString(" Tj\nET\n")
	return b.Bytes()
}

// BuildOrientedTextBlock is the oriented-rotation peer of
// BuildTextBlock. It emits the full Tm operator instead of Td so
// the replacement glyphs ride the same 2×2 transform as the
// original cell — rotation (90°/180°/270°), uniform scale, and
// orthogonal mirror all round-trip correctly. Used by
// table.cell.edit when the captured Tm 2×2 is non-identity.
//
// Format:
//
//	BT
//	/<font> <sizePt> Tf
//	<a> <b> <c> <d> <x> <y> Tm
//	(<text>) Tj
//	ET
//
// Caller responsibilities + escaping invariants mirror
// BuildTextBlock; this function is its strict superset for
// callers that have a full 2×2 in hand.
func BuildOrientedTextBlock(font string, sizePt, a, b, c, d, x, y float64, text string) []byte {
	var buf bytes.Buffer
	buf.WriteString("\nBT\n/")
	buf.WriteString(font)
	buf.WriteByte(' ')
	buf.WriteString(strconv.FormatFloat(sizePt, 'f', -1, 64))
	buf.WriteString(" Tf\n")
	buf.WriteString(strconv.FormatFloat(a, 'f', -1, 64))
	buf.WriteByte(' ')
	buf.WriteString(strconv.FormatFloat(b, 'f', -1, 64))
	buf.WriteByte(' ')
	buf.WriteString(strconv.FormatFloat(c, 'f', -1, 64))
	buf.WriteByte(' ')
	buf.WriteString(strconv.FormatFloat(d, 'f', -1, 64))
	buf.WriteByte(' ')
	buf.WriteString(strconv.FormatFloat(x, 'f', -1, 64))
	buf.WriteByte(' ')
	buf.WriteString(strconv.FormatFloat(y, 'f', -1, 64))
	buf.WriteString(" Tm\n")
	buf.Write(encodeLiteral(text))
	buf.WriteString(" Tj\nET\n")
	return buf.Bytes()
}

// scrubRange is a single byte-range replacement in a content stream.
type scrubRange struct {
	start, end  int    // inclusive-exclusive byte indices in the source
	replacement []byte // bytes to splice in (typically empty Tj/TJ operand)
}

// firstScrubAnchor captures the (font, size, anchor, Tm 2×2) of
// the FIRST text-show op whose glyph rect overlapped the target
// rect. ReplaceRectFirstAnchor uses these fields to draw the
// replacement text where the original cell content started — in
// the same orientation and scale as the original.
type firstScrubAnchor struct {
	found    bool
	fontName string  // resource name from Tf (e.g. "F1")
	fontSize float64 // bare Tf size, in points
	x, y     float64 // page-space anchor (tm[4], tm[5]) at the moment of the op
	// tmA, tmB, tmC, tmD are the 2×2 portion of the text matrix
	// at the moment of the op: [a b c d e f] becomes
	//   text-space (u, v) → page-space (a·u + c·v + x, b·u + d·v + y)
	// For an identity-orientation cell this is (1, 0, 0, 1) and
	// the replacement uses the simple Td form. For rotated or
	// uniformly-scaled cells the 2×2 is non-identity and the
	// replacement emits a full Tm so it matches orientation +
	// rendered size.
	tmA, tmB, tmC, tmD float64
}

// findScrubRanges walks the content stream, tracking the text-state
// machine, and returns a sorted list of byte-range replacements for
// every text-show op whose origin is inside `rect`. The second
// return value carries the (font, size, anchor) of the FIRST
// matching op — ScrubRect ignores it; ReplaceRectFirstAnchor uses
// it to position replacement text.
func findScrubRanges(stream []byte, rect Rect) ([]scrubRange, firstScrubAnchor) {
	// State machine — subset of extractTextEvents in layout.go,
	// plus per-token byte-range tracking for the operands.
	var inText bool
	var tm, tlm [6]float64
	identity := [6]float64{1, 0, 0, 1, 0, 0}
	tm, tlm = identity, identity
	var leading float64
	var fontName string
	var fontSize float64
	var charSpace, wordSpace float64

	// We re-implement the walkOps loop here instead of calling it,
	// because walkOps reuses its operand slice across calls — the
	// byte-range fields we just added would be stable, but the
	// callback would have to copy them out, and that's clunkier than
	// scanning inline.
	s := &scanner{src: stream}
	var operands []token
	var scrubs []scrubRange
	var first firstScrubAnchor

	// recordFirst stashes the (font, size, anchor) of the first
	// overlapping text-show op. Subsequent overlaps don't update
	// it — ReplaceRectFirstAnchor positions the replacement at the
	// cell's natural reading-order start.
	recordFirst := func() {
		if first.found {
			return
		}
		first = firstScrubAnchor{
			found:    true,
			fontName: fontName,
			fontSize: fontSize,
			x:        tm[4],
			y:        tm[5],
			tmA:      tm[0],
			tmB:      tm[1],
			tmC:      tm[2],
			tmD:      tm[3],
		}
	}

	// runOverlapsRect reports whether the rendered glyph bbox for
	// `text` (computed via the current text matrix) overlaps the
	// redact rect.
	//
	// The four corners of the text-space box [0, wText] × [0,
	// fontSize] are projected into page space through Tm — that's
	// `(a·x + c·y + e, b·x + d·y + f)` per ISO 32000-1 §8.3. We
	// then take the AABB of the four projected corners and do a
	// standard overlap test against the rect.
	//
	// "Any overlap" rather than "origin inside" is the correct
	// privacy posture for redaction: if any glyph touches the
	// rect, we scrub the whole op. Over-redaction is acceptable
	// (and standard in tools like Adobe); under-redaction would
	// leak text that the user explicitly asked to hide.
	//
	// Works for arbitrary Tm — identity, scaled, rotated,
	// skewed. Rotated/skewed text was previously left unscrubbed
	// (the overlay covered it visually but the bytes survived);
	// the new bbox math closes that leak.
	runOverlapsRect := func(text string) bool {
		wText := approxWidth(text, fontName, fontSize, charSpace, wordSpace)
		if wText <= 0 {
			// Empty / zero-width string: fall back to a
			// point-in-rect check on the baseline origin so a
			// degenerate Tj with no glyphs still gets scrubbed
			// when it's targeted.
			x, y := tm[4], tm[5]
			return x >= rect.X0 && x <= rect.X1 && y >= rect.Y0 && y <= rect.Y1
		}
		h := fontSize
		if h <= 0 {
			h = 1
		}
		// Four text-space corners: (0,0), (wText,0), (0,h), (wText,h).
		// Page = (a·x + c·y + e, b·x + d·y + f).
		project := func(x, y float64) (float64, float64) {
			return tm[0]*x + tm[2]*y + tm[4], tm[1]*x + tm[3]*y + tm[5]
		}
		cx, cy := [4]float64{}, [4]float64{}
		cx[0], cy[0] = project(0, 0)
		cx[1], cy[1] = project(wText, 0)
		cx[2], cy[2] = project(0, h)
		cx[3], cy[3] = project(wText, h)
		minX, maxX := cx[0], cx[0]
		minY, maxY := cy[0], cy[0]
		for i := 1; i < 4; i++ {
			if cx[i] < minX {
				minX = cx[i]
			}
			if cx[i] > maxX {
				maxX = cx[i]
			}
			if cy[i] < minY {
				minY = cy[i]
			}
			if cy[i] > maxY {
				maxY = cy[i]
			}
		}
		// AABB-vs-rect overlap. Boundary-inclusive (`<=` / `>=`):
		// the rect's edge is part of the redact area, so a glyph
		// resting exactly on the boundary counts as overlap. The
		// privacy-conservative posture — under-redacting at the
		// edge would leak text the user explicitly marked.
		return minX <= rect.X1 && maxX >= rect.X0 && minY <= rect.Y1 && maxY >= rect.Y0
	}

	// emptyTj is the canonical replacement bytes for a scrubbed
	// string operand. We keep the `(` and `)` so the operator's
	// operand is still well-formed; only the contents disappear.
	// (TJ array's `[ ]` delimiters always stay in place — v2
	// scrubs individual items inside the array, never the whole
	// array atomically.)
	emptyTj := []byte{'(', ')'}

	// advance updates Tm to where the next text-show op will land.
	// In text space, a Tj of width wText shifts the cursor by
	// (wText, 0); through Tm that becomes (wText·a, wText·b) in
	// page space — i.e., along whatever direction the rotated
	// baseline points.
	advance := func(text string) {
		wText := approxWidth(text, fontName, fontSize, charSpace, wordSpace)
		tm[4] += wText * tm[0]
		tm[5] += wText * tm[1]
	}

	for {
		s.skipWhitespace()
		if s.eof() {
			break
		}
		if s.peek() == '%' {
			s.skipTo('\n')
			continue
		}
		tok, ok := s.nextToken()
		if !ok {
			break
		}
		if tok.kind != tokOp {
			operands = append(operands, tok)
			continue
		}
		op := string(tok.bytes)
		switch op {
		case "BT":
			inText = true
			tm, tlm = identity, identity
		case "ET":
			inText = false
		case "Tf":
			if len(operands) >= 2 {
				if operands[0].kind == tokName {
					fontName = string(operands[0].bytes)
				}
				if operands[1].kind == tokNumber {
					fontSize, _ = strconv.ParseFloat(string(operands[1].bytes), 64)
				}
			}
		case "TL":
			if len(operands) >= 1 && operands[0].kind == tokNumber {
				leading, _ = strconv.ParseFloat(string(operands[0].bytes), 64)
			}
		case "Tc":
			if len(operands) >= 1 && operands[0].kind == tokNumber {
				charSpace, _ = strconv.ParseFloat(string(operands[0].bytes), 64)
			}
		case "Tw":
			if len(operands) >= 1 && operands[0].kind == tokNumber {
				wordSpace, _ = strconv.ParseFloat(string(operands[0].bytes), 64)
			}
		case "Td", "TD":
			if inText && len(operands) >= 2 {
				tx, ok1 := numberArg(operands[len(operands)-2])
				ty, ok2 := numberArg(operands[len(operands)-1])
				if ok1 && ok2 {
					tlm[4] += tx*tlm[0] + ty*tlm[2]
					tlm[5] += tx*tlm[1] + ty*tlm[3]
					tm = tlm
					if op == "TD" {
						leading = -ty
					}
				}
			}
		case "T*":
			if inText {
				tlm[4] += -leading * tlm[2]
				tlm[5] += -leading * tlm[3]
				tm = tlm
			}
		case "Tm":
			if len(operands) >= 6 {
				var ok bool
				var m [6]float64
				for i := 0; i < 6; i++ {
					m[i], ok = numberArg(operands[i])
					if !ok {
						break
					}
				}
				if ok {
					tm = m
					tlm = m
				}
			}
		case "Tj":
			// (text) Tj — operand[len-1] is the string literal.
			if len(operands) > 0 {
				last := operands[len(operands)-1]
				if last.kind == tokString {
					text := string(decodeLiteral(last.bytes))
					if runOverlapsRect(text) {
						recordFirst()
						scrubs = append(scrubs, scrubRange{
							start:       last.start,
							end:         last.end,
							replacement: emptyTj,
						})
						// Cleared: no advance.
					} else {
						advance(text)
					}
				}
			}
		case "'":
			// next-line + show: operand is the string literal.
			if inText {
				tlm[4] += -leading * tlm[2]
				tlm[5] += -leading * tlm[3]
				tm = tlm
			}
			if len(operands) > 0 {
				last := operands[len(operands)-1]
				if last.kind == tokString {
					text := string(decodeLiteral(last.bytes))
					if runOverlapsRect(text) {
						recordFirst()
						scrubs = append(scrubs, scrubRange{
							start:       last.start,
							end:         last.end,
							replacement: emptyTj,
						})
					} else {
						advance(text)
					}
				}
			}
		case `"`:
			// aw ac (text) " — aw/ac update spacing, then '.
			if len(operands) >= 3 {
				if v, ok := numberArg(operands[len(operands)-3]); ok {
					wordSpace = v
				}
				if v, ok := numberArg(operands[len(operands)-2]); ok {
					charSpace = v
				}
			}
			if inText {
				tlm[4] += -leading * tlm[2]
				tlm[5] += -leading * tlm[3]
				tm = tlm
			}
			if len(operands) > 0 {
				last := operands[len(operands)-1]
				if last.kind == tokString {
					text := string(decodeLiteral(last.bytes))
					if runOverlapsRect(text) {
						recordFirst()
						scrubs = append(scrubs, scrubRange{
							start:       last.start,
							end:         last.end,
							replacement: emptyTj,
						})
					} else {
						advance(text)
					}
				}
			}
		case "TJ":
			// [ (a) -100 (b) ] TJ — operand[len-1] is the array.
			//
			// v2 per-item surgery: walk each entry independently
			// and decide overlap-or-advance one string at a time.
			// The TJ array's `[ ]` delimiters stay in place; only
			// the byte ranges of overlapping `(...)` strings get
			// replaced with `()`. Numeric kerns are kept as-is and
			// applied to the running text matrix so subsequent
			// items land at the right cursor.
			//
			// Trade-off vs v1's "scrub the whole array on any
			// overlap": v2 preserves text the user didn't ask to
			// redact (privacy-tight but layout-preserving for
			// neighbours). Scrubbed strings still leave their
			// kerns in place — the surviving neighbours close
			// ranks, just as they do for an emptied plain Tj.
			if len(operands) > 0 {
				last := operands[len(operands)-1]
				if last.kind == tokArray {
					for _, item := range last.array {
						switch item.kind {
						case tokString:
							text := string(decodeLiteral(item.bytes))
							if runOverlapsRect(text) {
								recordFirst()
								scrubs = append(scrubs, scrubRange{
									start:       item.start,
									end:         item.end,
									replacement: emptyTj,
								})
								// Cleared: no advance.
							} else {
								advance(text)
							}
						case tokNumber:
							if v, err := strconv.ParseFloat(string(item.bytes), 64); err == nil {
								shift := v / 1000.0 * fontSize
								tm[4] -= shift * tm[0]
								tm[5] -= shift * tm[1]
							}
						}
					}
				}
			}
		}
		operands = operands[:0]
	}
	return scrubs, first
}
