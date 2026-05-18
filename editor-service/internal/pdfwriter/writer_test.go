package pdfwriter

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"editor-service/internal/corpus"
)

// minimalPDF returns the central corpus fixture. Kept as a thin
// alias so call sites don't change shape, and so the test file
// reads the same as before the corpus package landed.
func minimalPDF() []byte { return corpus.Minimal() }

func TestDiscover_ReadsTrailer(t *testing.T) {
	pdf := minimalPDF()
	tr, err := Discover(pdf)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if tr.Size != 4 {
		t.Errorf("Size = %d, want 4", tr.Size)
	}
	if tr.Root != "1 0 R" {
		t.Errorf("Root = %q, want %q", tr.Root, "1 0 R")
	}
	if tr.HasXrefStream {
		t.Error("classic xref-table fixture should not be flagged as a stream")
	}
	// StartXref must point at the byte where "xref\n" begins.
	if !bytes.HasPrefix(pdf[tr.StartXref:], []byte("xref")) {
		t.Errorf("StartXref %d does not point at `xref` keyword", tr.StartXref)
	}
}

func TestDiscover_EmptyInput(t *testing.T) {
	_, err := Discover(nil)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestDiscover_NoEOF(t *testing.T) {
	_, err := Discover([]byte("%PDF-1.4\nnot a pdf"))
	if err == nil {
		t.Fatal("expected error for missing end-of-file marker")
	}
}

func TestDiscover_NoStartXref(t *testing.T) {
	_, err := Discover([]byte("%PDF-1.4\n%%EOF\n"))
	if err == nil {
		t.Fatal("expected error for missing startxref token")
	}
}

func TestUpdate_EmptyStillAppendsSection(t *testing.T) {
	pdf := minimalPDF()
	var u Update
	if !u.Empty() {
		t.Fatal("zero-value Update should be Empty()")
	}
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("output must begin with the original bytes byte-for-byte")
	}
	if len(out) <= len(pdf) {
		t.Error("empty Update should still append a fresh xref + trailer")
	}
	if !bytes.HasSuffix(out, []byte("%%EOF\n")) {
		t.Error("output should end with end-of-file marker")
	}
	// The appended trailer must reference the prior xref via /Prev.
	if !bytes.Contains(out[len(pdf):], []byte("/Prev")) {
		t.Error("appended trailer must include /Prev pointer")
	}
	// startxref in the appended section must point inside the appended
	// section, not inside `pdf`.
	startXrefPos := bytes.LastIndex(out, []byte("startxref"))
	eofPos := bytes.LastIndex(out, []byte("%%EOF"))
	tail := out[startXrefPos+len("startxref") : eofPos]
	off, err := readInt(tail)
	if err != nil {
		t.Fatalf("parse new startxref: %v", err)
	}
	if off < int64(len(pdf)) {
		t.Errorf("new startxref %d should point into the appended section (>= %d)",
			off, len(pdf))
	}
}

func TestUpdate_OverridesAnExistingObject(t *testing.T) {
	pdf := minimalPDF()
	var u Update
	// Replace the Page object with a rotated copy.
	u.Set(3, []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Rotate 90 >>"))
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}

	// Object 3's new body must appear after the original bytes.
	appendix := out[len(pdf):]
	if !bytes.Contains(appendix, []byte("3 0 obj\n")) {
		t.Error("appendix should contain a new `3 0 obj` header")
	}
	if !bytes.Contains(appendix, []byte("/Rotate 90")) {
		t.Error("appendix should contain the rotated page body")
	}

	// The xref subsection must include object 3 at the *appendix offset*,
	// not the original offset. Sanity check: the xref entry's 10-digit
	// offset should fall within the appendix.
	tr, err := Discover(out)
	if err != nil {
		t.Fatalf("Discover after update: %v", err)
	}
	if tr.Size < 4 {
		t.Errorf("new /Size = %d, want >= 4", tr.Size)
	}
	if tr.StartXref < int64(len(pdf)) {
		t.Errorf("new startxref %d should sit in the appendix (>= %d)",
			tr.StartXref, len(pdf))
	}
}

func TestUpdate_AddsNewObjectNumber(t *testing.T) {
	pdf := minimalPDF()
	var u Update
	// Add object 5 (one past the existing /Size=4).
	u.Set(5, []byte("<< /Type /Metadata /Subtype /XML /Length 0 >>"))
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	tr, err := Discover(out)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if tr.Size < 6 {
		t.Errorf("Size = %d, want >= 6 after adding object 5", tr.Size)
	}
}

func TestUpdate_CoalescesContiguousSubsections(t *testing.T) {
	pdf := minimalPDF()
	var u Update
	u.Set(3, []byte("<< /A 1 >>"))
	u.Set(4, []byte("<< /B 2 >>"))
	u.Set(5, []byte("<< /C 3 >>"))
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	appendix := out[len(pdf):]
	// We expect a single subsection header "3 3" (three contiguous
	// objects starting at 3), not three separate "3 1" / "4 1" / "5 1"
	// headers.
	if !strings.Contains(string(appendix), "3 3\n") {
		t.Errorf("expected coalesced subsection header `3 3`; appendix=\n%s", appendix)
	}
	if strings.Contains(string(appendix), "3 1\n") {
		t.Errorf("did not expect a `3 1` subsection — should have been coalesced")
	}
}

func TestUpdate_NonContiguousObjectsGetSeparateSubsections(t *testing.T) {
	pdf := minimalPDF()
	var u Update
	u.Set(3, []byte("<< /A 1 >>"))
	u.Set(7, []byte("<< /B 2 >>"))
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "3 1\n") {
		t.Errorf("expected subsection `3 1`; appendix=\n%s", appendix)
	}
	if !strings.Contains(appendix, "7 1\n") {
		t.Errorf("expected subsection `7 1`; appendix=\n%s", appendix)
	}
}

func TestUpdate_PreservesOriginalBytes(t *testing.T) {
	// The most important invariant: a signed first revision must
	// survive an incremental update byte-for-byte. We assert that on
	// the *update path* directly: out[:len(pdf)] must equal pdf.
	pdf := minimalPDF()
	var u Update
	u.Set(3, []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Rotate 180 >>"))
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !bytes.Equal(out[:len(pdf)], pdf) {
		t.Error("original bytes must be preserved verbatim across an update")
	}
}

func TestUpdate_DoubleUpdateChainsPrev(t *testing.T) {
	pdf := minimalPDF()

	var u1 Update
	u1.Set(3, []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Rotate 90 >>"))
	rev1, err := u1.Bytes(pdf)
	if err != nil {
		t.Fatalf("rev1: %v", err)
	}
	tr1, err := Discover(rev1)
	if err != nil {
		t.Fatalf("Discover rev1: %v", err)
	}

	var u2 Update
	u2.Set(3, []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Rotate 180 >>"))
	rev2, err := u2.Bytes(rev1)
	if err != nil {
		t.Fatalf("rev2: %v", err)
	}
	tr2, err := Discover(rev2)
	if err != nil {
		t.Fatalf("Discover rev2: %v", err)
	}
	if tr2.StartXref <= tr1.StartXref {
		t.Errorf("rev2 startxref %d should be > rev1 startxref %d",
			tr2.StartXref, tr1.StartXref)
	}
	// The /Prev chain — rev2 trailer must reference rev1's xref offset.
	if !bytes.Contains(rev2[len(rev1):], []byte(fmt.Sprintf("/Prev %d", tr1.StartXref))) {
		t.Errorf("rev2 trailer should contain /Prev %d (rev1 startxref)", tr1.StartXref)
	}
}

// minimalXrefStreamPDF is the smallest synthetic PDF whose latest xref
// section is a /Type /XRef stream object rather than a classic `xref`
// keyword block. We don't decode the payload; the dict carries /Size
// and /Root which is everything Discover needs.
func minimalXrefStreamPDF() []byte {
	// Hand-construct so the `startxref` value really points at the
	// `1 0 obj` header.
	header := "%PDF-1.7\n"
	obj := "1 0 obj\n<< /Type /XRef /Size 4 /Root 1 0 R /W [1 1 1] /Length 0 >>\nstream\nendstream\nendobj\n"
	startxref := len(header)
	trailer := fmt.Sprintf("startxref\n%d\n%%%%EOF\n", startxref)
	return []byte(header + obj + trailer)
}

func TestDiscover_ParsesXrefStreamDict(t *testing.T) {
	pdf := minimalXrefStreamPDF()
	tr, err := Discover(pdf)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !tr.HasXrefStream {
		t.Error("HasXrefStream should be true for a /Type /XRef object")
	}
	if tr.Size != 4 {
		t.Errorf("Size = %d, want 4", tr.Size)
	}
	if tr.Root != "1 0 R" {
		t.Errorf("Root = %q, want %q", tr.Root, "1 0 R")
	}
}

func TestUpdate_XrefStreamForm_MatchesPriorForm(t *testing.T) {
	// The writer's form-selection rule: when the prior revision's
	// xref is a stream object, the appended incremental section
	// MUST also be an xref stream. Mixing forms is technically
	// legal (ISO 32000-1 §7.5.6 — readers follow /Prev through
	// both) but strict preflight validators flag the mismatch.
	pdf := minimalXrefStreamPDF()

	var u Update
	// Adding one object exercises the xref-stream's /Index packing
	// (one user obj + the xref-stream's self-entry) and forces the
	// trailer to renumber /Size.
	u.Set(2, []byte("<< /Type /Catalog /Pages 1 0 R >>"))
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes on xref-stream document: %v", err)
	}
	if !bytes.HasPrefix(out, pdf) {
		t.Error("output must begin with the original bytes verbatim")
	}
	appendix := string(out[len(pdf):])

	// The appendix carries an xref-stream object — recognise it by
	// the /Type /XRef key and the /W /Index dict entries.
	if !strings.Contains(appendix, "/Type /XRef") {
		t.Errorf("appendix should contain a /Type /XRef stream object; got:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/W [1 4 2]") {
		t.Errorf("appendix should declare /W [1 4 2]; got:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/Index [") {
		t.Errorf("appendix should declare an /Index subsection list; got:\n%s", appendix)
	}
	if !strings.Contains(appendix, "/Prev") {
		t.Errorf("appendix trailer should include /Prev pointing at the prior xref stream; got:\n%s", appendix)
	}
	if !strings.Contains(appendix, "stream\n") || !strings.Contains(appendix, "\nendstream\n") {
		t.Errorf("appendix should wrap the xref data in a stream block; got:\n%s", appendix)
	}
	// Classic-form markers MUST NOT appear — that would mean the
	// form-selection logic regressed.
	if strings.Contains(appendix, "\nxref\n") {
		t.Errorf("appendix unexpectedly contains a classic xref keyword block:\n%s", appendix)
	}
	if strings.Contains(appendix, "\ntrailer\n") {
		t.Errorf("appendix unexpectedly contains a classic trailer keyword:\n%s", appendix)
	}
}

func TestUpdate_XrefStream_RecordsEncodeAsWBigEndian(t *testing.T) {
	// Spot-check the binary stream payload: a single updated user
	// object at offset O should encode as `01 OOOOOOOO 0000` (7
	// bytes), followed by the xref-stream's self-entry. We construct
	// a tiny PDF and decode the stream by slicing on the visible
	// `stream\n` / `\nendstream` markers.
	pdf := minimalXrefStreamPDF()
	var u Update
	u.Set(2, []byte("<< /Type /Catalog /Pages 1 0 R >>"))
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	// Find the inner stream bytes.
	streamMark := "stream\n"
	endStreamMark := "\nendstream"
	si := strings.Index(string(out[len(pdf):]), streamMark)
	if si < 0 {
		t.Fatalf("could not locate stream marker in appendix:\n%s", out[len(pdf):])
	}
	ei := strings.Index(string(out[len(pdf):]), endStreamMark)
	if ei < 0 || ei <= si {
		t.Fatalf("could not locate endstream marker")
	}
	payload := out[len(pdf)+si+len(streamMark) : len(pdf)+ei]
	// 2 records (user obj 2 + xref-stream self-entry) × 7 bytes each = 14.
	if len(payload) != 14 {
		t.Errorf("stream payload length = %d, want 14 (2 records × 7 bytes)", len(payload))
	}
	if payload[0] != 0x01 {
		t.Errorf("first record type = %d, want 1 (in-use)", payload[0])
	}
	if payload[7] != 0x01 {
		t.Errorf("second record type = %d, want 1 (in-use)", payload[7])
	}
	// Generation bytes are 0 for fresh in-use objects.
	if payload[5] != 0 || payload[6] != 0 || payload[12] != 0 || payload[13] != 0 {
		t.Errorf("generation bytes should be 0 for fresh in-use entries; got %v", payload)
	}
}

func TestUpdate_ClassicForm_StillUsedOnClassicPriorRevision(t *testing.T) {
	// Companion to the xref-stream test above: when the prior xref
	// is a classic keyword block, the appended section must remain
	// classic too. This is the v0 default — exists explicitly so a
	// future refactor that flips the form selector is caught here.
	pdf := minimalPDF()
	var u Update
	u.Set(4, []byte("<< /Dummy true >>"))
	out, err := u.Bytes(pdf)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	appendix := string(out[len(pdf):])
	if !strings.Contains(appendix, "\nxref\n") {
		t.Errorf("appendix should contain a classic `xref` keyword block; got:\n%s", appendix)
	}
	if strings.Contains(appendix, "/Type /XRef") {
		t.Errorf("appendix unexpectedly emitted an xref-stream object on a classic-form prior:\n%s", appendix)
	}
}

func TestDiscover_RejectsGarbageAtStartxref(t *testing.T) {
	// startxref offset points at random bytes that are neither `xref`
	// nor an object header — Discover should now reject this (it used
	// to silently fall into the `default:` branch and set
	// HasXrefStream=true, which would mislead the writer).
	pdf := []byte("%PDF-1.4\nblah blah blah\nstartxref\n9\n%%EOF\n")
	if _, err := Discover(pdf); err == nil {
		t.Fatal("expected error for startxref pointing at non-xref / non-object bytes")
	}
}

func TestIsObjectHeader_Cases(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"1 0 obj", true},
		{"42 99 obj\n<<", true},
		{"xref\n0 1\n", false},
		{"1 0", false},   // no obj
		{"1 obj", false}, // missing generation
		{"foo bar obj", false},
		{"", false},
		{"   1 0 obj", false}, // leading whitespace not tolerated; Discover rejects
	}
	for _, tc := range cases {
		if got := isObjectHeader([]byte(tc.in)); got != tc.want {
			t.Errorf("isObjectHeader(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
