package spdom

import (
	"github.com/pdfcpu/pdfcpu/pkg/font"
)

// WrapTextToWidth breaks `text` into lines such that each line's
// rendered width at `(fontName, sizePt)` is at most `maxWidth`
// points. Greedy first-fit on whitespace boundaries — the
// simplest algorithm that produces correct, deterministic
// output for typical Latin body text.
//
// Semantics:
//
//   - Whitespace-delimited words are the atomic unit. A word
//     that on its own exceeds `maxWidth` is emitted on its own
//     line — we never break mid-word. Mid-word hyphenation
//     would change the rendered text and break copy/paste,
//     which is a worse failure than visible overflow.
//   - Existing newlines in the input are honoured as hard
//     breaks. A line that already fits is emitted as-is.
//   - `maxWidth <= 0` collapses to a single line containing
//     the whole text — same shape as the v0 EditTableCell
//     non-wrap path.
//   - Empty input → empty slice.
//
// The space character's advance contributes to the running
// line width, so a single trailing space at the end of a line
// is allowed to overflow imperceptibly (the alternative —
// re-fitting on trailing whitespace — adds complexity for no
// rendering benefit).
func WrapTextToWidth(text, fontName string, sizePt, maxWidth float64) []string {
	if text == "" {
		return nil
	}
	// Honour hard newlines first — each is its own wrapping
	// pass — so callers passing multi-paragraph strings see
	// their paragraph breaks preserved.
	var out []string
	for _, paragraph := range splitOnNewlines(text) {
		out = append(out, wrapParagraph(paragraph, fontName, sizePt, maxWidth)...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func wrapParagraph(paragraph, fontName string, sizePt, maxWidth float64) []string {
	if paragraph == "" {
		return []string{""}
	}
	if maxWidth <= 0 {
		return []string{paragraph}
	}
	words := splitWhitespace(paragraph)
	if len(words) == 0 {
		return []string{""}
	}

	var lines []string
	var current string
	currentWidth := 0.0
	spaceAdvance := CharAdvance(fontName, ' ', sizePt)

	for _, w := range words {
		wWidth := wordWidth(w, fontName, sizePt)
		switch {
		case current == "":
			// First word on the line — always accept, even
			// if it overflows alone. We never mid-word
			// break.
			current = w
			currentWidth = wWidth
		case currentWidth+spaceAdvance+wWidth <= maxWidth:
			current += " " + w
			currentWidth += spaceAdvance + wWidth
		default:
			lines = append(lines, current)
			current = w
			currentWidth = wWidth
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

// wordWidth sums per-rune advances for `w` at the given font
// + size. No spacing arguments — callers in this file always
// measure word-internal widths in clean text-state (charSpace
// and wordSpace at 0).
func wordWidth(w, fontName string, sizePt float64) float64 {
	total := 0.0
	for _, r := range w {
		total += CharAdvance(fontName, r, sizePt)
	}
	return total
}

func splitOnNewlines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func splitWhitespace(s string) []string {
	var out []string
	start := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		isWS := c == ' ' || c == '\t' || c == '\r'
		switch {
		case !isWS && start < 0:
			start = i
		case isWS && start >= 0:
			out = append(out, s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

// CharAdvance returns the horizontal advance for a single rune
// when rendered at `sizePt` points in `fontName`.
//
// For any of the 14 PDF core fonts (Helvetica family, Times
// family, Courier family, Symbol, ZapfDingbats) we resolve the
// per-glyph width from pdfcpu's bundled AFM tables. Each AFM
// entry is in 1/1000 em — scale by `sizePt / 1000` to get
// user-space points.
//
// For unknown fonts we fall back to the historical 0.5em-per-
// glyph average. Real-world PDFs frequently use subset fonts
// with opaque names like `F1`, and the parser doesn't yet
// resolve the page's /Font resource dict (a follow-up; see
// resolveCanonicalFontName); the fallback keeps layout extraction
// usable on those PDFs at slightly lower fidelity.
//
// Returns 0 for size <= 0; the multiplicative downstream math
// expects "width contribution of one glyph" so a zero falls out
// cleanly when callers chain through approxWidth.
func CharAdvance(fontName string, r rune, sizePt float64) float64 {
	if sizePt <= 0 {
		return 0
	}
	if fontName != "" && font.IsCoreFont(fontName) {
		w := font.CharWidth(fontName, r)
		return float64(w) / 1000.0 * sizePt
	}
	return 0.5 * sizePt
}
