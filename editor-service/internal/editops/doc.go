// Package editops dispatches sPDOM operations from the editor's wire
// format (JSON {type, …}) to the typed translators in
// [editor-service/internal/pdfedit], stacking them onto one input PDF
// to produce one output revision.
//
// This is the seam between the HTTP layer and the PDF-mutation
// primitives. The HTTP handler does the easy bits (auth, JSON binding,
// DB persistence, response envelope); this package owns the harder
// part: validating each op's shape, dispatching to the right
// translator, deciding whether a multi-op request maps to one revision
// or many.
//
// v0 scope:
//
//   - One op per request. Multi-op requests are accepted at the JSON
//     boundary (the wire format always carries an array) but only the
//     single-op case is implemented today; multiple ops in one request
//     would currently produce one incremental section per op, which is
//     semantically *fine* (each section is a valid revision and the
//     final state is correct) but operationally weird for callers
//     expecting one-revision-per-call. So we reject `len(ops) != 1`
//     with [ErrOnlyOneOpSupported]. The "atomic multi-op revision"
//     design lands when text.replace + annotation.add need it.
//   - Only [OpType.PageRotate]. Other op types parse and emit
//     [ErrUnknownOp]. As each translator lands (text.replace,
//     annotation.add, …) it gets a case here and the corresponding
//     parsing/validation helper.
//
// Why a separate package rather than handler-local code:
//
//   - It's unit-testable without a DB, without gin, without spawning
//     a process. Tests here drive only bytes-in/bytes-out.
//   - It's the natural place for the multi-op atomicity design once
//     more than one translator lands.
//   - Keeps the HTTP handler small enough to read in one screen.
package editops
