package invoice

import (
	"fmt"
	"strings"
)

// winAnsiToUnicode maps a single PDF WinAnsiEncoding byte to
// the Unicode codepoint a PDF reader should show when the
// user copies + pastes (or a screen reader narrates) that
// glyph. Empty codepoints (0x00-0x1F controls, 0x7F DEL, and
// the four undefined slots in the 0x80-0x9F range) return -1
// so the caller skips them in the CMap output.
//
// The 0x20-0x7E range (printable ASCII) is identity-mapped
// because the WinAnsi byte happens to equal its Unicode
// codepoint; same for 0xA0-0xFF (Latin-1 supplement). The
// only non-trivial mappings live in 0x80-0x9F — Windows
// repurposed those bytes for typographic characters (smart
// quotes, em/en dashes, the euro sign, bullets) that ISO
// 8859-1 left as control characters.
//
// Reference: PDF 1.7 Appendix D.2 (WinAnsiEncoding table).
func winAnsiToUnicode(code byte) rune {
	if code >= 0x20 && code <= 0x7E {
		return rune(code)
	}
	if code >= 0xA0 {
		return rune(code)
	}
	// 0x80-0x9F: WinAnsi-specific glyphs.
	switch code {
	case 0x80:
		return 0x20AC // €
	case 0x82:
		return 0x201A // ‚
	case 0x83:
		return 0x0192 // ƒ
	case 0x84:
		return 0x201E // „
	case 0x85:
		return 0x2026 // …
	case 0x86:
		return 0x2020 // †
	case 0x87:
		return 0x2021 // ‡
	case 0x88:
		return 0x02C6 // ˆ
	case 0x89:
		return 0x2030 // ‰
	case 0x8A:
		return 0x0160 // Š
	case 0x8B:
		return 0x2039 // ‹
	case 0x8C:
		return 0x0152 // Œ
	case 0x8E:
		return 0x017D // Ž
	case 0x91:
		return 0x2018 // '
	case 0x92:
		return 0x2019 // '
	case 0x93:
		return 0x201C // "
	case 0x94:
		return 0x201D // "
	case 0x95:
		return 0x2022 // •
	case 0x96:
		return 0x2013 // –
	case 0x97:
		return 0x2014 // —
	case 0x98:
		return 0x02DC // ˜
	case 0x99:
		return 0x2122 // ™
	case 0x9A:
		return 0x0161 // š
	case 0x9B:
		return 0x203A // ›
	case 0x9C:
		return 0x0153 // œ
	case 0x9E:
		return 0x017E // ž
	case 0x9F:
		return 0x0178 // Ÿ
	}
	// 0x00-0x1F control chars, 0x7F DEL, and the four
	// undefined slots (0x81, 0x8D, 0x8F, 0x90, 0x9D) have no
	// glyph and don't appear in the CMap.
	return -1
}

// buildWinAnsiToUnicodeCMap returns the contents of a
// /ToUnicode CMap stream covering the WinAnsi encoding. The
// stream is referenced from each font's /ToUnicode entry so
// PDF readers can recover Unicode codepoints from rendered
// glyph codes — necessary for correct copy/paste, text
// search, accessibility tools, and PDF/A conformance.
//
// Format follows PDF 1.7 §9.10.3 ("Mapping Character Codes
// to Unicode Values"). A single bfchar entry per glyph code
// is the simplest form and avoids range-coalescing pitfalls
// at the WinAnsi 0x80-0x9F boundary (where the mapping is
// non-monotonic in Unicode space). The output is
// deterministic — same bytes every render — so callers can
// golden-test it.
func buildWinAnsiToUnicodeCMap() string {
	var entries []byte
	count := 0
	for code := 0x20; code <= 0xFF; code++ {
		if cp := winAnsiToUnicode(byte(code)); cp >= 0 {
			entries = append(entries, []byte(fmt.Sprintf("<%02X> <%04X>\n", code, cp))...)
			count++
		}
	}

	// bfchar blocks are capped at 100 entries per PDF spec.
	// WinAnsi has ~219 entries so we emit three blocks.
	var b strings.Builder
	b.Grow(8 * 1024)
	b.WriteString("/CIDInit /ProcSet findresource begin\n")
	b.WriteString("12 dict begin\nbegincmap\n")
	b.WriteString("/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n")
	b.WriteString("/CMapName /Adobe-Identity-UCS def\n")
	b.WriteString("/CMapType 2 def\n")
	b.WriteString("1 begincodespacerange\n<00> <FF>\nendcodespacerange\n")

	const perBlock = 100
	chunks := splitCMapEntries(string(entries), perBlock)
	for _, chunk := range chunks {
		n := strings.Count(chunk, "\n")
		fmt.Fprintf(&b, "%d beginbfchar\n", n)
		b.WriteString(chunk)
		b.WriteString("endbfchar\n")
	}

	b.WriteString("endcmap\n")
	b.WriteString("CMapName currentdict /CMap defineresource pop\n")
	b.WriteString("end\nend\n")
	return b.String()
}

// splitCMapEntries chunks the bfchar entry list into blocks of
// at most `perBlock` lines so each beginbfchar/endbfchar pair
// stays within PDF's 100-entry-per-block limit.
func splitCMapEntries(entries string, perBlock int) []string {
	if entries == "" {
		return nil
	}
	lines := strings.SplitAfter(entries, "\n")
	// SplitAfter leaves a trailing empty string when the
	// input ends with "\n"; drop it so block sizes are
	// accurate.
	if last := lines[len(lines)-1]; last == "" {
		lines = lines[:len(lines)-1]
	}
	var out []string
	for i := 0; i < len(lines); i += perBlock {
		end := i + perBlock
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, strings.Join(lines[i:end], ""))
	}
	return out
}
