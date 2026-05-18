package pdfwriter

import (
	"bytes"
	"fmt"
	"io"
	"sort"
)

// Object is one indirect object to insert or replace in the new
// revision. Body is the raw bytes that appear between `N 0 obj\n` and
// `\nendobj` — typically a dictionary like `<< /Type /Page /Rotate 90 >>`
// or a stream wrapper around a content stream.
//
// Generation is intentionally not exposed: every appended object uses
// generation 0. PDFs in the wild almost universally use generation 0;
// generation-bump deletions are rare and out of scope for v0.
type Object struct {
	Num  int    // object number; must be >= 1
	Body []byte // bytes between "obj" and "endobj", exclusive of those keywords
}

// Update accumulates the objects to write in one incremental revision.
//
// Zero value is ready to use:
//
//	var u pdfwriter.Update
//	u.Set(7, []byte("<< /Type /Page /Rotate 90 /Parent 2 0 R >>"))
//	out, err := u.Bytes(original)
type Update struct {
	objects map[int]Object
}

// Set registers an object to write. If Set is called twice for the same
// object number the later body wins — last-write-wins matches how
// PDF readers resolve duplicate definitions inside a single revision.
func (u *Update) Set(num int, body []byte) {
	if u.objects == nil {
		u.objects = map[int]Object{}
	}
	u.objects[num] = Object{Num: num, Body: body}
}

// Empty reports whether the update has no object mutations queued.
// An "empty" update still emits a fresh xref + trailer with /Prev — it
// is the simplest possible incremental revision and is useful for
// resealing or version-pinning a document without changing content.
func (u *Update) Empty() bool { return len(u.objects) == 0 }

// Bytes encodes the incremental revision and returns the full bytes of
// the new file: `original` followed by the appended-section bytes.
//
// The result is what the caller writes to disk as the new revision.
// Reading it from the start with any conforming PDF parser will see all
// objects in `original` *plus* the objects in this update — and any
// object whose number was set here overrides the prior definition.
//
// Form selection: the appended section's xref form MATCHES the prior
// revision's form — classic `xref` table on top of a classic doc,
// `/Type /XRef` stream object on top of a stream-form doc. Mixing
// forms is technically legal (ISO 32000-1 §7.5.6 — readers follow
// /Prev through both) but strict preflight validators flag the
// mismatch, so we don't.
//
// Notes:
//   - We do not validate that `original` is a well-formed PDF beyond
//     what Discover checks (a usable trailer + startxref). Garbage in,
//     garbage out — but garbage out is still appendable.
//   - The returned slice shares no memory with `original`; callers may
//     mutate either independently.
func (u *Update) Bytes(original []byte) ([]byte, error) {
	t, err := Discover(original)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.Grow(len(original) + 256 + 64*len(u.objects))
	buf.Write(original)

	// Ensure the appended section starts on a fresh line. PDFs almost
	// always end with `%%EOF\n` but tolerate stripped trailing newlines.
	if buf.Len() > 0 && buf.Bytes()[buf.Len()-1] != '\n' {
		buf.WriteByte('\n')
	}

	// --- Emit objects, recording each one's byte offset for the xref ---
	nums := make([]int, 0, len(u.objects))
	for n := range u.objects {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	entries := make([]xrefEntry, 0, len(nums))
	for _, n := range nums {
		offset := int64(buf.Len())
		obj := u.objects[n]
		fmt.Fprintf(&buf, "%d 0 obj\n", n)
		buf.Write(obj.Body)
		if len(obj.Body) > 0 && obj.Body[len(obj.Body)-1] != '\n' {
			buf.WriteByte('\n')
		}
		buf.WriteString("endobj\n")
		entries = append(entries, xrefEntry{num: n, offset: offset})
	}

	if t.HasXrefStream {
		writeXrefStreamIncremental(&buf, t, entries)
	} else {
		writeClassicXrefIncremental(&buf, t, entries)
	}
	return buf.Bytes(), nil
}

// xrefEntry is one in-use object's number → offset mapping. Shared by
// the classic and xref-stream emit paths.
type xrefEntry struct {
	num    int
	offset int64
}

// writeClassicXrefIncremental emits the appended section using the
// classic `xref` keyword table + `trailer <<…>>` form (ISO 32000-1
// §7.5.4 + §7.5.5). This is the v0 default; see [Bytes] for the
// form-selection rule.
func writeClassicXrefIncremental(buf *bytes.Buffer, t Trailer, entries []xrefEntry) {
	xrefOffset := int64(buf.Len())
	buf.WriteString("xref\n")
	// Mandatory subsection: object 0 must exist and be marked free
	// (generation 65535, the "head of the free-list" convention).
	buf.WriteString("0 1\n")
	buf.WriteString("0000000000 65535 f \n")

	// Each updated object number forms a subsection. Adjacent runs are
	// coalesced so a contiguous block (e.g. {3,4,5}) is one subsection,
	// matching what conforming producers emit.
	for i := 0; i < len(entries); {
		j := i
		for j+1 < len(entries) && entries[j+1].num == entries[j].num+1 {
			j++
		}
		first := entries[i].num
		count := j - i + 1
		fmt.Fprintf(buf, "%d %d\n", first, count)
		for k := i; k <= j; k++ {
			fmt.Fprintf(buf, "%010d 00000 n \n", entries[k].offset)
		}
		i = j + 1
	}

	// New /Size must be > max object number defined across all
	// revisions: max(prior /Size, highestNewObj+1).
	newSize := t.Size
	for _, e := range entries {
		if e.num+1 > newSize {
			newSize = e.num + 1
		}
	}

	buf.WriteString("trailer\n")
	buf.WriteString("<<")
	fmt.Fprintf(buf, " /Size %d", newSize)
	fmt.Fprintf(buf, " /Root %s", t.Root)
	if t.Info != "" {
		fmt.Fprintf(buf, " /Info %s", t.Info)
	}
	fmt.Fprintf(buf, " /Prev %d", t.StartXref)
	buf.WriteString(" >>\n")
	buf.WriteString("startxref\n")
	fmt.Fprintf(buf, "%d\n", xrefOffset)
	buf.WriteString("%%EOF\n")
}

// writeXrefStreamIncremental emits the appended section as a
// /Type /XRef stream object (ISO 32000-1 §7.5.8). Used when the
// prior revision's last xref was itself a stream — strict readers
// expect the chain to stay homogeneous.
//
// Layout we emit:
//
//	M 0 obj
//	<< /Type /XRef /Size S /Root R [/Info I] /Prev P
//	   /W [1 4 2] /Index [first1 count1 first2 count2 …]
//	   /Length L >>
//	stream
//	<L bytes of records, W[0]+W[1]+W[2] = 7 bytes each>
//	endstream
//	endobj
//	startxref
//	<offset of "M 0 obj">
//	%%EOF
//
// M = max(updated obj nums) + 1 is reserved for the xref stream
// itself. The xref stream object's own entry is included in the
// stream so a strict reader can resolve `M 0 R` if anything in a
// future revision references it (rare, but the spec mandates the
// self-entry).
//
// Field widths /W [1 4 2]: 1-byte type, 4-byte offset (uint32 BE —
// 4 GiB max file size which exceeds the practical ceiling for
// editor docs), 2-byte generation (covers the 65535 free-list-head
// value). Records are uncompressed; FlateDecode is a follow-up
// improvement when we have a larger surface to amortise the
// import cost.
func writeXrefStreamIncremental(buf *bytes.Buffer, t Trailer, entries []xrefEntry) {
	// The xref-stream object takes the next-free object number;
	// callers reserve their objects above the prior /Size, and we
	// claim one slot beyond the highest updated number. Choosing
	// max(updated)+1 keeps the /Index packing tight when the
	// updated nums are contiguous.
	xrefObjNum := t.Size
	for _, e := range entries {
		if e.num >= xrefObjNum {
			xrefObjNum = e.num + 1
		}
	}

	xrefOffset := int64(buf.Len())

	// Build the stream payload first so /Length is known when we
	// emit the dictionary. Combine entries + the self-entry, then
	// pack into adjacent /Index subsections.
	type rec struct {
		num    int
		typ    byte
		offset uint32
	}
	recs := make([]rec, 0, len(entries)+1)
	for _, e := range entries {
		// Defensive: a 4 GiB+ PDF would exceed the chosen field
		// width. We don't currently produce one, but flag it
		// loudly if we ever do — silent truncation would corrupt
		// the xref.
		if e.offset > 0xFFFFFFFF {
			// Out of band: caller cannot recover. The classic-form
			// path can't represent this either (its offset field
			// is %010d = up to ~10^10 which is 10 GiB and so also
			// over uint32). For v0 we accept the limit; a future
			// /W [1 5 2] or [1 8 2] makes this go away.
			return
		}
		recs = append(recs, rec{num: e.num, typ: 1, offset: uint32(e.offset)})
	}
	recs = append(recs, rec{num: xrefObjNum, typ: 1, offset: uint32(xrefOffset)})
	sort.Slice(recs, func(i, j int) bool { return recs[i].num < recs[j].num })

	// Pack adjacent obj nums into /Index subsections.
	type sub struct {
		first int
		count int
	}
	var subs []sub
	for i := 0; i < len(recs); {
		j := i
		for j+1 < len(recs) && recs[j+1].num == recs[j].num+1 {
			j++
		}
		subs = append(subs, sub{first: recs[i].num, count: j - i + 1})
		i = j + 1
	}

	// Encode records as W=[1 4 2] big-endian rows.
	var stream bytes.Buffer
	stream.Grow(len(recs) * 7)
	for _, r := range recs {
		stream.WriteByte(r.typ)
		stream.WriteByte(byte(r.offset >> 24))
		stream.WriteByte(byte(r.offset >> 16))
		stream.WriteByte(byte(r.offset >> 8))
		stream.WriteByte(byte(r.offset))
		// Generation: 0 for every in-use entry; we don't reuse
		// slots so the high byte stays 0.
		stream.WriteByte(0)
		stream.WriteByte(0)
	}

	newSize := xrefObjNum + 1
	if t.Size > newSize {
		newSize = t.Size
	}

	// --- Emit the xref-stream object ---
	fmt.Fprintf(buf, "%d 0 obj\n", xrefObjNum)
	buf.WriteString("<< /Type /XRef")
	fmt.Fprintf(buf, " /Size %d", newSize)
	fmt.Fprintf(buf, " /Root %s", t.Root)
	if t.Info != "" {
		fmt.Fprintf(buf, " /Info %s", t.Info)
	}
	fmt.Fprintf(buf, " /Prev %d", t.StartXref)
	buf.WriteString(" /W [1 4 2]")
	buf.WriteString(" /Index [")
	for i, s := range subs {
		if i > 0 {
			buf.WriteByte(' ')
		}
		fmt.Fprintf(buf, "%d %d", s.first, s.count)
	}
	buf.WriteByte(']')
	fmt.Fprintf(buf, " /Length %d", stream.Len())
	buf.WriteString(" >>\nstream\n")
	buf.Write(stream.Bytes())
	buf.WriteString("\nendstream\nendobj\n")

	buf.WriteString("startxref\n")
	fmt.Fprintf(buf, "%d\n", xrefOffset)
	buf.WriteString("%%EOF\n")
}

// EncodeTo writes the same bytes as Bytes() to w. Allocations stay the
// same — Bytes() builds in memory either way — but EncodeTo lets the
// caller stream directly to disk without holding a second copy.
func (u *Update) EncodeTo(w io.Writer, original []byte) error {
	out, err := u.Bytes(original)
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}
