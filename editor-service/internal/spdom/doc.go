// Package spdom implements the Semantic PDF Document Object Model — the
// editor's L4 abstraction over a PDF, described in product plan §5.2.
//
// The four-layer architecture (top-down):
//
//	L4 — sPDOM:        Document → Page → Block → Line → Run → Glyph
//	L3 — Layout:       blocks, columns, lines, baselines, reading order
//	L2 — Object Model: parsed content streams (text ops, image ops, paths)
//	L1 — Bytes:        xref, objects, fonts, incremental updates
//
// This package owns the **L4 data types** and the public `Parse` entrypoint.
// Internally it walks from L1 (via pdfcpu) up to L4. Each layer is a
// well-defined pass with a tested contract:
//
//   - L1 → L2: pdfcpu parses the PDF byte stream into pages + content
//     streams. (Currently the only layer that touches the byte format.)
//   - L2 → L3: cluster glyphs into runs, runs into lines, lines into
//     blocks, blocks into columns; assign a reading order. **Not yet
//     implemented** — Phase 1 follow-up. The data types are in place so
//     the layout pass can be implemented without changing the API.
//   - L3 → L4: emit sPDOM JSON consumed by the editor frontend and the
//     future collab-service indexer.
//
// Stable IDs (plan §5.4): every node has an ID derived from its parent ID
// + ordinal position so re-parsing the same PDF returns the same tree IDs,
// and the frontend can address nodes consistently across reloads. We use
// UUIDv5 with a fixed namespace; the formula is documented at NodeID().
package spdom
