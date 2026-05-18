package fonts

import (
	"strings"
	"testing"
)

func TestLookup_PDF14_AllPresent(t *testing.T) {
	// Every one of the 14 standard PDF fonts must resolve.
	for _, name := range []string{
		"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique",
		"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic",
		"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique",
		"Symbol", "ZapfDingbats",
	} {
		f := Lookup(name)
		if f == nil {
			t.Errorf("PDF-14 font missing from catalog: %q", name)
			continue
		}
		if f.Origin != OriginPDFCore {
			t.Errorf("%s: Origin = %q, want pdf-core", name, f.Origin)
		}
	}
}

func TestLookup_Trimmed(t *testing.T) {
	if f := Lookup("  Helvetica  "); f == nil || f.PSName != "Helvetica" {
		t.Errorf("Lookup should trim whitespace; got %v", f)
	}
}

func TestLookup_CaseSensitive(t *testing.T) {
	// PDF PostScript names are case-sensitive per ISO 32000-1.
	if f := Lookup("helvetica"); f != nil {
		t.Errorf("Lookup should be case-sensitive; lowercase resolved to %v", f)
	}
}

func TestLookup_Empty(t *testing.T) {
	if Lookup("") != nil || Lookup("   ") != nil {
		t.Error("Lookup should return nil for empty/whitespace input")
	}
}

func TestLookup_UnknownReturnsNil(t *testing.T) {
	if Lookup("Made-Up-Font-2025") != nil {
		t.Error("Lookup should return nil for unknown font")
	}
}

func TestNames_NonEmpty(t *testing.T) {
	all := Names()
	if len(all) < 14 {
		t.Errorf("Names() returned %d, want at least 14 (PDF base fonts)", len(all))
	}
	seen := map[string]bool{}
	for _, n := range all {
		if seen[n] {
			t.Errorf("Names() returned duplicate: %q", n)
		}
		seen[n] = true
	}
}

func TestLookupByFamilyStyle_Helvetica(t *testing.T) {
	f := LookupByFamilyStyle("Helvetica", StyleBold)
	if f == nil || f.PSName != "Helvetica-Bold" {
		t.Errorf("Helvetica/Bold: got %v, want Helvetica-Bold", f)
	}
}

func TestLookupByFamilyStyle_ObliqueNormalizesToItalic(t *testing.T) {
	// Times has Italic; if a doc asks for Times/Oblique we should map to Italic.
	f := LookupByFamilyStyle("Times", StyleOblique)
	if f == nil {
		t.Fatal("Times/Oblique should fall through to Italic family member")
	}
	if !strings.Contains(f.PSName, "Italic") {
		t.Errorf("Times/Oblique resolved to %q, want a *Italic font", f.PSName)
	}
}

func TestLookupByFamilyStyle_FallsBackToRegular(t *testing.T) {
	// Symbol only has a regular style — asking for Symbol/Bold should fall
	// back to plain Symbol rather than returning nil.
	f := LookupByFamilyStyle("Symbol", StyleBold)
	if f == nil || f.PSName != "Symbol" {
		t.Errorf("Symbol/Bold fallback: got %v, want plain Symbol", f)
	}
}

func TestLookupByFamilyStyle_UnknownFamily(t *testing.T) {
	if f := LookupByFamilyStyle("MyMadeUpFont", StyleRegular); f != nil {
		t.Errorf("unknown family should return nil; got %v", f)
	}
}

func TestLookupByFamilyStyle_EmptyInput(t *testing.T) {
	if LookupByFamilyStyle("", StyleRegular) != nil {
		t.Error("empty family should return nil")
	}
}

func TestCapHeightDeltaPct(t *testing.T) {
	tests := []struct {
		a, b      int
		wantClose float64 // tolerance: 0.01
	}{
		{718, 716, 0.28}, // Helvetica → Arimo
		{662, 660, 0.30}, // Times → Tinos
		{562, 555, 1.25}, // Courier → Cousine
		{500, 500, 0.0},  // identical
		{0, 500, 100.0},  // a == 0 → fail-safe
		{500, 0, 100.0},  // b == 0 → fail-safe
		{-1, 500, 100.0}, // negative → fail-safe
	}
	for _, tc := range tests {
		got := CapHeightDeltaPct(tc.a, tc.b)
		if got < tc.wantClose-0.05 || got > tc.wantClose+0.05 {
			t.Errorf("CapHeightDeltaPct(%d,%d) = %.4f, want ≈ %.4f",
				tc.a, tc.b, got, tc.wantClose)
		}
	}
}

// TestPDF14_HaveOrNoNeedSubstitutes encodes the contract that every
// substitute-needing PDF-14 family has a target listed, and the glyph-set
// fonts (Symbol/ZapfDingbats) explicitly have none. A regression here
// would silently break the writer's fallback path.
func TestPDF14_HaveOrNoNeedSubstitutes(t *testing.T) {
	needsSubstitute := []string{
		"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique",
		"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic",
		"Courier", "Courier-Bold", "Courier-Oblique", "Courier-BoldOblique",
	}
	for _, name := range needsSubstitute {
		f := Lookup(name)
		if f == nil {
			t.Fatalf("%s missing", name)
		}
		if len(f.Substitutes) == 0 {
			t.Errorf("%s should declare at least one substitute", name)
		}
	}
	for _, name := range []string{"Symbol", "ZapfDingbats"} {
		f := Lookup(name)
		if f == nil {
			t.Fatalf("%s missing", name)
		}
		if len(f.Substitutes) != 0 {
			t.Errorf("%s should have no substitutes (glyph-set font); got %v",
				name, f.Substitutes)
		}
	}
}

// TestRegistry_SubstituteTargetsExist guards against a typo in the
// Substitutes list pointing at a PSName that doesn't exist.
func TestRegistry_SubstituteTargetsExist(t *testing.T) {
	for name, f := range registry {
		for _, sub := range f.Substitutes {
			if Lookup(sub) == nil {
				t.Errorf("%s lists unknown substitute %q", name, sub)
			}
		}
	}
}
