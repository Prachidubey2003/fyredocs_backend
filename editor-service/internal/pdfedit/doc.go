// Package pdfedit translates high-level document operations
// ("rotate page 3 to 90°", "replace text run R7 with …") into the
// (objNum → object body) mutations that `internal/pdfwriter` knows how
// to append as an incremental update.
//
// Layering:
//
//	caller (HTTP handler / sPDOM op queue)
//	   ↓ — calls one of the typed ops in this package
//	pdfedit.<Op>(originalBytes, args…) → []byte
//	   ↓ — looks up the affected object via pdfcpu,
//	       rewrites its dictionary, calls into pdfwriter
//	pdfwriter.Update.Set + Bytes
//	   ↓ — appends incremental bytes
//	new revision bytes
//
// What lives here:
//
//   - One exported function per sPDOM op. v0 ships [RotatePage] only;
//     [SetMetadata], text-run replacement, annotation-add, redact-apply,
//     etc. land in follow-ups as the sPDOM op contract is wired into
//     the HTTP handler.
//   - Object-body synthesis helpers (serialize a pdfcpu types.Dict to
//     the bytes that go between `N 0 obj` and `endobj`).
//
// What does NOT live here:
//
//   - The xref / trailer / startxref bookkeeping — that's [pdfwriter].
//   - Persistence of revisions, idempotency keys, audit-event emission,
//     or signed-URL generation — those are HTTP handler concerns.
//   - Content-stream rewriting (replacing the bytes between BT … ET on
//     a page). Several ops will need this later (text.replace,
//     annotation.add when it injects an /Annot reference array onto a
//     page that previously had no /Annots key); they'll get added when
//     the op queue exists to drive them.
package pdfedit
