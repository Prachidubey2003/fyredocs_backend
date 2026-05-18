package fonts

import (
	"strings"
	"testing"
)

func TestFindSubstitute_HelveticaWithinThreshold(t *testing.T) {
	// Plan §1.3 measurable: auto-substitution succeeds with cap-height
	// delta ≤ 0.5%. Helvetica → Arimo is ~0.28% and must pass.
	plan := FindSubstitute("Helvetica", 0)
	if plan.Result != ResultMappedInsideThreshold {
		t.Errorf("result = %q, want %q", plan.Result, ResultMappedInsideThreshold)
	}
	if plan.Substitute == nil || plan.Substitute.PSName != "Arimo" {
		t.Errorf("substitute = %v, want Arimo", plan.Substitute)
	}
	if plan.CapHeightDeltaPct > DefaultCapHeightThresholdPct {
		t.Errorf("delta %.4f exceeds threshold %.4f",
			plan.CapHeightDeltaPct, DefaultCapHeightThresholdPct)
	}
}

func TestFindSubstitute_AllHelveticaStylesPass(t *testing.T) {
	for _, name := range []string{
		"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Helvetica-BoldOblique",
	} {
		plan := FindSubstitute(name, 0)
		if plan.Result != ResultMappedInsideThreshold {
			t.Errorf("%s → %s (delta %.2f%%) — want inside-threshold result",
				name, plan.Result, plan.CapHeightDeltaPct)
		}
	}
}

func TestFindSubstitute_AllTimesStylesPass(t *testing.T) {
	for _, name := range []string{
		"Times-Roman", "Times-Bold", "Times-Italic", "Times-BoldItalic",
	} {
		plan := FindSubstitute(name, 0)
		if plan.Result != ResultMappedInsideThreshold {
			t.Errorf("%s → %s (delta %.2f%%) — want inside-threshold result",
				name, plan.Result, plan.CapHeightDeltaPct)
		}
	}
}

func TestFindSubstitute_CourierOutsideThresholdAtDefault(t *testing.T) {
	// Courier → Cousine cap-height delta is ~1.25%, above the 0.5% default.
	// The plan considers this "best available but flag for review".
	plan := FindSubstitute("Courier", 0)
	if plan.Result != ResultMappedOutsideThreshold {
		t.Errorf("Courier result = %q, want %q",
			plan.Result, ResultMappedOutsideThreshold)
	}
	if plan.Substitute == nil || plan.Substitute.PSName != "Cousine" {
		t.Errorf("Courier substitute = %v, want Cousine", plan.Substitute)
	}
	if plan.CapHeightDeltaPct <= DefaultCapHeightThresholdPct {
		t.Errorf("expected delta > %.4f, got %.4f",
			DefaultCapHeightThresholdPct, plan.CapHeightDeltaPct)
	}
}

func TestFindSubstitute_CourierInsideThresholdAtLooserBar(t *testing.T) {
	// Operators can tighten or loosen the threshold per workflow. With a
	// 2% bar, Courier → Cousine fits.
	plan := FindSubstitute("Courier", 2.0)
	if plan.Result != ResultMappedInsideThreshold {
		t.Errorf("at threshold=2.0, Courier result = %q, want %q",
			plan.Result, ResultMappedInsideThreshold)
	}
}

func TestFindSubstitute_NoSubstituteForGlyphSetFonts(t *testing.T) {
	for _, name := range []string{"Symbol", "ZapfDingbats"} {
		plan := FindSubstitute(name, 0)
		if plan.Result != ResultNoSubstituteAvailable {
			t.Errorf("%s result = %q, want %q",
				name, plan.Result, ResultNoSubstituteAvailable)
		}
		if plan.Substitute != nil {
			t.Errorf("%s should not propose a substitute; got %v",
				name, plan.Substitute)
		}
	}
}

func TestFindSubstitute_UnknownFont(t *testing.T) {
	plan := FindSubstitute("DefinitelyNotAFont", 0)
	if plan.Result != ResultUnknownFont {
		t.Errorf("unknown result = %q, want %q", plan.Result, ResultUnknownFont)
	}
	if plan.Original != nil {
		t.Errorf("unknown should have nil Original; got %v", plan.Original)
	}
}

func TestExactMatchPlan(t *testing.T) {
	plan := ExactMatchPlan("Helvetica")
	if plan.Result != ResultExactMatch {
		t.Errorf("result = %q, want %q", plan.Result, ResultExactMatch)
	}
	if plan.Original == nil || plan.Substitute == nil {
		t.Error("both Original and Substitute should be set (same pointer logically)")
	}
}

func TestPlan_StringFormatIsReadable(t *testing.T) {
	// We don't lock the format; we just want it to be non-empty and to
	// include the original name. This is a smoke test for log output.
	plan := FindSubstitute("Helvetica", 0)
	got := plan.String()
	if got == "" {
		t.Error("Plan.String returned empty")
	}
	if !strings.Contains(got, "Helvetica") {
		t.Errorf("Plan.String missing source font name: %q", got)
	}
}

func TestPlan_String_UnknownFont(t *testing.T) {
	plan := FindSubstitute("NotAFont", 0)
	got := plan.String()
	if got == "" || !strings.Contains(got, string(ResultUnknownFont)) {
		t.Errorf("unknown plan string: %q", got)
	}
}

func TestFindSubstitute_DefaultThresholdAppliedWhenZeroOrNegative(t *testing.T) {
	// thresholdPct = 0 means "use the default".
	plan := FindSubstitute("Helvetica", 0)
	if plan.ThresholdPct != DefaultCapHeightThresholdPct {
		t.Errorf("threshold = %.4f, want default %.4f",
			plan.ThresholdPct, DefaultCapHeightThresholdPct)
	}
	// Negative is also treated as "use default".
	plan = FindSubstitute("Helvetica", -1)
	if plan.ThresholdPct != DefaultCapHeightThresholdPct {
		t.Errorf("negative threshold not normalised; got %.4f",
			plan.ThresholdPct)
	}
}
