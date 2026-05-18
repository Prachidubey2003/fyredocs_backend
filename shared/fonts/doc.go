// Package fonts is the editor's font catalog and substitution registry.
//
// It is a pure, side-effect-free data + lookup utility (per CLAUDE.md §2
// allowed shared utilities). Consumers:
//
//   - editor-service uses it to canonicalize fonts referenced by parsed
//     sPDOM Runs and to plan substitutions when an edit introduces glyphs
//     not present in the original embedded subset.
//   - the future Rust incremental PDF writer reads the substitution table
//     to decide which font to embed when a write op needs glyphs the
//     existing font doesn't have.
//   - workers (organize-pdf, optimize-pdf) can use Lookup to validate that
//     a font referenced by a document is known before performing
//     destructive ops.
//
// What's in scope today:
//
//   - The 14 standard PDF base fonts (Helvetica/Times/Courier × four
//     styles + Symbol + ZapfDingbats) as `Origin == PDFCore`.
//   - Metrics-equivalent open-source substitutes (Croscore family —
//     Arimo, Tinos, Cousine — Apache 2.0) ranked highest-fidelity-first.
//   - Cap-height at 1000em recorded for every font so the editor can
//     validate the plan §1.3 acceptance criterion ("auto-substitution
//     succeeds with cap-height delta ≤ 0.5% in ≥ 95% of edits").
//
// What's out of scope (deliberately deferred):
//
//   - Loading user-supplied OpenType/TTF files at runtime. The data here
//     is metadata only; actual font bytes are obtained by the writer
//     from disk / OS / a Phase 5 licensed-font store.
//   - Full Adobe-Font-Metrics (AFM) per-glyph width tables — needed for
//     justified-text reflow but not for substitution decisions.
//   - CJK / RTL / Arabic shaping. Phase 5 enterprise tier.
package fonts
