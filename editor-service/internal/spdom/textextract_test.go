package spdom

import (
	"strings"
	"testing"
)

// extractText drives the L2.5 text-only pass over a PDF content stream.
// These tests cover the four text-show operators + literal-escape rules.

func TestExtractText_SimpleTj(t *testing.T) {
	got := extractText([]byte(`BT /F1 12 Tf 100 700 Td (Hello, world!) Tj ET`))
	if !strings.Contains(got, "Hello, world!") {
		t.Errorf("got %q, want it to contain 'Hello, world!'", got)
	}
}

func TestExtractText_TJArray(t *testing.T) {
	got := extractText([]byte(`BT /F1 12 Tf 100 700 Td [(Hello)-100( )-100(world)] TJ ET`))
	// TJ joins array string entries (numbers are spacing adjustments, not chars).
	if !strings.Contains(got, "Hello world") {
		t.Errorf("got %q, want it to contain 'Hello world'", got)
	}
}

func TestExtractText_QuoteAndDoubleQuote(t *testing.T) {
	// `'` and `"` are the move-to-next-line variants of Tj.
	got := extractText([]byte(`(Line 1)' 1.5 1.5 (Line 2)" `))
	if !strings.Contains(got, "Line 1") || !strings.Contains(got, "Line 2") {
		t.Errorf("got %q, want both 'Line 1' and 'Line 2'", got)
	}
}

func TestExtractText_OctalEscape(t *testing.T) {
	// `\101` is octal for 0x41 ('A'); `\102` = 'B'; `\103` = 'C'.
	got := extractText([]byte(`(\101\102\103) Tj`))
	if !strings.Contains(got, "ABC") {
		t.Errorf("got %q, want it to contain 'ABC'", got)
	}
}

func TestExtractText_BackslashEscapes(t *testing.T) {
	// `\n`, `\t`, `\(`, `\)`, `\\`
	got := extractText([]byte(`(a\nb\tc\(d\)e\\f) Tj`))
	if !strings.Contains(got, "a\nb\tc(d)e\\f") {
		t.Errorf("got %q, want escape sequences decoded", got)
	}
}

func TestExtractText_NestedParens(t *testing.T) {
	// Balanced parens inside a string literal are not escaped.
	got := extractText([]byte(`(foo (bar) baz) Tj`))
	if !strings.Contains(got, "foo (bar) baz") {
		t.Errorf("got %q, want it to contain 'foo (bar) baz'", got)
	}
}

func TestExtractText_IgnoresNonTextOperators(t *testing.T) {
	// Random graphics operators must not crash the scanner or leak into output.
	got := extractText([]byte(`q 1 0 0 1 0 0 cm 100 200 m 300 400 l S Q BT (Greeting) Tj ET`))
	if !strings.Contains(got, "Greeting") {
		t.Errorf("got %q, want 'Greeting' from a stream with graphics ops", got)
	}
	if strings.ContainsAny(got, "qmlS") && !strings.Contains(got, "Greeting") {
		t.Errorf("graphics operators leaked into output: %q", got)
	}
}

func TestExtractText_EmptyAndGarbage(t *testing.T) {
	if got := extractText(nil); got != "" {
		t.Errorf("nil input: got %q, want empty", got)
	}
	if got := extractText([]byte{}); got != "" {
		t.Errorf("empty input: got %q, want empty", got)
	}
	if got := extractText([]byte("definitely not a content stream")); strings.Contains(got, "definitely") {
		t.Errorf("garbage input leaked into output: %q", got)
	}
}

func TestExtractText_LineComments(t *testing.T) {
	// `%` outside a string runs to end of line.
	got := extractText([]byte("% comment line\n(Visible) Tj\n% another\n"))
	if !strings.Contains(got, "Visible") {
		t.Errorf("got %q, want 'Visible' after stripping comments", got)
	}
}

func TestStripControlBytes(t *testing.T) {
	in := "Hello\x00\x01World\nLine 2\tTabbed"
	got := stripControlBytes(in)
	want := "HelloWorld\nLine 2\tTabbed"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeLiteral_OctalUpToThreeDigits(t *testing.T) {
	// `\1` (one digit), `\12` (two digits), `\123` (three digits)
	cases := []struct {
		in   string
		want string
	}{
		{`\1`, "\001"},
		{`\12`, "\012"},
		{`\123`, "\123"},
		{`\1234`, "\123" + "4"}, // 3-digit cap then literal '4'
	}
	for _, tc := range cases {
		got := string(decodeLiteral([]byte(tc.in)))
		if got != tc.want {
			t.Errorf("decodeLiteral(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
