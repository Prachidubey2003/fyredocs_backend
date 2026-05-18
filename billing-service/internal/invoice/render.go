package invoice

import (
	"fmt"
	"html"
	"strings"
)

// RenderHTML returns a self-contained HTML5 invoice. Inline
// styles only — the output is meant to drop straight into an
// outbound email's HTML part, where external `<link>` to
// stylesheets either gets stripped (Gmail) or fails to load
// (offline clients).
//
// Returns ErrEmptyNumber / ErrEmptyCurrency if those required
// fields haven't been set. Other validation errors land at
// New / Compute time.
func (inv Invoice) RenderHTML() (string, error) {
	if strings.TrimSpace(inv.Number) == "" {
		return "", ErrEmptyNumber
	}
	if strings.TrimSpace(inv.Currency) == "" {
		return "", ErrEmptyCurrency
	}
	var b strings.Builder
	b.Grow(2048)
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"/>`)
	b.WriteString(`<title>Invoice `)
	b.WriteString(html.EscapeString(inv.Number))
	b.WriteString(`</title></head><body style="font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;color:#111;max-width:640px;margin:24px auto;padding:0 16px">`)

	// Header — invoice number + dates
	b.WriteString(`<header style="display:flex;justify-content:space-between;align-items:flex-start;border-bottom:1px solid #e5e5e5;padding-bottom:16px;margin-bottom:16px"><div><h1 style="margin:0 0 4px;font-size:20px">Invoice `)
	b.WriteString(html.EscapeString(inv.Number))
	b.WriteString(`</h1>`)
	if inv.IssuedAt != "" {
		b.WriteString(`<div style="color:#666;font-size:13px">Issued `)
		b.WriteString(html.EscapeString(inv.IssuedAt))
		b.WriteString(`</div>`)
	}
	if inv.DueAt != "" {
		b.WriteString(`<div style="color:#666;font-size:13px">Due `)
		b.WriteString(html.EscapeString(inv.DueAt))
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
	// Total in the upper-right — what people glance at first.
	b.WriteString(`<div style="text-align:right"><div style="color:#666;font-size:12px;text-transform:uppercase;letter-spacing:0.04em">Amount due</div><div style="font-size:22px;font-weight:600;margin-top:2px">`)
	b.WriteString(html.EscapeString(FormatMoneyCents(inv.TotalCents, inv.Currency)))
	b.WriteString(`</div></div></header>`)

	// From / To panel
	b.WriteString(`<section style="display:flex;gap:24px;margin-bottom:24px;font-size:13px;line-height:1.5">`)
	writeParty(&b, "From", inv.Issuer)
	writeParty(&b, "Billed to", inv.Customer)
	b.WriteString(`</section>`)

	// Line items table
	b.WriteString(`<table style="width:100%;border-collapse:collapse;font-size:14px;margin-bottom:16px"><thead><tr style="border-bottom:1px solid #e5e5e5;text-align:left;color:#666"><th style="padding:8px 0;font-weight:500">Description</th><th style="padding:8px 0;font-weight:500;text-align:right;width:64px">Qty</th><th style="padding:8px 0;font-weight:500;text-align:right;width:96px">Unit</th><th style="padding:8px 0;font-weight:500;text-align:right;width:112px">Amount</th></tr></thead><tbody>`)
	for _, line := range inv.Lines {
		b.WriteString(`<tr style="border-bottom:1px solid #f0f0f0"><td style="padding:10px 0">`)
		b.WriteString(html.EscapeString(line.Description))
		b.WriteString(`</td><td style="padding:10px 0;text-align:right">`)
		b.WriteString(fmt.Sprintf("%d", line.Quantity))
		b.WriteString(`</td><td style="padding:10px 0;text-align:right">`)
		b.WriteString(html.EscapeString(FormatMoneyCents(line.UnitPriceCents, inv.Currency)))
		b.WriteString(`</td><td style="padding:10px 0;text-align:right">`)
		b.WriteString(html.EscapeString(FormatMoneyCents(line.LineTotalCents, inv.Currency)))
		b.WriteString(`</td></tr>`)
	}
	b.WriteString(`</tbody></table>`)

	// Totals
	b.WriteString(`<aside style="margin-left:auto;max-width:280px;font-size:14px"><div style="display:flex;justify-content:space-between;padding:4px 0"><span style="color:#666">Subtotal</span><span>`)
	b.WriteString(html.EscapeString(FormatMoneyCents(inv.SubtotalCents, inv.Currency)))
	b.WriteString(`</span></div>`)
	if inv.TaxBps > 0 {
		// 825 bps → "Tax (8.25%)"
		taxPct := fmt.Sprintf("%.2f%%", float64(inv.TaxBps)/100)
		b.WriteString(`<div style="display:flex;justify-content:space-between;padding:4px 0"><span style="color:#666">Tax (`)
		b.WriteString(taxPct)
		b.WriteString(`)</span><span>`)
		b.WriteString(html.EscapeString(FormatMoneyCents(inv.TaxCents, inv.Currency)))
		b.WriteString(`</span></div>`)
	}
	b.WriteString(`<div style="display:flex;justify-content:space-between;padding:8px 0;border-top:1px solid #e5e5e5;margin-top:4px;font-weight:600"><span>Total</span><span>`)
	b.WriteString(html.EscapeString(FormatMoneyCents(inv.TotalCents, inv.Currency)))
	b.WriteString(`</span></div></aside>`)

	// Memo
	if strings.TrimSpace(inv.Memo) != "" {
		b.WriteString(`<footer style="margin-top:32px;padding-top:16px;border-top:1px solid #e5e5e5;color:#666;font-size:13px;white-space:pre-wrap">`)
		b.WriteString(html.EscapeString(inv.Memo))
		b.WriteString(`</footer>`)
	}

	b.WriteString(`</body></html>`)
	return b.String(), nil
}

// writeParty emits one "From" or "Billed to" block. Empty Party
// fields are skipped so the column doesn't show stray blank
// lines.
func writeParty(b *strings.Builder, label string, p Party) {
	b.WriteString(`<div style="flex:1"><div style="color:#666;font-size:12px;text-transform:uppercase;letter-spacing:0.04em;margin-bottom:4px">`)
	b.WriteString(html.EscapeString(label))
	b.WriteString(`</div>`)
	if p.Name != "" {
		b.WriteString(`<div style="font-weight:500">`)
		b.WriteString(html.EscapeString(p.Name))
		b.WriteString(`</div>`)
	}
	if p.AddressLine != "" {
		b.WriteString(`<div style="color:#444;white-space:pre-line">`)
		b.WriteString(html.EscapeString(p.AddressLine))
		b.WriteString(`</div>`)
	}
	if p.Email != "" {
		b.WriteString(`<div style="color:#444">`)
		b.WriteString(html.EscapeString(p.Email))
		b.WriteString(`</div>`)
	}
	if p.TaxID != "" {
		b.WriteString(`<div style="color:#666;font-size:12px;margin-top:2px">Tax ID: `)
		b.WriteString(html.EscapeString(p.TaxID))
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
}

// RenderPlainText returns a fixed-column ASCII invoice suitable
// for SMTP plain-text parts, terminal display, or audit-log
// archives. Layout is intentionally simple — descriptions wrap
// at a 50-char column, amounts right-align in a 16-char column.
//
// Same required-field guards as RenderHTML.
func (inv Invoice) RenderPlainText() (string, error) {
	if strings.TrimSpace(inv.Number) == "" {
		return "", ErrEmptyNumber
	}
	if strings.TrimSpace(inv.Currency) == "" {
		return "", ErrEmptyCurrency
	}
	var b strings.Builder
	b.Grow(1024)
	fmt.Fprintf(&b, "INVOICE %s\n", inv.Number)
	if inv.IssuedAt != "" {
		fmt.Fprintf(&b, "Issued: %s\n", inv.IssuedAt)
	}
	if inv.DueAt != "" {
		fmt.Fprintf(&b, "Due:    %s\n", inv.DueAt)
	}
	b.WriteString(strings.Repeat("-", 68) + "\n")

	if inv.Issuer.Name != "" || inv.Customer.Name != "" {
		b.WriteString("FROM:\n")
		writePartyText(&b, inv.Issuer)
		b.WriteString("\nBILLED TO:\n")
		writePartyText(&b, inv.Customer)
		b.WriteString("\n")
		b.WriteString(strings.Repeat("-", 68) + "\n")
	}

	// 50-char description column + 16-char amount column.
	for _, line := range inv.Lines {
		desc := line.Description
		amount := FormatMoneyCents(line.LineTotalCents, inv.Currency)
		// Quick-and-honest wrap: chunk description into
		// 50-char rows; subsequent rows are blank in the
		// amount column.
		for first, chunk := true, ""; len(desc) > 0; first = false {
			if len(desc) <= 50 {
				chunk, desc = desc, ""
			} else {
				chunk, desc = desc[:50], desc[50:]
			}
			if first {
				fmt.Fprintf(&b, "%-50s %16s\n", chunk, amount)
			} else {
				fmt.Fprintf(&b, "%-50s\n", chunk)
			}
		}
	}
	b.WriteString(strings.Repeat("-", 68) + "\n")
	fmt.Fprintf(&b, "%-50s %16s\n", "Subtotal", FormatMoneyCents(inv.SubtotalCents, inv.Currency))
	if inv.TaxBps > 0 {
		label := fmt.Sprintf("Tax (%.2f%%)", float64(inv.TaxBps)/100)
		fmt.Fprintf(&b, "%-50s %16s\n", label, FormatMoneyCents(inv.TaxCents, inv.Currency))
	}
	b.WriteString(strings.Repeat("-", 68) + "\n")
	fmt.Fprintf(&b, "%-50s %16s\n", "TOTAL", FormatMoneyCents(inv.TotalCents, inv.Currency))

	if strings.TrimSpace(inv.Memo) != "" {
		b.WriteString("\n")
		b.WriteString(strings.Repeat("-", 68) + "\n")
		b.WriteString(inv.Memo)
		if !strings.HasSuffix(inv.Memo, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func writePartyText(b *strings.Builder, p Party) {
	if p.Name != "" {
		fmt.Fprintf(b, "  %s\n", p.Name)
	}
	if p.AddressLine != "" {
		for _, line := range strings.Split(p.AddressLine, "\n") {
			fmt.Fprintf(b, "  %s\n", line)
		}
	}
	if p.Email != "" {
		fmt.Fprintf(b, "  %s\n", p.Email)
	}
	if p.TaxID != "" {
		fmt.Fprintf(b, "  Tax ID: %s\n", p.TaxID)
	}
}
