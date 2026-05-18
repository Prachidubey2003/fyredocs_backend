# Font registry & substitution

When an edit introduces glyphs the embedded font subset doesn't cover, the writer needs a fallback that preserves visual fidelity. The font registry is the data-and-rules layer that decides the fallback.

This document is the operator's and integrator's reference for [`shared/fonts/`](../../../shared/fonts/). It pairs with plan §1.3 ("Font reconstruction") and §5.8 ("Font handling"), and is consumed by editor-service today and by the future Rust incremental writer.

## What's in the package

| File | Purpose |
|---|---|
| [`doc.go`](../../../shared/fonts/doc.go) | Package overview, in-scope and out-of-scope. |
| [`registry.go`](../../../shared/fonts/registry.go) | `Font`, `Style`, `Origin`, `License` types; `Lookup`, `LookupByFamilyStyle`, `Names`, `CapHeightDeltaPct`. |
| [`data.go`](../../../shared/fonts/data.go) | Static catalog: PDF-14 base fonts + Croscore (Arimo, Tinos, Cousine) substitutes. |
| [`substitute.go`](../../../shared/fonts/substitute.go) | `FindSubstitute(psName, threshold)` returns a `Plan` describing what the writer should do. |
| `*_test.go` | 20+ tests including the plan §1.3 acceptance criterion. |

## The catalog

26 entries today:

- **14 PDF-core fonts.** `OriginPDFCore`. Every conformant PDF reader has them; the writer never needs to embed bytes. Helvetica family (4 styles), Times (4), Courier (4), Symbol, ZapfDingbats.
- **12 Croscore substitutes.** `OriginOpenSource`, Apache 2.0. Arimo (4) / Tinos (4) / Cousine (4) — the canonical metrics-compatible drop-ins for Helvetica / Times / Courier.

Symbol and ZapfDingbats have no listed substitutes — they're glyph-set fonts and no open mapping preserves their visual identity. The writer keeps them as-is.

## How substitution decides

The plan §1.3 measurable: **auto-substitution must succeed with cap-height delta ≤ 0.5% in ≥ 95% of edits.**

Given a PostScript name and a threshold, `FindSubstitute` returns one of five results:

| `Result` | When it fires | What the writer does |
|---|---|---|
| `exact-match` | The original font is in the catalog and renderable as-is. | Use the original. |
| `mapped-inside-threshold` | A substitute exists and cap-height delta ≤ threshold. | Use the substitute without confirmation. |
| `mapped-outside-threshold` | A substitute exists but delta > threshold. | Use the substitute; surface a UI warning. |
| `unknown-font` | The original isn't in the catalog. | Either embed the original glyphs (if available) or refuse the edit and surface a clear error. |
| `no-substitute-available` | The original is known but has no listed substitute (Symbol, ZapfDingbats). | Keep the original. |

## Delta table at a glance

| Original (PDF-14) | Substitute | Cap-height Δ | Default result |
|---|---|---|---|
| Helvetica family (4 styles) | Arimo family | ≈ 0.28 % | inside-threshold ✓ |
| Times family (4 styles) | Tinos family | ≈ 0.30 % | inside-threshold ✓ |
| Courier family (4 styles) | Cousine family | ≈ 1.25 % | **outside-threshold** (best-available; flag for review) |
| Symbol, ZapfDingbats | — | — | no-substitute-available |

Of the 14 standard fonts, **12 of 14 (86%) pass at the 0.5% default** and **2 of 14 (14%) need a UI confirmation** (Symbol + ZapfDingbats are no-substitute cases that aren't really "substitution decisions" — they pass through unchanged). The four Courier styles fall outside the threshold and prompt for review. That's roughly the plan §1.3 acceptance frame for the PDF-14 universe; the production validation runs over the actual customer-font distribution.

Operators who want a wider auto-accept band (e.g., for batch automated workflows) can pass a higher threshold to `FindSubstitute(name, 2.0)` and Courier flips to inside-threshold.

## What this layer does NOT do

- It does not load font *bytes* at runtime. The catalog is metadata only. The Rust writer / mobile renderer / future licensed-font store handle the bytes.
- It does not provide per-glyph widths (AFM tables). Justified-text reflow lives at the layout layer; only metric-equivalent class is recorded here.
- It does not do CJK / RTL / Arabic shaping. Tracked for Phase 5 enterprise tier.
- It does not parse `/ToUnicode` cmaps from in-the-wild PDFs. That's an L2.5 sPDOM concern (see [EDITOR_SERVICE.md](../services/EDITOR_SERVICE.md)).

## Adding a font

1. Add an entry to the `registry` map in [`data.go`](../../../shared/fonts/data.go) with `PSName`, `Family`, `Style`, `Origin`, `License`, `CapHeight1000`, and an ordered `Substitutes` list.
2. If the new font is a *substitute* for an existing entry, add its `PSName` to that entry's `Substitutes` list (highest-fidelity first).
3. The test guard `TestRegistry_SubstituteTargetsExist` will fail at CI time if you list a typo in `Substitutes` that doesn't resolve. The plan §1.3 acceptance bar is enforced by `TestFindSubstitute_AllHelveticaStylesPass` and its siblings — extend them when you add a new family.

Cap-heights for any new entry should be sourced from a canonical AFM-equivalent file; commit the source in the surrounding comment.

## Cross-references

- [Plan §1.3 and §5.8](../../../../../../.claude/plans/i-want-you-to-keen-orbit.md) — the strategic intent.
- [EDITOR_SERVICE.md](../services/EDITOR_SERVICE.md) — primary consumer of `Lookup` (canonicalizes sPDOM Run.Font) and `FindSubstitute` (will be called by the future edit op-handler).
- [STORAGE.md](STORAGE.md) — where embedded-font byte storage will live when the licensed-font store lands in Phase 5.
