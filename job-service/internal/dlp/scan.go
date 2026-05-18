package dlp

import (
	"sort"
	"strings"
)

// Finding is one match the scanner found in the source text.
type Finding struct {
	// Category is the kind of sensitive data — see the
	// Category constants for the supported set.
	Category Category
	// Match is the exact substring that triggered the
	// finding. Persist with caution — by definition this is
	// the sensitive value the scanner caught. Audit-log
	// consumers should redact / hash before storage.
	Match string
	// Start, End are byte offsets into the source text
	// (inclusive-exclusive). Useful for highlighting in the
	// upload-rejection UI ("we found a credit-card number
	// here, please remove it before retrying").
	Start, End int
	// Detail is an optional extra label per category — set
	// today only for credit-card matches (brand). Empty
	// otherwise.
	Detail string
}

// Scan runs every DLP pattern over `text` and returns the
// findings sorted by Start offset. Overlapping matches from
// different categories are all returned; the caller decides
// whether to dedupe.
//
// Empty input → nil. Allocates linear in the number of matches;
// the regex tables are compiled once at package init.
func Scan(text string) []Finding {
	if text == "" {
		return nil
	}
	var out []Finding
	for _, p := range patterns {
		for _, idx := range p.re.FindAllStringIndex(text, -1) {
			start, end := idx[0], idx[1]
			match := text[start:end]
			if p.cat == CategoryCreditCard {
				// Filter the noisy "13–19 digit run" regex
				// through Luhn — that's what separates a
				// real PAN from a phone number / SKU code.
				digits := stripSeparators(match)
				if !luhnValid(digits) {
					continue
				}
				out = append(out, Finding{
					Category: p.cat,
					Match:    match,
					Start:    start,
					End:      end,
					Detail:   brand(digits),
				})
				continue
			}
			out = append(out, Finding{
				Category: p.cat,
				Match:    match,
				Start:    start,
				End:      end,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		// Stable within same offset — keep category order
		// from the patterns table so callers see consistent
		// output across runs.
		return out[i].Category < out[j].Category
	})
	return out
}

// stripSeparators removes spaces and dashes — the only
// separators the credit-card regex tolerates. We avoid
// `strings.ReplaceAll` plus a second pass; for a 19-char
// max input the inline loop is faster + allocates less.
func stripSeparators(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if c := s[i]; c != ' ' && c != '-' {
			b = append(b, c)
		}
	}
	return string(b)
}

// luhnValid runs the Luhn checksum on `digits`. Returns false
// for inputs shorter than 13 or longer than 19 — outside that
// range, the digit run isn't a credit card by definition (the
// regex already enforces this, but the explicit guard keeps
// the function safe to call from anywhere).
func luhnValid(digits string) bool {
	n := len(digits)
	if n < 13 || n > 19 {
		return false
	}
	sum := 0
	double := false
	// Walk right-to-left, doubling every second digit.
	for i := n - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// brand returns the conventional card-network name for a
// PAN-shaped digit string. Useful for the audit log so on-call
// can grep "Amex" vs "Visa" leaks. Best-effort — unknown
// prefixes return empty.
func brand(digits string) string {
	if len(digits) == 0 {
		return ""
	}
	switch {
	case strings.HasPrefix(digits, "4"):
		return "visa"
	case len(digits) >= 2 && (digits[:2] >= "51" && digits[:2] <= "55"):
		return "mastercard"
	case len(digits) >= 2 && (digits[:2] == "34" || digits[:2] == "37"):
		return "amex"
	case len(digits) >= 4 && digits[:4] == "6011":
		return "discover"
	case len(digits) >= 2 && digits[:2] == "35":
		return "jcb"
	}
	return ""
}
