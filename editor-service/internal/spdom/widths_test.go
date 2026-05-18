package spdom

import (
	"testing"
)

func TestCharAdvance_ZeroOrNegativeSize(t *testing.T) {
	if got := CharAdvance("Helvetica", 'a', 0); got != 0 {
		t.Errorf("CharAdvance(_, _, 0) = %v, want 0", got)
	}
	if got := CharAdvance("Helvetica", 'a', -5); got != 0 {
		t.Errorf("CharAdvance(_, _, -5) = %v, want 0", got)
	}
}

func TestCharAdvance_CoreFontUsesAFMWidths(t *testing.T) {
	// Helvetica is one of the 14 PDF core fonts. The AFM widths for
	// 'i' (a narrow glyph) and 'M' (a wide glyph) should diverge from
	// each other AND from the 0.5em fallback (6.0 at size 12).
	const size = 12.0
	wi := CharAdvance("Helvetica", 'i', size)
	wM := CharAdvance("Helvetica", 'M', size)
	if wM <= wi {
		t.Errorf("AFM widths: M=%.3f, i=%.3f — expected M > i", wM, wi)
	}
	const fallback = 0.5 * size
	if wi == fallback || wM == fallback {
		// Not strict — but the chance that either 'i' or 'M' lands
		// exactly on the 500/1000-em fallback is negligible.
		t.Errorf("AFM widths suspiciously identical to fallback %.3f: i=%.3f M=%.3f",
			fallback, wi, wM)
	}
}

func TestCharAdvance_UnknownFontFallsBack(t *testing.T) {
	// Subset font names ("F1", "TT2", "AAAAAA+Helvetica") are NOT
	// recognised as core fonts and must hit the 0.5em fallback.
	cases := []string{"", "F1", "TT2", "AAAAAA+Helvetica", "MyCustomFont"}
	const size = 10.0
	const want = 0.5 * size
	for _, font := range cases {
		got := CharAdvance(font, 'x', size)
		if got != want {
			t.Errorf("CharAdvance(%q, 'x', %v) = %v, want %v (fallback)",
				font, size, got, want)
		}
	}
}

func TestCharAdvance_ScalesLinearlyWithSize(t *testing.T) {
	// Doubling size doubles the advance (linear in points).
	a := CharAdvance("Helvetica", 'M', 10)
	b := CharAdvance("Helvetica", 'M', 20)
	if b != 2*a {
		t.Errorf("expected 2× scaling: CharAdvance(M, 10)=%.3f, CharAdvance(M, 20)=%.3f", a, b)
	}
}

func TestStripSubsetTag(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"BCDEEE+Helvetica", "Helvetica"},
		{"AAAAAA+Times-Roman", "Times-Roman"},
		{"ZZZZZZ+", "ZZZZZZ+"}, // empty body is not a real PDF construct; leave untouched
		{"Helvetica", "Helvetica"},
		{"abc+def", "abc+def"},          // lowercase letters → not a subset tag
		{"ABCDE+Helvetica", "ABCDE+Helvetica"}, // only 5 letters → not a subset tag
		{"ABCDEF1Helvetica", "ABCDEF1Helvetica"}, // no '+' at position 6
		{"", ""},
		{"AB", "AB"},
	}
	for _, tc := range cases {
		if got := stripSubsetTag(tc.in); got != tc.want {
			t.Errorf("stripSubsetTag(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExtractTextEvents_ResolvesFontMapToCanonicalName(t *testing.T) {
	// Resource name F1 → /Helvetica via the page's font map. The
	// emitted textEvent should carry the canonical name so downstream
	// AFM-width lookups hit pdfcpu's core-font tables.
	stream := []byte("BT /F1 12 Tf 72 720 Td (Hi) Tj ET")
	fontMap := map[string]string{"F1": "Helvetica"}
	events, supported := extractTextEvents(stream, fontMap)
	if !supported {
		t.Fatal("supported should be true")
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].fontName != "Helvetica" {
		t.Errorf("fontName = %q, want Helvetica (resource-name resolution)", events[0].fontName)
	}
}

func TestExtractTextEvents_UnresolvedFontKeepsResourceName(t *testing.T) {
	// No map entry for F1 — extractor preserves the raw resource name.
	// approxWidth then falls back to 0.5em-per-glyph for unknown fonts.
	stream := []byte("BT /F1 12 Tf 72 720 Td (Hi) Tj ET")
	events, supported := extractTextEvents(stream, map[string]string{"F2": "Times-Roman"})
	if !supported {
		t.Fatal("supported should be true")
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].fontName != "F1" {
		t.Errorf("fontName = %q, want F1 (unresolved fallback)", events[0].fontName)
	}
}

func TestApproxWidth_DivergesBetweenFallbackAndCoreFont(t *testing.T) {
	// Same text + size, different resolution paths:
	// - "" (no font name) → fallback (0.5em per glyph)
	// - "Helvetica" → real AFM widths
	// The two should produce different totals for a string with
	// varied glyph widths.
	const text = "Mliii"
	const size = 12.0
	fallback := approxWidth(text, "", size, 0, 0)
	core := approxWidth(text, "Helvetica", size, 0, 0)
	if fallback == core {
		t.Errorf("expected divergence: fallback=%.3f core=%.3f", fallback, core)
	}
}
