package dlp

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// buildOOXMLZip returns the bytes of an in-memory zip with the
// supplied (name, content) entries. Mirrors a real .docx /
// .xlsx / .pptx layout closely enough that the extractor's zip
// + XML pipeline runs the same code path as on a real file —
// without needing a real Office file as a fixture.
func buildOOXMLZip(t *testing.T, entries map[string]string) []byte {
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
	return buf.Bytes()
}

func TestExtractTextFromOOXML_DocxHappyPath(t *testing.T) {
	// Minimal docx-shaped zip with one document.xml that has
	// three <w:t> runs. The local-name match captures all
	// three regardless of the `w:` namespace prefix.
	doc := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>SSN:</w:t></w:r><w:r><w:t> 123-45-6789</w:t></w:r></w:p>
    <w:p><w:r><w:t>Done.</w:t></w:r></w:p>
  </w:body>
</w:document>`
	zipBytes := buildOOXMLZip(t, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types/>`,
		"word/document.xml":   doc,
		"word/media/image1.png": "binary noise that should be skipped",
		"word/_rels/document.xml.rels": `<?xml version="1.0"?><Relationships/>`,
	})

	got, err := ExtractTextFromOOXML(bytes.NewReader(zipBytes), int64(len(zipBytes)), OOXMLKindDocx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{"SSN:", "123-45-6789", "Done."} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\noutput: %q", want, got)
		}
	}
	// Binary noise from word/media/ MUST NOT appear — the
	// non-XML filter rejects it.
	if strings.Contains(got, "binary noise") {
		t.Errorf("non-XML zip entries leaked into output: %q", got)
	}
}

func TestExtractTextFromOOXML_XlsxFindsTextInSharedStringsAndInlineStrings(t *testing.T) {
	// xlsx splits its text content across two paths:
	//   - shared strings table (xl/sharedStrings.xml) for
	//     repeated cell values, referenced by index from
	//     worksheet cells.
	//   - inline strings (xl/worksheets/sheet*.xml) for
	//     one-off cell values.
	// The extractor must capture both — DLP doesn't care
	// which path the spreadsheet author took.
	sharedStrings := `<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <si><t>4111-1111-1111-1111</t></si>
  <si><t>Just a header</t></si>
</sst>`
	sheet := `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row><c r="A1" t="s"><v>0</v></c></row>
    <row><c r="A2" t="inlineStr"><is><t>SSN 987-65-4321</t></is></c></row>
  </sheetData>
</worksheet>`
	zipBytes := buildOOXMLZip(t, map[string]string{
		"xl/sharedStrings.xml":     sharedStrings,
		"xl/worksheets/sheet1.xml": sheet,
	})

	got, err := ExtractTextFromOOXML(bytes.NewReader(zipBytes), int64(len(zipBytes)), OOXMLKindXlsx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{"4111-1111-1111-1111", "Just a header", "SSN 987-65-4321"} {
		if !strings.Contains(got, want) {
			t.Errorf("xlsx output missing %q\noutput: %q", want, got)
		}
	}
}

func TestExtractTextFromOOXML_PptxCapturesSlideText(t *testing.T) {
	slide := `<?xml version="1.0"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
       xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree>
    <p:sp><p:txBody>
      <a:p><a:r><a:t>Quarterly card 4242 4242 4242 4242</a:t></a:r></a:p>
    </p:txBody></p:sp>
  </p:spTree></p:cSld>
</p:sld>`
	zipBytes := buildOOXMLZip(t, map[string]string{
		"ppt/slides/slide1.xml": slide,
	})

	got, err := ExtractTextFromOOXML(bytes.NewReader(zipBytes), int64(len(zipBytes)), OOXMLKindPptx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(got, "4242 4242 4242 4242") {
		t.Errorf("pptx output missing card-shape PII\noutput: %q", got)
	}
}

func TestExtractTextFromOOXML_OnlyWalksContentPrefix(t *testing.T) {
	// docx kind looks under "word/" — content under "xl/" or
	// "ppt/" in the same zip MUST be skipped. Guards against
	// a future bundling tool that drops a stale sheet1.xml in
	// the wrong tree.
	doc := `<?xml version="1.0"?><w:document xmlns:w="urn:x"><w:body><w:p><w:r><w:t>only-this</w:t></w:r></w:p></w:body></w:document>`
	wrong := `<?xml version="1.0"?><sst><si><t>should-not-appear</t></si></sst>`
	zipBytes := buildOOXMLZip(t, map[string]string{
		"word/document.xml":    doc,
		"xl/sharedStrings.xml": wrong,
	})
	got, err := ExtractTextFromOOXML(bytes.NewReader(zipBytes), int64(len(zipBytes)), OOXMLKindDocx)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(got, "only-this") {
		t.Errorf("docx prefix output missing expected text: %q", got)
	}
	if strings.Contains(got, "should-not-appear") {
		t.Errorf("docx walk leaked content from xl/: %q", got)
	}
}

func TestExtractTextFromOOXML_TruncatesAtOutputCapAndReportsErr(t *testing.T) {
	// Build a single text run that exceeds the output cap.
	// Caller still receives the prefix bytes; err is
	// ErrOOXMLTooLarge so callers can log + audit the
	// truncation event.
	const oversize = ooxmlMaxExtractBytes + 1024
	huge := strings.Repeat("x", oversize)
	doc := `<?xml version="1.0"?><w:document xmlns:w="urn:x"><w:body><w:p><w:r><w:t>` +
		huge + `</w:t></w:r></w:p></w:body></w:document>`
	zipBytes := buildOOXMLZip(t, map[string]string{
		"word/document.xml": doc,
	})
	got, err := ExtractTextFromOOXML(bytes.NewReader(zipBytes), int64(len(zipBytes)), OOXMLKindDocx)
	if !errors.Is(err, ErrOOXMLTooLarge) {
		t.Errorf("err = %v, want ErrOOXMLTooLarge", err)
	}
	// Prefix bytes still returned (so DLP findings in the
	// emitted region are actionable).
	if len(got) == 0 {
		t.Error("truncated output unexpectedly empty")
	}
	if len(got) > ooxmlMaxExtractBytes {
		t.Errorf("output exceeded cap: %d > %d", len(got), ooxmlMaxExtractBytes)
	}
}

func TestExtractTextFromOOXML_RejectsUnknownKind(t *testing.T) {
	_, err := ExtractTextFromOOXML(bytes.NewReader([]byte("PK")), 2, "rtf")
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestExtractTextFromOOXML_MalformedZipReturnsError(t *testing.T) {
	// Not a zip at all — the caller's contract says corrupt
	// zips surface an error so handler code can log + fall
	// open without misclassifying as "clean".
	_, err := ExtractTextFromOOXML(bytes.NewReader([]byte("not a zip")), 9, OOXMLKindDocx)
	if err == nil {
		t.Fatal("expected zip-open error")
	}
}

func TestExtractTextFromOOXML_PartialPartFailureKeepsRemainder(t *testing.T) {
	// One XML part is malformed; the other is fine. The
	// extractor must surface the readable part rather than
	// failing the whole document — a half-readable docx is
	// still PII-bearing and worth scanning.
	good := `<?xml version="1.0"?><w:document xmlns:w="urn:x"><w:body><w:p><w:r><w:t>readable</w:t></w:r></w:p></w:body></w:document>`
	bad := `<?xml version="1.0"?><w:document><<<<<`
	zipBytes := buildOOXMLZip(t, map[string]string{
		"word/document.xml": good,
		"word/footnotes.xml": bad,
	})
	got, err := ExtractTextFromOOXML(bytes.NewReader(zipBytes), int64(len(zipBytes)), OOXMLKindDocx)
	if err != nil {
		t.Fatalf("partial-failure walk should not error: %v", err)
	}
	if !strings.Contains(got, "readable") {
		t.Errorf("readable part was dropped: %q", got)
	}
}
