package spdom

import (
	"bytes"
	"errors"
	"testing"
)

func TestReplaceFirstLiteral_HappyPath(t *testing.T) {
	stream := []byte("BT /F1 12 Tf (hello) Tj ET")
	out, err := ReplaceFirstLiteral(stream, "hello", "world")
	if err != nil {
		t.Fatalf("ReplaceFirstLiteral: %v", err)
	}
	if !bytes.Contains(out, []byte("(world) Tj")) {
		t.Errorf("output missing rewritten literal:\n%s", out)
	}
	if bytes.Contains(out, []byte("hello")) {
		t.Errorf("output still contains original literal:\n%s", out)
	}
}

func TestReplaceFirstLiteral_OnlyFirstMatch(t *testing.T) {
	// Two `(hello) Tj` ops; only the first should be rewritten so
	// callers can do "replace next" via repeated calls if they
	// want — replacing all-at-once is a different semantic that
	// would obscure paginated review flows.
	stream := []byte("(hello) Tj (hello) Tj")
	out, err := ReplaceFirstLiteral(stream, "hello", "world")
	if err != nil {
		t.Fatalf("ReplaceFirstLiteral: %v", err)
	}
	expected := []byte("(world) Tj (hello) Tj")
	if !bytes.Equal(out, expected) {
		t.Errorf("got:\n%s\nwant:\n%s", out, expected)
	}
}

func TestReplaceFirstLiteral_NotFoundReturnsSentinel(t *testing.T) {
	stream := []byte("BT (other) Tj ET")
	_, err := ReplaceFirstLiteral(stream, "hello", "world")
	if !errors.Is(err, ErrLiteralNotFound) {
		t.Errorf("err = %v, want ErrLiteralNotFound", err)
	}
}

func TestReplaceFirstLiteral_HandlesEscapesInTarget(t *testing.T) {
	// Source has `\(`, `\)`, `\\` — the decoder must reduce
	// them to literal `(`, `)`, `\` before comparing.
	stream := []byte(`(say \(hi\)) Tj`)
	out, err := ReplaceFirstLiteral(stream, "say (hi)", "bye")
	if err != nil {
		t.Fatalf("ReplaceFirstLiteral: %v", err)
	}
	if !bytes.Contains(out, []byte("(bye) Tj")) {
		t.Errorf("expected rewritten literal; got:\n%s", out)
	}
}

func TestReplaceFirstLiteral_EscapesReplaceText(t *testing.T) {
	// Inserting text that contains parens or backslash must
	// escape it on the way out — otherwise the resulting PDF is
	// invalid.
	stream := []byte("(plain) Tj")
	out, err := ReplaceFirstLiteral(stream, "plain", `with (parens) and \backslash`)
	if err != nil {
		t.Fatalf("ReplaceFirstLiteral: %v", err)
	}
	want := []byte(`(with \(parens\) and \\backslash) Tj`)
	if !bytes.Contains(out, want) {
		t.Errorf("missing properly-escaped output; got:\n%s\nwant substring:\n%s", out, want)
	}
}

func TestReplaceFirstLiteral_IgnoresLiteralInsideTJArray(t *testing.T) {
	// `(hello)` inside a TJ array is NOT a candidate — we only
	// match plain `Tj`. The array passes through unchanged; the
	// matching `(hello) Tj` after it is the one we rewrite.
	stream := []byte("[(hello) -50 (world)] TJ (hello) Tj")
	out, err := ReplaceFirstLiteral(stream, "hello", "bye")
	if err != nil {
		t.Fatalf("ReplaceFirstLiteral: %v", err)
	}
	if !bytes.Contains(out, []byte("[(hello) -50 (world)] TJ")) {
		t.Errorf("TJ array got mutated unexpectedly:\n%s", out)
	}
	if !bytes.Contains(out, []byte("(bye) Tj")) {
		t.Errorf("plain Tj wasn't rewritten:\n%s", out)
	}
}

func TestReplaceFirstLiteral_HandlesParenInsideLiteral(t *testing.T) {
	// Unescaped parens may nest if balanced. Make sure our
	// scanner doesn't truncate.
	stream := []byte("(outer (inner) more) Tj")
	out, err := ReplaceFirstLiteral(stream, "outer (inner) more", "replaced")
	if err != nil {
		t.Fatalf("ReplaceFirstLiteral: %v", err)
	}
	if !bytes.Contains(out, []byte("(replaced) Tj")) {
		t.Errorf("nested-paren literal mis-bounded; output:\n%s", out)
	}
}

func TestReplaceFirstLiteral_HandlesCommentsBeforeOp(t *testing.T) {
	stream := []byte("(hello) %% comment line\nTj")
	out, err := ReplaceFirstLiteral(stream, "hello", "bye")
	if err != nil {
		t.Fatalf("ReplaceFirstLiteral: %v", err)
	}
	if !bytes.Contains(out, []byte("(bye)")) {
		t.Errorf("comment between literal and Tj broke matching:\n%s", out)
	}
}

func TestReplaceFirstLiteral_DecodesOctalEscape(t *testing.T) {
	// `\101` = 65 = 'A' — verify decoder via the public path:
	// caller passes the decoded form as `find`; the matcher
	// must locate the raw escape sequence.
	stream := []byte(`(\101BC) Tj`)
	out, err := ReplaceFirstLiteral(stream, "ABC", "ok")
	if err != nil {
		t.Fatalf("ReplaceFirstLiteral on octal-escaped literal: %v", err)
	}
	if !bytes.Contains(out, []byte("(ok) Tj")) {
		t.Errorf("octal-escape literal not matched; output:\n%s", out)
	}
}
