package dlp

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// OOXMLKind identifies which Office Open XML container shape
// the extractor should walk. Each kind maps to a different
// directory layout inside the zip.
type OOXMLKind string

const (
	OOXMLKindDocx OOXMLKind = "docx"
	OOXMLKindXlsx OOXMLKind = "xlsx"
	OOXMLKindPptx OOXMLKind = "pptx"
)

// ooxmlContentPrefix returns the zip-internal directory under
// which the kind's text-bearing XML lives. Walking just this
// subtree (rather than every file in the zip) skips
// embedded media, [Content_Types].xml, relationships, and
// other binary noise — all of which would slow extraction
// without contributing scanable text.
func ooxmlContentPrefix(kind OOXMLKind) (string, error) {
	switch kind {
	case OOXMLKindDocx:
		return "word/", nil
	case OOXMLKindXlsx:
		return "xl/", nil
	case OOXMLKindPptx:
		return "ppt/", nil
	default:
		return "", fmt.Errorf("dlp: unknown OOXML kind %q", string(kind))
	}
}

// ooxmlMaxExtractBytes caps the total text the extractor will
// emit. Mirrors the existing pdftext output cap so the
// downstream `Scan` step sees the same memory profile as the
// PDF path. Truncation happens silently — findings in the
// emitted prefix still surface; findings past the cap are
// missed, which is the right trade-off when the alternative is
// OOM under adversarial inputs.
const ooxmlMaxExtractBytes = 8 * 1024 * 1024

// ErrOOXMLTooLarge is returned when the extractor would emit
// more text than ooxmlMaxExtractBytes. The caller treats this
// the same as a successful extraction of the prefix — DLP
// findings up to the cap are still actionable.
var ErrOOXMLTooLarge = errors.New("dlp: OOXML extraction exceeded output cap")

// ExtractTextFromOOXML pulls the user-visible text out of an
// Office Open XML container (.docx / .xlsx / .pptx). Returns
// the concatenated text content of every `<*:t>` element under
// the kind's content prefix, joined by single spaces.
//
// Design choices:
//
//   - Pure stdlib (archive/zip + encoding/xml). Tika /
//     LibreOffice-headless would be more thorough (capturing
//     comments, footnotes, slide notes, chart labels, etc.)
//     but they're heavy runtime dependencies (Java sidecar /
//     CLI subprocess). For DLP — which only needs visible
//     PII-shaped text to feed a regex scan — the OOXML's own
//     visible-text elements are sufficient. Upgrading to Tika
//     is tracked as a follow-up if the dev tier ships a
//     deeper-coverage option.
//
//   - Local-name match on `t` rather than namespace-aware
//     match on `w:t` / `a:t`. Avoids hard-coding W3C / OOXML
//     namespaces, which Microsoft has rev'd across Office
//     versions. The trade-off: we'd accidentally capture text
//     from non-OOXML elements named `t` if they appeared in
//     these directories — extremely unlikely in practice.
//
//   - Files are walked in zip order, NOT name-sorted. Page /
//     slide ordering only matters for human reading; DLP scan
//     output is order-agnostic (the regex either matches or
//     doesn't). Skipping the sort saves an allocation per
//     extraction.
//
// The reader pair (io.ReaderAt + size) matches archive/zip's
// signature — callers with an *os.File pass it directly;
// callers with bytes wrap via bytes.NewReader.
func ExtractTextFromOOXML(r io.ReaderAt, size int64, kind OOXMLKind) (string, error) {
	prefix, err := ooxmlContentPrefix(kind)
	if err != nil {
		return "", err
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return "", fmt.Errorf("dlp: open OOXML zip: %w", err)
	}

	var out strings.Builder
	out.Grow(64 * 1024)
	truncated := false

	for _, f := range zr.File {
		if truncated {
			break
		}
		if !strings.HasPrefix(f.Name, prefix) {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
			// Skip binaries (images, embedded fonts) and
			// relationship files (.rels) — they carry no
			// scanable text.
			continue
		}
		if err := appendXMLTextContent(&out, f, &truncated); err != nil {
			// Per-part failure (corrupt XML, malformed zip
			// entry) drops just that part. The remainder of
			// the document is still worth scanning — a
			// half-readable docx is still PII-bearing.
			continue
		}
	}

	text := out.String()
	if truncated {
		return text, ErrOOXMLTooLarge
	}
	return text, nil
}

// appendXMLTextContent streams one XML part out of the zip and
// writes the CharData of every `<*:t>` element into `out`.
// Returns early with truncated=true once the output cap is
// reached so the caller stops walking further parts.
func appendXMLTextContent(out *strings.Builder, f *zip.File, truncated *bool) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	// OOXML files declare their encoding (utf-8) but some
	// emit a UTF-8 BOM that confuses strict parsers. The
	// charset hook below is a no-op for utf-8 — present so
	// the decoder accepts the declared encoding without
	// requiring caller-side configuration.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) {
		return input, nil
	}

	inTextElement := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inTextElement++
			}
		case xml.EndElement:
			if t.Name.Local == "t" && inTextElement > 0 {
				inTextElement--
				// Insert a single space between text runs
				// so adjacent runs ("Foo" + "bar") don't
				// concatenate into a single token that
				// breaks regex word boundaries.
				if out.Len() > 0 && !endsWithSpace(out) {
					out.WriteByte(' ')
				}
			}
		case xml.CharData:
			if inTextElement > 0 {
				if out.Len()+len(t) > ooxmlMaxExtractBytes {
					// Write what fits, then signal stop.
					remaining := ooxmlMaxExtractBytes - out.Len()
					if remaining > 0 {
						out.Write(t[:remaining])
					}
					*truncated = true
					return nil
				}
				out.Write(t)
			}
		}
	}
}

// endsWithSpace is a 1-byte tail check on the builder. Used to
// avoid emitting double spaces between text runs.
func endsWithSpace(out *strings.Builder) bool {
	s := out.String()
	return len(s) > 0 && s[len(s)-1] == ' '
}
