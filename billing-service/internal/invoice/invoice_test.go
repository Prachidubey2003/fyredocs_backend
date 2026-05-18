package invoice

import (
	"errors"
	"strings"
	"testing"
)

// sampleInvoice produces a small but realistic invoice used as
// the shared fixture across the rendering tests. Caller can
// shallow-clone + mutate fields per test.
func sampleInvoice() Invoice {
	return Invoice{
		Number:   "FYR-2026-0042",
		IssuedAt: "2026-05-16",
		DueAt:    "2026-06-15",
		Currency: "USD",
		Issuer: Party{
			Name:        "Fyredocs Inc.",
			Email:       "billing@fyredocs.com",
			AddressLine: "548 Market St #54129\nSan Francisco, CA 94104",
			TaxID:       "EIN 84-1234567",
		},
		Customer: Party{
			Name:        "Acme Corp",
			Email:       "ap@acme.example",
			AddressLine: "1 Acme Plaza\nAnytown, NY 10001",
		},
		Lines: []LineItem{
			{Description: "Fyredocs Pro — May 2026", Quantity: 1, UnitPriceCents: 1500},
			{Description: "Additional seats (3 × $12.00)", Quantity: 3, UnitPriceCents: 1200},
			{Description: "API ops above plan", Quantity: 2000, UnitPriceCents: 1},
		},
		TaxBps: 825, // 8.25%
		Memo:   "Thanks for being a customer — see you next month.",
	}
}

// ---- Math --------------------------------------------------------------

func TestCompute_BasicLineTotalsAndSubtotal(t *testing.T) {
	inv := Compute(sampleInvoice())
	want := []int64{1500, 3600, 2000}
	for i, w := range want {
		if got := inv.Lines[i].LineTotalCents; got != w {
			t.Errorf("Lines[%d].LineTotalCents = %d, want %d", i, got, w)
		}
	}
	if inv.SubtotalCents != 7100 {
		t.Errorf("SubtotalCents = %d, want 7100", inv.SubtotalCents)
	}
}

func TestCompute_TaxRoundsTowardZero(t *testing.T) {
	inv := Compute(sampleInvoice())
	// 7100 * 825 / 10000 = 585.75 → 585 (integer truncate).
	if inv.TaxCents != 585 {
		t.Errorf("TaxCents = %d, want 585 (truncated from 585.75)", inv.TaxCents)
	}
	if inv.TotalCents != 7685 {
		t.Errorf("TotalCents = %d, want 7685 (7100 + 585)", inv.TotalCents)
	}
}

func TestCompute_ZeroTaxKeepsTotalEqualToSubtotal(t *testing.T) {
	inv := sampleInvoice()
	inv.TaxBps = 0
	got := Compute(inv)
	if got.TaxCents != 0 {
		t.Errorf("TaxCents = %d, want 0", got.TaxCents)
	}
	if got.TotalCents != got.SubtotalCents {
		t.Errorf("TotalCents (%d) != SubtotalCents (%d)", got.TotalCents, got.SubtotalCents)
	}
}

func TestCompute_DiscountsAsNegativeLineItems(t *testing.T) {
	// Discount expressed as a line with negative unit price.
	// Subtotal nets the discount, tax applies to the netted
	// subtotal — same behaviour Stripe / QuickBooks use.
	inv := Invoice{
		Number:   "FYR-2026-0099",
		Currency: "USD",
		Lines: []LineItem{
			{Description: "Fyredocs Pro — May 2026", Quantity: 1, UnitPriceCents: 1500},
			{Description: "Annual-prepay discount", Quantity: 1, UnitPriceCents: -300},
		},
		TaxBps: 1000, // 10%
	}
	got := Compute(inv)
	if got.SubtotalCents != 1200 {
		t.Errorf("SubtotalCents = %d, want 1200 (1500 - 300)", got.SubtotalCents)
	}
	if got.TaxCents != 120 {
		t.Errorf("TaxCents = %d, want 120 (10%% of netted subtotal)", got.TaxCents)
	}
	if got.TotalCents != 1320 {
		t.Errorf("TotalCents = %d, want 1320", got.TotalCents)
	}
}

func TestCompute_NegativeSubtotalGetsZeroTax(t *testing.T) {
	// Refund invoice: full credit, no items, no tax owed.
	inv := Invoice{
		Number:   "FYR-2026-0100-credit",
		Currency: "USD",
		Lines: []LineItem{
			{Description: "Refund for invoice #42", Quantity: 1, UnitPriceCents: -1500},
		},
		TaxBps: 825,
	}
	got := Compute(inv)
	if got.SubtotalCents != -1500 {
		t.Errorf("SubtotalCents = %d, want -1500", got.SubtotalCents)
	}
	if got.TaxCents != 0 {
		t.Errorf("TaxCents = %d, want 0 on a negative subtotal (no IRS refund of uncollected tax)", got.TaxCents)
	}
	if got.TotalCents != -1500 {
		t.Errorf("TotalCents = %d, want -1500", got.TotalCents)
	}
}

func TestCompute_IsIdempotent(t *testing.T) {
	first := Compute(sampleInvoice())
	second := Compute(first)
	if first.SubtotalCents != second.SubtotalCents ||
		first.TaxCents != second.TaxCents ||
		first.TotalCents != second.TotalCents {
		t.Errorf("re-running Compute on a computed invoice changed totals:\n  first=%+v\n  second=%+v", first, second)
	}
}

// ---- Constructor / validation ------------------------------------------

func TestNew_NormalisesCurrencyAndComputes(t *testing.T) {
	raw := sampleInvoice()
	raw.Currency = " usd "
	got, err := New(raw)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", got.Currency)
	}
	if got.TotalCents != 7685 {
		t.Errorf("TotalCents = %d, want 7685 — New should also Compute", got.TotalCents)
	}
}

func TestNew_RejectsNoLines(t *testing.T) {
	inv := sampleInvoice()
	inv.Lines = nil
	if _, err := New(inv); err == nil {
		t.Error("New should reject an invoice with no line items")
	}
}

func TestNew_RejectsBlankLineDescription(t *testing.T) {
	inv := sampleInvoice()
	inv.Lines[1].Description = "   "
	_, err := New(inv)
	if err == nil || !strings.Contains(err.Error(), "Description") {
		t.Errorf("err = %v, want a description-required error", err)
	}
}

func TestNew_RejectsTaxBpsOutOfRange(t *testing.T) {
	for _, bps := range []int{-1, 10001} {
		inv := sampleInvoice()
		inv.TaxBps = bps
		_, err := New(inv)
		if !errors.Is(err, ErrTaxBpsOutOfRange) {
			t.Errorf("TaxBps=%d: err = %v, want ErrTaxBpsOutOfRange", bps, err)
		}
	}
}

// ---- Render guards -----------------------------------------------------

func TestRenderHTML_RejectsMissingNumber(t *testing.T) {
	inv, _ := New(sampleInvoice())
	inv.Number = ""
	if _, err := inv.RenderHTML(); !errors.Is(err, ErrEmptyNumber) {
		t.Errorf("err = %v, want ErrEmptyNumber", err)
	}
}

func TestRenderHTML_RejectsMissingCurrency(t *testing.T) {
	inv, _ := New(sampleInvoice())
	inv.Currency = ""
	if _, err := inv.RenderHTML(); !errors.Is(err, ErrEmptyCurrency) {
		t.Errorf("err = %v, want ErrEmptyCurrency", err)
	}
}

func TestRenderPlainText_RejectsMissingNumber(t *testing.T) {
	inv, _ := New(sampleInvoice())
	inv.Number = ""
	if _, err := inv.RenderPlainText(); !errors.Is(err, ErrEmptyNumber) {
		t.Errorf("err = %v, want ErrEmptyNumber", err)
	}
}

// ---- HTML output -------------------------------------------------------

func TestRenderHTML_ContainsKeyFields(t *testing.T) {
	inv, _ := New(sampleInvoice())
	got, err := inv.RenderHTML()
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	for _, want := range []string{
		"FYR-2026-0042",
		"2026-05-16",
		"2026-06-15",
		"Fyredocs Inc.",
		"Acme Corp",
		"Fyredocs Pro — May 2026",
		"Additional seats (3 × $12.00)",
		"API ops above plan",
		"USD 76.85", // total
		"USD 71.00", // subtotal
		"USD 5.85",  // tax
		"Tax (8.25%)",
		"Thanks for being a customer",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML output missing %q", want)
		}
	}
}

func TestRenderHTML_EscapesPartyAndLineDescriptions(t *testing.T) {
	// XSS-style payload in user-controlled fields must appear
	// HTML-escaped, never as raw markup.
	inv, _ := New(sampleInvoice())
	inv.Customer.Name = `Acme & "Co" <script>x</script>`
	inv.Lines[0].Description = `Pro <b>plan</b>`
	got, err := inv.RenderHTML()
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if strings.Contains(got, "<script>") {
		t.Errorf("unescaped <script> leaked into output: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("script tag should appear escaped; got %q", got)
	}
	if !strings.Contains(got, "&amp;") {
		t.Errorf("ampersand should appear escaped; got %q", got)
	}
	if strings.Contains(got, "Pro <b>plan</b>") {
		t.Errorf("line description not escaped: %q", got)
	}
	if !strings.Contains(got, "Pro &lt;b&gt;plan&lt;/b&gt;") {
		t.Errorf("escaped line description missing: %q", got)
	}
}

func TestRenderHTML_OmitsTaxRowWhenTaxIsZero(t *testing.T) {
	inv := sampleInvoice()
	inv.TaxBps = 0
	computed, _ := New(inv)
	got, err := computed.RenderHTML()
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	if strings.Contains(got, "Tax (") {
		t.Errorf("Tax row should be omitted when TaxBps=0; got %q", got)
	}
}

// ---- Plain-text output -------------------------------------------------

func TestRenderPlainText_ContainsHeaderTotalsAndMemo(t *testing.T) {
	inv, _ := New(sampleInvoice())
	got, err := inv.RenderPlainText()
	if err != nil {
		t.Fatalf("RenderPlainText: %v", err)
	}
	for _, want := range []string{
		"INVOICE FYR-2026-0042",
		"Issued: 2026-05-16",
		"Due:    2026-06-15",
		"FROM:",
		"  Fyredocs Inc.",
		"BILLED TO:",
		"  Acme Corp",
		"Subtotal",
		"Tax (8.25%)",
		"TOTAL",
		"USD 76.85",
		"Thanks for being a customer",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plain-text output missing %q", want)
		}
	}
}

func TestRenderPlainText_WrapsLongDescriptions(t *testing.T) {
	inv := sampleInvoice()
	// 135 chars — must wrap onto three rows. Wrap chunks
	// mid-word at the 50-char column, intentionally simple.
	inv.Lines[0].Description = strings.Repeat("verylongproductnamesegment ", 5)
	got, _ := Compute(inv).RenderPlainText()

	if !strings.Contains(got, "verylongproductnamesegment") {
		t.Fatalf("long description missing entirely: %q", got)
	}
	// A continuation row carries description text but NOT the
	// amount column — i.e., it contains "verylongproductname"
	// somewhere AND does not contain the currency code "USD".
	continuations := 0
	for _, row := range strings.Split(got, "\n") {
		if strings.Contains(row, "verylongproduct") && !strings.Contains(row, "USD") {
			continuations++
		}
	}
	if continuations < 2 {
		t.Errorf("expected ≥2 continuation rows for a 135-char description; got %d in:\n%s", continuations, got)
	}
}

// ---- Money formatting --------------------------------------------------

func TestFormatMoneyCents_CommonCases(t *testing.T) {
	cases := []struct {
		cents    int64
		currency string
		want     string
	}{
		{0, "USD", "USD 0.00"},
		{1, "USD", "USD 0.01"},
		{99, "USD", "USD 0.99"},
		{100, "USD", "USD 1.00"},
		{12345, "USD", "USD 123.45"},
		{-12345, "USD", "USD -123.45"},
		{12345, "eur", "EUR 123.45"},      // normalises case
		{12345, "  GBP ", "GBP 123.45"},   // trims whitespace
		{12345, "", "USD 123.45"},         // defaults to USD when missing
		{12345, "JPY", "JPY 123.45"},      // currency code carried, format approximate
	}
	for _, c := range cases {
		if got := FormatMoneyCents(c.cents, c.currency); got != c.want {
			t.Errorf("FormatMoneyCents(%d, %q) = %q, want %q",
				c.cents, c.currency, got, c.want)
		}
	}
}
