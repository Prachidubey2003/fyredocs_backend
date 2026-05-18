package fonts

import (
	"math"
	"strings"
)

// Style enumerates the typographic style axes that matter for substitution.
type Style string

const (
	StyleRegular     Style = "regular"
	StyleBold        Style = "bold"
	StyleItalic      Style = "italic"
	StyleBoldItalic  Style = "bold-italic"
	StyleOblique     Style = "oblique"      // synonymous with italic for sans/mono
	StyleBoldOblique Style = "bold-oblique" // synonymous with bold-italic
)

// Origin records where the font definition came from. Affects substitution
// decisions: a `PDFCore` font is always renderable in any PDF reader, so
// the writer can fall back to it without embedding anything.
type Origin string

const (
	// OriginPDFCore — one of the 14 standard PDF base fonts. Every
	// conformant reader has them. Never needs embedding.
	OriginPDFCore Origin = "pdf-core"
	// OriginOpenSource — license permits embedding subsets without per-doc
	// licensing. Used as the preferred substitute target.
	OriginOpenSource Origin = "open-source"
	// OriginProprietary — embedding may require a runtime license check.
	// Reserved for the Phase 5 licensed-font store.
	OriginProprietary Origin = "proprietary"
)

// License records the redistribution terms of the font files (NOT the
// substitution-table metadata, which lives in this repo).
type License string

const (
	LicensePDFCore     License = "pdf-core"   // packaged with every PDF reader
	LicenseApache2     License = "apache-2.0" // Croscore (Arimo, Tinos, Cousine)
	LicenseOFL         License = "ofl-1.1"
	LicenseLGPL        License = "lgpl-2.1"
	LicenseProprietary License = "proprietary"
	LicenseUnknown     License = "unknown"
)

// Font is one record in the catalog. PSName is the canonical key — every
// PDF embeds fonts by their PostScript name (e.g. "Helvetica-BoldOblique").
type Font struct {
	// PSName is the canonical, case-sensitive PostScript name.
	PSName string
	// Family is the typographic family ("Helvetica", "Times", etc.). Used
	// for substitution: if the exact PSName isn't found, the family is the
	// first fallback key.
	Family  string
	Style   Style
	Origin  Origin
	License License
	// CapHeight1000 is the cap-height in font units at a 1000-em scale.
	// Used to validate that a candidate substitute is metrics-equivalent
	// (see CapHeightDeltaPct + the plan §1.3 ≤ 0.5% threshold).
	CapHeight1000 int
	// Substitutes is an ordered list of PSNames to try when this font's
	// glyph set is insufficient for a new edit. Highest-fidelity first.
	// May be empty (e.g., for Symbol / ZapfDingbats, which have no clean
	// open substitute — the writer must keep the original font).
	Substitutes []string
}

// Lookup returns the catalog entry for a PostScript name, or nil if none.
// Case-sensitive: PDF PostScript names are case-sensitive per ISO 32000-1.
func Lookup(psName string) *Font {
	psName = strings.TrimSpace(psName)
	if psName == "" {
		return nil
	}
	if f, ok := registry[psName]; ok {
		return &f
	}
	return nil
}

// Names returns every catalog PostScript name. Order is unspecified;
// callers should sort if they need deterministic output. Useful for tests
// and for the documentation generator.
func Names() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}

// LookupByFamilyStyle is the family-name fallback path. Used when a
// document references a font by family name (some authoring tools do this)
// rather than the canonical PostScript name. Returns the closest matching
// entry, or nil.
func LookupByFamilyStyle(family string, style Style) *Font {
	family = strings.TrimSpace(family)
	if family == "" {
		return nil
	}
	wantStyle := normalizeStyle(style)
	for _, f := range registry {
		if !strings.EqualFold(f.Family, family) {
			continue
		}
		if normalizeStyle(f.Style) == wantStyle {
			return &f
		}
	}
	// Fall back to the regular weight of the family.
	for _, f := range registry {
		if strings.EqualFold(f.Family, family) && f.Style == StyleRegular {
			return &f
		}
	}
	return nil
}

// normalizeStyle treats Oblique as Italic for substitution purposes —
// most sans-serif and monospace families use Oblique while serif families
// use Italic; for the lookup table it's the same axis.
func normalizeStyle(s Style) Style {
	switch s {
	case StyleOblique:
		return StyleItalic
	case StyleBoldOblique:
		return StyleBoldItalic
	default:
		return s
	}
}

// CapHeightDeltaPct returns |a − b| / a as a percentage. Used to validate
// substitution candidates against the plan §1.3 threshold (auto-substitution
// passes when the delta is ≤ 0.5%).
//
// Returns 100.0 when either side is zero or unknown — that's a "fail safely"
// behaviour for catalog entries with missing metrics (Symbol / ZapfDingbats).
func CapHeightDeltaPct(a, b int) float64 {
	if a <= 0 || b <= 0 {
		return 100.0
	}
	return math.Abs(float64(a-b)) / float64(a) * 100.0
}
