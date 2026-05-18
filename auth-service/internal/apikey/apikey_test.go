package apikey

import (
	"errors"
	"strings"
	"testing"
)

// TestMain dials the argon2 cost parameters down so the suite stays
// fast. Production keeps the defaults defined in apikey.go.
func TestMain(m *testing.M) {
	argonTime = 1
	argonMemory = 8 * 1024 // 8 MiB is plenty for tests
	argonThreads = 1
	m.Run()
}

func TestGenerate_ProducesWireFormatShape(t *testing.T) {
	g, err := Generate(EnvLive)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(g.Plaintext, "fyr_live_") {
		t.Errorf("Plaintext = %q, want prefix fyr_live_", g.Plaintext)
	}
	parts := strings.Split(g.Plaintext, "_")
	if len(parts) != 4 {
		t.Fatalf("Plaintext has %d parts, want 4", len(parts))
	}
	if len(parts[2]) != PrefixLength {
		t.Errorf("prefix len = %d, want %d", len(parts[2]), PrefixLength)
	}
	if len(parts[3]) != SecretLength {
		t.Errorf("secret len = %d, want %d", len(parts[3]), SecretLength)
	}
	if g.Prefix != parts[2] {
		t.Errorf("Generated.Prefix = %q, doesn't match wire-format prefix %q",
			g.Prefix, parts[2])
	}
	if g.Hash == "" {
		t.Error("Hash must be populated")
	}
	if !strings.HasPrefix(g.Hash, "$argon2id$") {
		t.Errorf("Hash should be argon2id-encoded; got %q", g.Hash)
	}
}

func TestGenerate_TestEnvProducesTestPrefix(t *testing.T) {
	g, err := Generate(EnvTest)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.HasPrefix(g.Plaintext, "fyr_test_") {
		t.Errorf("Plaintext = %q, want fyr_test_ prefix", g.Plaintext)
	}
}

func TestGenerate_RejectsUnknownEnv(t *testing.T) {
	if _, err := Generate("staging"); err == nil {
		t.Error("expected error for unknown environment")
	}
}

func TestGenerate_ProducesUniqueKeys(t *testing.T) {
	// 1024 generations and the prefix should always be unique. Not a
	// real entropy test but a sanity check that we're not leaking a
	// fixed value through a bug.
	seen := make(map[string]bool, 1024)
	for i := 0; i < 1024; i++ {
		g, err := Generate(EnvLive)
		if err != nil {
			t.Fatalf("iter %d: Generate: %v", i, err)
		}
		if seen[g.Prefix] {
			t.Fatalf("iter %d: duplicate prefix %q", i, g.Prefix)
		}
		seen[g.Prefix] = true
	}
}

func TestParse_HappyPath(t *testing.T) {
	g, err := Generate(EnvLive)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	p, err := Parse(g.Plaintext)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Env != EnvLive {
		t.Errorf("Env = %q, want %q", p.Env, EnvLive)
	}
	if p.Prefix != g.Prefix {
		t.Errorf("Prefix = %q, want %q", p.Prefix, g.Prefix)
	}
	if p.Secret == "" {
		t.Error("Secret should be non-empty")
	}
}

func TestParse_RejectsMalformedInputs(t *testing.T) {
	bad := []string{
		"",
		"fyr_",
		"fyr_live_abc",
		"fyr_live_abc_def", // wrong lengths
		"fyr_staging_aaaaaaaaaaaaaa_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // bad env
		"sk_live_aaaaaaaaaaaaaa_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",     // wrong overall prefix
		"fyr_live_aaaaaaaaaaaaaa_aaa",                                 // secret too short
	}
	for _, in := range bad {
		t.Run(in, func(t *testing.T) {
			_, err := Parse(in)
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("Parse(%q) err = %v, want ErrMalformed", in, err)
			}
		})
	}
}

func TestVerify_AcceptsCorrectSecret(t *testing.T) {
	g, err := Generate(EnvLive)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	p, err := Parse(g.Plaintext)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !Verify(p.Secret, g.Hash) {
		t.Error("Verify should accept the issued secret against its own hash")
	}
}

func TestVerify_RejectsWrongSecret(t *testing.T) {
	g, err := Generate(EnvLive)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if Verify("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", g.Hash) {
		t.Error("Verify should reject a different secret")
	}
}

func TestVerify_RejectsGarbageHash(t *testing.T) {
	if Verify("any-secret", "not a hash") {
		t.Error("Verify should reject a malformed hash without panicking")
	}
}

func TestVerify_RejectsEmptyInputs(t *testing.T) {
	if Verify("", "") {
		t.Error("empty inputs should not verify")
	}
}
