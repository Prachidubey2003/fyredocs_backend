package handlers

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"job-service/internal/dlp"
)

// dlpTestMinimalPDF builds the smallest PDF that pdfcpu will
// parse with `streamBody` as the raw (unescaped) text inside a
// single `Tj` literal. Mirrors the pattern used in
// shared/pdftext/extract_test.go but lives here because Go
// test helpers can't cross package boundaries.
//
// The body bytes go verbatim between the `(` and `)` of the
// Tj operand — so a caller passing `"SSN: 123-45-6789"` gets
// a PDF whose extracted text is exactly that string.
func dlpTestMinimalPDF(streamBody string) []byte {
	contents := "BT /F1 12 Tf 72 720 Td (" + streamBody + ") Tj ET"
	contentObj := fmt.Sprintf(
		"4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		len(contents), contents,
	)
	parts := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n",
		contentObj,
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	}
	return dlpTestBuildClassic(parts, "1 0 R")
}

func dlpTestBuildClassic(parts []string, root string) []byte {
	header := "%PDF-1.4\n"
	offsets := make([]int, 0, len(parts))
	pos := len(header)
	for _, p := range parts {
		offsets = append(offsets, pos)
		pos += len(p)
	}
	var b bytes.Buffer
	b.Grow(pos + 200)
	b.WriteString(header)
	for _, p := range parts {
		b.WriteString(p)
	}
	xrefOff := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(parts)+1)
	b.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root %s >>\nstartxref\n%d\n%%%%EOF\n",
		len(parts)+1, root, xrefOff)
	return b.Bytes()
}

// writeTempFile drops `body` into a temp file and returns its
// path. Saves typing across the gate tests.
func writeTempFile(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

// enableDLP flips DLP_ENABLED=true for the duration of the
// test. t.Setenv handles unwinding.
func enableDLP(t *testing.T) {
	t.Helper()
	t.Setenv("DLP_ENABLED", "true")
}

// ---- dlpEnabled --------------------------------------------------------

func TestDLPEnabled_Truthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "True", "yes", "YES", " true "} {
		t.Setenv("DLP_ENABLED", v)
		if !dlpEnabled() {
			t.Errorf("DLP_ENABLED=%q should be truthy", v)
		}
	}
}

func TestDLPEnabled_Falsy(t *testing.T) {
	// Explicit falsy + the default "unset" case both produce
	// false. Unset gets simulated by setting then unsetting.
	for _, v := range []string{"", "0", "false", "off", "no", "anything-else"} {
		t.Setenv("DLP_ENABLED", v)
		if dlpEnabled() {
			t.Errorf("DLP_ENABLED=%q should NOT be truthy", v)
		}
	}
	t.Setenv("DLP_ENABLED", "") // simulate unset
	if dlpEnabled() {
		t.Error("empty DLP_ENABLED should be falsy")
	}
}

// ---- runDLPGate bypass paths ------------------------------------------

func TestRunDLPGate_BypassWhenDisabled(t *testing.T) {
	// Even a file with obvious PII passes through when the
	// gate is off — the env-flag is the user-facing kill
	// switch for the whole feature.
	t.Setenv("DLP_ENABLED", "")
	path := writeTempFile(t, "leak.txt", "SSN: 123-45-6789")
	got, err := runDLPGate(path, "leak.txt")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if got.Scanned {
		t.Error("expected Scanned=false when DLP is disabled")
	}
	if len(got.Findings) != 0 {
		t.Errorf("expected no findings when bypassed; got %+v", got.Findings)
	}
}

func TestRunDLPGate_BypassNonTextExtension(t *testing.T) {
	// PDFs scan via shared/pdftext (see TestRunDLPGate_ScansPDFAndFindsSSN).
	// Office files now scan via the stdlib OOXML extractor (see
	// the OOXML tests below). What's left as bypass: images
	// (no text), unknown extensions, and bare files.
	enableDLP(t)
	for _, name := range []string{"leak.png", "leak.jpg", "leak.zip", "leak"} {
		path := writeTempFile(t, name, "SSN: 123-45-6789")
		got, err := runDLPGate(path, name)
		if err != nil {
			t.Fatalf("%s: runDLPGate: %v", name, err)
		}
		if got.Scanned {
			t.Errorf("%s: expected Scanned=false (non-text)", name)
		}
	}
}

// ---- runDLPGate active path --------------------------------------------

func TestRunDLPGate_CleanTextPasses(t *testing.T) {
	enableDLP(t)
	path := writeTempFile(t, "notes.txt", "Hello world.\nNothing sensitive here.")
	got, err := runDLPGate(path, "notes.txt")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if !got.Scanned {
		t.Error("expected Scanned=true for text file")
	}
	if len(got.Findings) != 0 {
		t.Errorf("expected no findings on clean text; got %+v", got.Findings)
	}
}

func TestRunDLPGate_FlagsSSN(t *testing.T) {
	enableDLP(t)
	path := writeTempFile(t, "leak.txt", "Customer SSN: 123-45-6789")
	got, err := runDLPGate(path, "leak.txt")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if !got.Scanned {
		t.Error("expected Scanned=true")
	}
	if len(got.Findings) == 0 {
		t.Fatal("expected at least one SSN finding")
	}
	found := false
	for _, f := range got.Findings {
		if f.Category == dlp.CategorySSN {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected an SSN finding in %+v", got.Findings)
	}
}

func TestRunDLPGate_FlagsCreditCardInCSV(t *testing.T) {
	// Realistic shape: a CSV row with a Luhn-valid PAN. The
	// DLP scan should flag it.
	enableDLP(t)
	body := "id,name,card\n42,Alice,4111 1111 1111 1111\n"
	path := writeTempFile(t, "customers.csv", body)
	got, err := runDLPGate(path, "customers.csv")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if !got.Scanned {
		t.Error("expected Scanned=true")
	}
	gotCategories := make([]string, 0, len(got.Findings))
	for _, f := range got.Findings {
		gotCategories = append(gotCategories, string(f.Category))
	}
	sort.Strings(gotCategories)
	if len(gotCategories) == 0 || gotCategories[0] != string(dlp.CategoryCreditCard) {
		t.Errorf("expected credit_card finding; got %v", gotCategories)
	}
}

func TestRunDLPGate_RespectsScanCap(t *testing.T) {
	// File body larger than dlpMaxScanBytes; the SSN sits
	// past the cap. The gate's scan window stops at
	// dlpMaxScanBytes, so the SSN should NOT be flagged.
	// Documents the v0 truncation behaviour so a future
	// regression on the limit fails loudly.
	enableDLP(t)
	// Make the padding alone larger than the cap so the SSN
	// (appended after) lands past the scan window. Bytes per
	// repetition × repetitions > dlpMaxScanBytes, with margin.
	const filler = "filler text. " // 13 bytes
	padding := strings.Repeat(filler, (dlpMaxScanBytes/len(filler))+10)
	body := padding + "\nSSN: 123-45-6789\n"
	if len(body) <= dlpMaxScanBytes {
		t.Fatalf("test setup: body=%d bytes, must exceed cap %d", len(body), dlpMaxScanBytes)
	}
	path := writeTempFile(t, "huge.txt", body)
	got, err := runDLPGate(path, "huge.txt")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if !got.Scanned {
		t.Error("expected Scanned=true (file is text and DLP is on)")
	}
	for _, f := range got.Findings {
		if f.Category == dlp.CategorySSN {
			t.Errorf("SSN past the %d-byte cap was flagged — cap not honoured", dlpMaxScanBytes)
		}
	}
}

// ---- runDLPGate PDF path ----------------------------------------------

// writeTempBytes is the binary-safe sibling of writeTempFile.
// PDFs are not utf8-safe so we can't reuse the string helper.
func writeTempBytes(t *testing.T, name string, body []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

func TestRunDLPGate_ScansPDFAndFindsSSN(t *testing.T) {
	// The most important PDF case: a real PDF with an SSN in a
	// text literal must trip the gate. This closes the v0
	// plain-text-only gap.
	enableDLP(t)
	pdf := dlpTestMinimalPDF("Customer SSN: 123-45-6789")
	path := writeTempBytes(t, "contract.pdf", pdf)
	got, err := runDLPGate(path, "contract.pdf")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if !got.Scanned {
		t.Fatal("expected Scanned=true for a .pdf upload with DLP enabled")
	}
	found := false
	for _, f := range got.Findings {
		if f.Category == dlp.CategorySSN {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected an SSN finding in PDF contents; got %+v", got.Findings)
	}
}

func TestRunDLPGate_CleanPDFPasses(t *testing.T) {
	enableDLP(t)
	pdf := dlpTestMinimalPDF("Quarterly results: profit up, margin steady.")
	path := writeTempBytes(t, "report.pdf", pdf)
	got, err := runDLPGate(path, "report.pdf")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if !got.Scanned {
		t.Fatal("expected Scanned=true on clean PDF")
	}
	if len(got.Findings) != 0 {
		t.Errorf("expected no findings on clean PDF; got %+v", got.Findings)
	}
}

func TestRunDLPGate_CorruptPDFFallsOpen(t *testing.T) {
	// Bytes with a .pdf extension that aren't a valid PDF
	// (mimetype gate further upstream would normally catch
	// this, but defense in depth). The gate must NOT 500 the
	// upload — it falls open with Scanned=false, nil err.
	enableDLP(t)
	path := writeTempFile(t, "garbage.pdf", "this is not a PDF, just plain text with SSN 123-45-6789")
	got, err := runDLPGate(path, "garbage.pdf")
	if err != nil {
		t.Fatalf("corrupt PDF must not surface an error; got %v", err)
	}
	if got.Scanned {
		t.Errorf("expected Scanned=false on PDF parse failure (fall-open); got Scanned=true with findings %+v", got.Findings)
	}
}

func TestRunDLPGate_BypassPDFWhenDisabled(t *testing.T) {
	// The env-flag still gates the whole feature — PDF scanning
	// is no exception. A PDF with an SSN passes through when
	// DLP_ENABLED is off.
	t.Setenv("DLP_ENABLED", "")
	pdf := dlpTestMinimalPDF("Customer SSN: 123-45-6789")
	path := writeTempBytes(t, "contract.pdf", pdf)
	got, err := runDLPGate(path, "contract.pdf")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if got.Scanned {
		t.Error("expected Scanned=false when DLP is disabled, even for PDFs")
	}
	if len(got.Findings) != 0 {
		t.Errorf("expected no findings when bypassed; got %+v", got.Findings)
	}
}

// ---- Error paths ------------------------------------------------------

func TestRunDLPGate_MissingFileReturnsError(t *testing.T) {
	enableDLP(t)
	got, err := runDLPGate("/nonexistent/path/to.txt", "to.txt")
	if err == nil {
		t.Error("expected error for missing file")
	}
	if got.Scanned {
		t.Error("Scanned should be false on read error")
	}
}

// ---- Response-categories helper ---------------------------------------

func TestDLPFindingCategoriesForResponse_DedupesAndPreservesShape(t *testing.T) {
	findings := []dlp.Finding{
		{Category: dlp.CategorySSN, Match: "123-45-6789"},
		{Category: dlp.CategoryCreditCard, Match: "4111 1111 1111 1111"},
		{Category: dlp.CategorySSN, Match: "987-65-4321"},  // duplicate category
		{Category: dlp.CategoryEmail, Match: "x@y.com"},
		{Category: dlp.CategoryCreditCard, Match: "5555..."}, // dup
	}
	got := dlpFindingCategoriesForResponse(findings)
	if len(got) != 3 {
		t.Errorf("expected 3 deduped categories; got %v", got)
	}
	// Crucially: the raw matched bytes MUST NOT appear in
	// the response. The whole point of the block is to keep
	// sensitive bytes out of every channel — echoing them
	// back to the caller defeats the purpose.
	joined := strings.Join(got, "|")
	for _, leak := range []string{"123-45-6789", "987-65-4321", "4111", "5555", "x@y.com"} {
		if strings.Contains(joined, leak) {
			t.Errorf("response should NOT echo matched bytes; %q leaked into %q", leak, joined)
		}
	}
}

func TestDLPFindingCategoriesForResponse_EmptyInputReturnsNil(t *testing.T) {
	if got := dlpFindingCategoriesForResponse(nil); got != nil {
		t.Errorf("nil findings: got %v, want nil", got)
	}
	if got := dlpFindingCategoriesForResponse([]dlp.Finding{}); got != nil {
		t.Errorf("empty findings: got %v, want nil", got)
	}
}

// ---- runDLPGate OOXML path --------------------------------------------

// writeOOXMLZip stages a .docx/.xlsx/.pptx-shaped zip file on
// disk and returns the path. The caller picks the (name,
// content) entries based on which kind they're exercising.
func writeOOXMLZip(t *testing.T, outName string, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, outName)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

func TestRunDLPGate_FlagsSSNInDocx(t *testing.T) {
	enableDLP(t)
	doc := `<?xml version="1.0"?><w:document xmlns:w="urn:x"><w:body>
		<w:p><w:r><w:t>Employee SSN: 123-45-6789</w:t></w:r></w:p>
	</w:body></w:document>`
	path := writeOOXMLZip(t, "leak.docx", map[string]string{
		"word/document.xml": doc,
	})

	got, err := runDLPGate(path, "leak.docx")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if !got.Scanned {
		t.Fatal("expected Scanned=true for docx with text")
	}
	hasSSN := false
	for _, f := range got.Findings {
		if f.Category == dlp.CategorySSN {
			hasSSN = true
			break
		}
	}
	if !hasSSN {
		t.Errorf("expected SSN finding; got %+v", got.Findings)
	}
}

func TestRunDLPGate_FlagsCreditCardInXlsx(t *testing.T) {
	enableDLP(t)
	sharedStrings := `<?xml version="1.0"?><sst><si><t>4111-1111-1111-1111</t></si></sst>`
	sheet := `<?xml version="1.0"?><worksheet><sheetData><row><c r="A1" t="s"><v>0</v></c></row></sheetData></worksheet>`
	path := writeOOXMLZip(t, "leak.xlsx", map[string]string{
		"xl/sharedStrings.xml":     sharedStrings,
		"xl/worksheets/sheet1.xml": sheet,
	})

	got, err := runDLPGate(path, "leak.xlsx")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if !got.Scanned {
		t.Fatal("expected Scanned=true for xlsx with text")
	}
	hasCC := false
	for _, f := range got.Findings {
		if f.Category == dlp.CategoryCreditCard {
			hasCC = true
			break
		}
	}
	if !hasCC {
		t.Errorf("expected credit-card finding; got %+v", got.Findings)
	}
}

func TestRunDLPGate_FlagsCreditCardInPptx(t *testing.T) {
	enableDLP(t)
	slide := `<?xml version="1.0"?><p:sld xmlns:p="urn:x" xmlns:a="urn:y">
		<p:cSld><p:spTree><p:sp><p:txBody>
			<a:p><a:r><a:t>Card: 4242 4242 4242 4242</a:t></a:r></a:p>
		</p:txBody></p:sp></p:spTree></p:cSld>
	</p:sld>`
	path := writeOOXMLZip(t, "leak.pptx", map[string]string{
		"ppt/slides/slide1.xml": slide,
	})

	got, err := runDLPGate(path, "leak.pptx")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if !got.Scanned {
		t.Fatal("expected Scanned=true for pptx with text")
	}
	hasCC := false
	for _, f := range got.Findings {
		if f.Category == dlp.CategoryCreditCard {
			hasCC = true
			break
		}
	}
	if !hasCC {
		t.Errorf("expected credit-card finding; got %+v", got.Findings)
	}
}

func TestRunDLPGate_CleanDocxPasses(t *testing.T) {
	enableDLP(t)
	doc := `<?xml version="1.0"?><w:document xmlns:w="urn:x"><w:body>
		<w:p><w:r><w:t>Quarterly progress notes. All on track.</w:t></w:r></w:p>
	</w:body></w:document>`
	path := writeOOXMLZip(t, "clean.docx", map[string]string{
		"word/document.xml": doc,
	})

	got, err := runDLPGate(path, "clean.docx")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if !got.Scanned {
		t.Error("expected Scanned=true for valid docx")
	}
	if len(got.Findings) != 0 {
		t.Errorf("expected no findings on clean docx; got %+v", got.Findings)
	}
}

func TestRunDLPGate_CorruptOOXMLFallsOpen(t *testing.T) {
	// A .docx-named file that isn't a valid zip must NOT block
	// the upload. Mirrors scanPDF's fall-open semantics — the
	// mimetype gate upstream rejects obviously-wrong content
	// types; anything reaching DLP and failing to parse is a
	// real-but-weird Office file and we shouldn't punish the
	// user.
	enableDLP(t)
	path := writeTempFile(t, "broken.docx", "this is not a zip")
	got, err := runDLPGate(path, "broken.docx")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if got.Scanned {
		t.Error("expected Scanned=false for corrupt zip (fall open)")
	}
	if len(got.Findings) != 0 {
		t.Errorf("corrupt zip should produce no findings; got %+v", got.Findings)
	}
}

func TestRunDLPGate_BypassOOXMLWhenDisabled(t *testing.T) {
	// DLP_ENABLED unset → office files bypass like every
	// other extension.
	t.Setenv("DLP_ENABLED", "")
	doc := `<?xml version="1.0"?><w:document xmlns:w="urn:x"><w:body>
		<w:p><w:r><w:t>SSN: 123-45-6789</w:t></w:r></w:p>
	</w:body></w:document>`
	path := writeOOXMLZip(t, "leak.docx", map[string]string{
		"word/document.xml": doc,
	})
	got, err := runDLPGate(path, "leak.docx")
	if err != nil {
		t.Fatalf("runDLPGate: %v", err)
	}
	if got.Scanned {
		t.Error("expected Scanned=false when DLP disabled")
	}
}
