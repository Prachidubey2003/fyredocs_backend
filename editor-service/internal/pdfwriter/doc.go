// Package pdfwriter emits PDF *incremental updates* per ISO 32000-1 §7.5.6.
//
// Why incremental updates, not a full rewrite:
//
//   - Preserves the original bytes byte-for-byte. Any prior signature
//     (PAdES, AcroForm sig) that covers the first revision stays valid;
//     re-emitting the whole file would invalidate it.
//   - Cheap to compute and small on disk — every revision appends only
//     the objects it changed plus a fresh xref + trailer.
//   - Naturally enables git-style version history: each revision is a
//     byte range, and reading at a /Prev pointer reconstructs that
//     historical state.
//
// What an incremental update looks like on disk:
//
//	<original PDF bytes — unchanged, including its trailer + %%EOF>
//	<new or replacement objects, each "N G obj … endobj">
//	xref
//	  0 1
//	  0000000000 65535 f
//	  <subsection per updated object>
//	trailer
//	  << /Size <total-objects> /Root <ref> /Prev <prev-xref-offset> /Info <ref?> >>
//	startxref
//	  <byte offset of the "xref" line above>
//	%%EOF
//
// The /Prev pointer chains this revision's xref back to the previous
// revision's xref, so a reader that opens the file finds every object —
// the ones we touched here, plus everything inherited from prior bytes.
//
// What this package does:
//
//   - Discover (from the bytes of the prior revision) the values needed
//     to seed the next update: previous startxref, /Size, /Root, /Info.
//     See [Discover].
//   - Take a set of (objNum, body) replacements/insertions and emit the
//     appended bytes that the caller concatenates onto the prior bytes.
//     See [Update] and [Update.Bytes].
//
// What this package deliberately does NOT do (yet):
//
//   - Parse object bodies. The caller supplies raw "<< … >>" or stream
//     bytes. This keeps the package small and focused on the cross-ref
//     layer. The sPDOM-op → object-mutation layer lives elsewhere.
//   - Emit xref *streams* on output (ISO 32000-1 §7.5.8). We READ both
//     classic xref tables and stream-form xref objects on input (see
//     [parseXrefStreamDict] in reader.go), and the spec explicitly
//     allows an incremental update to use either form regardless of
//     what the prior revisions used — the reader follows /Prev through
//     both transparently. So we always *emit* the simpler classic
//     table. The roadmap follow-up is to emit stream-form sections too
//     when the input used them, so producer parity is exact end to end.
//   - Compress streams, deduplicate objects, or garbage-collect. Those
//     are L0/L1 optimisations that arrive with the Rust rewrite later.
//
// Boundary with the broader pipeline:
//
//	sPDOM op (text.replace, page.rotate, …)
//	   ↓ — handled in editor-service/internal/edits (TODO, next pass)
//	object-level mutation set (objNum → new body)
//	   ↓ — this package
//	appended bytes
//	   ↓ — written to revisions/{rev_id}.delta under /files/ (§4.4.3)
//
// Round-trip invariant (enforced by tests): for an empty update set,
// Bytes(original) returns a file whose object graph is byte-identical to
// the original's, with an extra empty xref + trailer at the end. The
// PDF is still openable.
package pdfwriter
