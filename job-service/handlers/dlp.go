package handlers

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"fyredocs/shared/pdftext"

	"job-service/internal/dlp"
)

// dlpEnabled reports whether the DLP scan gate is active. Off
// by default so dev / staging deploys see no behavioural
// change; tenants on the enterprise tier flip it on via the
// `DLP_ENABLED=true` environment variable.
//
// A real implementation lives behind a per-org policy flag in
// auth-service (`org.dlp_policy`); the env-gate here is the
// v0 stopgap so we can ship the wire-up before the org-policy
// shape lands.
func dlpEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DLP_ENABLED")))
	return v == "1" || v == "true" || v == "yes"
}

// dlpTextExtensions is the v0 list of file extensions we read
// verbatim as text. Anything in this map gets the LimitReader
// path; anything else either takes a converter path (see
// dlpPDFExtensions) or bypasses with `scanned=false`.
//
// Lower-case, leading dot. Lookup uses filepath.Ext() which
// returns the same shape.
var dlpTextExtensions = map[string]bool{
	".txt":  true,
	".csv":  true,
	".tsv":  true,
	".md":   true,
	".json": true,
	".log":  true,
	".xml":  true,
	".html": true,
	".htm":  true,
	".yaml": true,
	".yml":  true,
}

// dlpPDFExtensions is the set of extensions routed through the
// shared `pdftext` library (text-literal extraction over PDF
// content streams). PDFs are the majority of uploads, so
// shipping DLP-for-PDF closes the most-important gap in the
// v0 plain-text gate.
var dlpPDFExtensions = map[string]bool{
	".pdf": true,
}

// dlpOOXMLExtensions maps an Office Open XML file extension to
// the [`dlp.OOXMLKind`] the in-process extractor walks. Pure-
// stdlib path (archive/zip + encoding/xml) — no Tika / no
// LibreOffice sidecar. See [`dlp.ExtractTextFromOOXML`] for the
// trade-off versus a heavier extractor (we cover visible-text
// elements but skip slide notes, comments, and chart labels —
// adequate for regex-driven PII scanning).
var dlpOOXMLExtensions = map[string]dlp.OOXMLKind{
	".docx": dlp.OOXMLKindDocx,
	".xlsx": dlp.OOXMLKindXlsx,
	".pptx": dlp.OOXMLKindPptx,
}

// dlpMaxScanBytes caps how much of a file we'll read into
// memory for scanning. 4MB is enough for any reasonable text
// payload + catches the "user pasted a 30MB CSV with one PII
// row at the top" case. Larger files truncate; we still flag
// findings in the first 4MB.
const dlpMaxScanBytes = 4 * 1024 * 1024

// dlpMaxPDFBytes caps how much of a PDF we'll read into memory
// before handing it to pdftext.Extract. The library itself
// caps its OUTPUT at 8MB; this is a guard on the INPUT side so
// a 500MB malicious PDF doesn't OOM the scanner. 32MB covers
// 99%+ of real uploads (the median is well under 5MB); larger
// PDFs slip through with scanned=false and are flagged in
// audit. v0 tradeoff — extending PDF coverage is tracked.
const dlpMaxPDFBytes = 32 * 1024 * 1024

// dlpMaxOOXMLBytes caps how much of an Office file we'll read
// in. Tighter than the PDF cap because OOXML files compress
// well (a 16MB .docx unpacks to dozens of MB of XML) and the
// extractor itself caps its OUTPUT via ooxmlMaxExtractBytes
// (8MB) — the input cap is just the OOM guard against a
// 500MB malicious zip. Real-world Office files cluster well
// under 5MB; 16MB covers virtually every legitimate upload.
const dlpMaxOOXMLBytes = 16 * 1024 * 1024

// dlpScanResult is what runDLPGate returns. Findings is the
// per-category list of matches (empty == clean). Scanned ==
// false means the scanner bypassed this file (env disabled,
// or file extension not in the text-only allow-list); the
// caller treats that the same as "clean" but logs it so audit
// data shows the bypass.
type dlpScanResult struct {
	Scanned  bool
	Findings []dlp.Finding
}

// runDLPGate inspects an assembled upload and returns whether
// the caller should proceed.
//
//   - DLP_ENABLED unset / not truthy → Scanned=false, no
//     findings, no error. The handler proceeds as if the
//     gate didn't exist.
//   - Extension in dlpTextExtensions → read first
//     dlpMaxScanBytes verbatim, run dlp.Scan.
//   - Extension in dlpPDFExtensions → read up to
//     dlpMaxPDFBytes, extract text literals via
//     pdftext.Extract, run dlp.Scan on the result. PDF text
//     extraction is best-effort — corrupt PDFs surface as
//     Scanned=false with no findings (logged separately by
//     the caller via the returned error). The plain-text
//     fast path is unchanged.
//   - Extension in dlpOOXMLExtensions (.docx / .xlsx / .pptx)
//     → read up to dlpMaxOOXMLBytes, walk the zip via
//     dlp.ExtractTextFromOOXML, run dlp.Scan on the
//     concatenated text. Truncation past the extractor's
//     output cap is treated the same as a successful scan
//     of the prefix — findings up to the cap are still
//     actionable.
//   - Anything else → Scanned=false, no findings.
//
// Returns a non-nil error only on a real filesystem read
// failure or a fatal PDF parse error — never for "the file
// looks clean" or "the file looks dirty". Callers distinguish
// those via Findings.
//
// Exposed at package level so tests can drive it without
// staging an HTTP request.
func runDLPGate(filePath, fileName string) (dlpScanResult, error) {
	if !dlpEnabled() {
		return dlpScanResult{Scanned: false}, nil
	}
	ext := strings.ToLower(filepath.Ext(fileName))

	switch {
	case dlpTextExtensions[ext]:
		return scanPlainText(filePath)
	case dlpPDFExtensions[ext]:
		return scanPDF(filePath)
	default:
		if kind, ok := dlpOOXMLExtensions[ext]; ok {
			return scanOOXML(filePath, kind)
		}
		return dlpScanResult{Scanned: false}, nil
	}
}

// scanPlainText runs the verbatim-bytes scan path. Capped at
// dlpMaxScanBytes; bytes past the cap silently miss findings.
func scanPlainText(filePath string) (dlpScanResult, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return dlpScanResult{}, err
	}
	defer f.Close()

	buf, err := io.ReadAll(io.LimitReader(f, dlpMaxScanBytes))
	if err != nil {
		return dlpScanResult{}, err
	}
	findings := dlp.Scan(string(buf))
	return dlpScanResult{Scanned: true, Findings: findings}, nil
}

// scanPDF runs the PDF extraction + scan path. Read cap on
// the input side (dlpMaxPDFBytes) protects the scanner from
// adversarial PDFs; the library applies its own 8MB cap on
// the OUTPUT side.
//
// Parse failures fall open with `Scanned: false, err == nil`
// — never 500 the upload because pdfcpu choked. The mimetype
// gate further up the chain catches non-PDF bytes with a
// `.pdf` extension; anything that gets here and fails to
// parse is a real-but-weird PDF (e.g., linearized with a
// corrupt xref) and the user-visible cost of blocking those
// outweighs the residual PII-leak risk. Returns an error
// only for genuine filesystem failures.
func scanPDF(filePath string) (dlpScanResult, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return dlpScanResult{}, err
	}
	defer f.Close()

	buf, err := io.ReadAll(io.LimitReader(f, dlpMaxPDFBytes))
	if err != nil {
		return dlpScanResult{}, err
	}
	text, err := pdftext.Extract(buf)
	if err != nil {
		return dlpScanResult{Scanned: false}, nil
	}
	findings := dlp.Scan(text)
	return dlpScanResult{Scanned: true, Findings: findings}, nil
}

// scanOOXML reads an Office Open XML file (.docx / .xlsx /
// .pptx), pulls visible-text content out of the relevant XML
// parts via dlp.ExtractTextFromOOXML, then runs dlp.Scan on
// the concatenated result.
//
// Failure modes:
//
//   - Filesystem failure → returns the error so the caller
//     surfaces it.
//   - Corrupt zip / malformed OOXML → falls open
//     (Scanned=false, err=nil). Matches scanPDF's stance:
//     blocking the upload of a corrupt file would punish the
//     user for malformed input that may not even contain PII,
//     and the mimetype gate higher up the chain already
//     rejects obviously-wrong content types.
//   - Output cap hit (dlp.ErrOOXMLTooLarge) → Scanned=true
//     with the findings collected from the emitted prefix.
//     The user-visible behaviour matches a clean scan when
//     no findings land in the prefix; when findings DO land,
//     they block as expected.
func scanOOXML(filePath string, kind dlp.OOXMLKind) (dlpScanResult, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return dlpScanResult{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return dlpScanResult{}, err
	}
	size := info.Size()
	if size > dlpMaxOOXMLBytes {
		size = dlpMaxOOXMLBytes
	}

	// archive/zip needs an io.ReaderAt. *os.File satisfies it
	// directly — no buffering needed for the zip directory
	// walk, which only reads the central-directory tail.
	text, err := dlp.ExtractTextFromOOXML(f, size, kind)
	if err != nil && !errors.Is(err, dlp.ErrOOXMLTooLarge) {
		// Corrupt zip / malformed XML — fall open. The
		// mimetype gate upstream stops genuinely
		// non-OOXML content with these extensions; anything
		// reaching here is a real-but-weird Office file.
		return dlpScanResult{Scanned: false}, nil
	}
	findings := dlp.Scan(text)
	return dlpScanResult{Scanned: true, Findings: findings}, nil
}

// dlpFindingCategoriesForResponse turns a slice of Findings
// into the de-duplicated category list we surface in the 422
// response body. We DO NOT return the matched substrings —
// the matched bytes ARE the sensitive data the user is being
// told to remove; echoing them back would defeat the purpose
// of the block.
func dlpFindingCategoriesForResponse(findings []dlp.Finding) []string {
	if len(findings) == 0 {
		return nil
	}
	seen := make(map[dlp.Category]bool, len(findings))
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		if seen[f.Category] {
			continue
		}
		seen[f.Category] = true
		out = append(out, string(f.Category))
	}
	return out
}
