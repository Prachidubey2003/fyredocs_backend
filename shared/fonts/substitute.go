package fonts

import "fmt"

// DefaultCapHeightThresholdPct is the plan §1.3 acceptance bar for
// auto-substitution: the candidate's cap-height must differ from the
// original by no more than this percentage.
const DefaultCapHeightThresholdPct = 0.5

// Result encodes how a substitution was decided.
type Result string

const (
	// ResultExactMatch — the original font is in the catalog and is
	// renderable on its own; no substitution needed.
	ResultExactMatch Result = "exact-match"

	// ResultMappedInsideThreshold — original has a known substitute and
	// the substitute's cap-height is within the threshold (plan §1.3
	// "auto-substitution succeeds in ≥ 95% of edits"). Writer applies
	// without further confirmation.
	ResultMappedInsideThreshold Result = "mapped-inside-threshold"

	// ResultMappedOutsideThreshold — a substitute exists but its
	// cap-height exceeds the threshold. The writer should still use it
	// (it's the best-available open match) but surface a warning to the
	// UI so the user can confirm. Today this fires for Courier → Cousine.
	ResultMappedOutsideThreshold Result = "mapped-outside-threshold"

	// ResultUnknownFont — original PostScript name is not in the catalog.
	// The writer must either embed the original glyphs (if available on
	// disk) or refuse the edit and surface a clear error.
	ResultUnknownFont Result = "unknown-font"

	// ResultNoSubstituteAvailable — original is known but has no listed
	// substitute (Symbol, ZapfDingbats). Writer must keep the original.
	ResultNoSubstituteAvailable Result = "no-substitute-available"
)

// Plan is the output of a substitution lookup — what the writer should do
// when an edit requires glyphs not present in the original embedded subset.
type Plan struct {
	Original          *Font   // the font referenced by the document
	Substitute        *Font   // the chosen substitute, nil if none
	Result            Result  // outcome of the decision
	CapHeightDeltaPct float64 // |orig - sub| / orig as %; 0 if no substitute
	ThresholdPct      float64 // the threshold applied
}

// String is for human-readable telemetry / logs.
func (p Plan) String() string {
	if p.Substitute == nil {
		return fmt.Sprintf("font=%v result=%s", originalName(p.Original), p.Result)
	}
	return fmt.Sprintf("font=%s -> sub=%s result=%s delta=%.2f%% threshold=%.2f%%",
		originalName(p.Original), p.Substitute.PSName, p.Result, p.CapHeightDeltaPct, p.ThresholdPct)
}

// FindSubstitute looks up `psName` and decides how the writer should
// satisfy an edit that needs new glyphs.
//
// Use DefaultCapHeightThresholdPct for the threshold unless the caller
// has a reason to be stricter or more lenient.
func FindSubstitute(psName string, thresholdPct float64) Plan {
	if thresholdPct <= 0 {
		thresholdPct = DefaultCapHeightThresholdPct
	}

	original := Lookup(psName)
	if original == nil {
		return Plan{Original: nil, Result: ResultUnknownFont, ThresholdPct: thresholdPct}
	}

	// PDF-core fonts can always be rendered as-is by any conformant reader
	// — no substitution required. We still return the entry so callers
	// can short-circuit (and so the Plan is uniform).
	if original.Origin == OriginPDFCore && len(original.Substitutes) == 0 {
		return Plan{
			Original:     original,
			Result:       ResultNoSubstituteAvailable,
			ThresholdPct: thresholdPct,
		}
	}

	if len(original.Substitutes) == 0 {
		return Plan{
			Original:     original,
			Result:       ResultNoSubstituteAvailable,
			ThresholdPct: thresholdPct,
		}
	}

	for _, candidateName := range original.Substitutes {
		candidate := Lookup(candidateName)
		if candidate == nil {
			continue
		}
		delta := CapHeightDeltaPct(original.CapHeight1000, candidate.CapHeight1000)
		if delta <= thresholdPct {
			return Plan{
				Original:          original,
				Substitute:        candidate,
				Result:            ResultMappedInsideThreshold,
				CapHeightDeltaPct: delta,
				ThresholdPct:      thresholdPct,
			}
		}
	}

	// Nothing within threshold — return the first listed substitute (best
	// available) and let the caller decide whether to confirm with the user.
	first := Lookup(original.Substitutes[0])
	if first == nil {
		return Plan{Original: original, Result: ResultNoSubstituteAvailable, ThresholdPct: thresholdPct}
	}
	return Plan{
		Original:          original,
		Substitute:        first,
		Result:            ResultMappedOutsideThreshold,
		CapHeightDeltaPct: CapHeightDeltaPct(original.CapHeight1000, first.CapHeight1000),
		ThresholdPct:      thresholdPct,
	}
}

// ExactMatchPlan is the result returned for a font that already lives in
// the catalog and can be used as-is (no substitution needed). Callers use
// this when an edit's glyphs ARE in the original's embedded subset — the
// substitution path is a fallback, not the default.
func ExactMatchPlan(psName string) Plan {
	f := Lookup(psName)
	return Plan{
		Original:     f,
		Substitute:   f,
		Result:       ResultExactMatch,
		ThresholdPct: DefaultCapHeightThresholdPct,
	}
}

func originalName(f *Font) string {
	if f == nil {
		return "<unknown>"
	}
	return f.PSName
}
