package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// withTempXDG overrides $XDG_CONFIG_HOME for the duration of the
// test and restores the prior value on cleanup. Avoids touching
// the real home directory.
func withTempXDG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestLoad_ReturnsErrNotLoggedInWhenFileMissing(t *testing.T) {
	withTempXDG(t)
	_, err := Load()
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("err = %v, want ErrNotLoggedIn", err)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	root := withTempXDG(t)
	in := &Config{APIKey: "fyr_live_abc_secret", BaseURL: "https://api.example.com"}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// File should live at $XDG_CONFIG_HOME/fyredocs/config.json
	got, err := os.Stat(filepath.Join(root, "fyredocs", "config.json"))
	if err != nil {
		t.Fatalf("config file missing: %v", err)
	}
	if perm := got.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file perms = %o, want 0600 (api keys are secrets)", perm)
	}
	out, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.APIKey != in.APIKey {
		t.Errorf("APIKey roundtrip: got %q, want %q", out.APIKey, in.APIKey)
	}
	if out.BaseURL != in.BaseURL {
		t.Errorf("BaseURL roundtrip: got %q, want %q", out.BaseURL, in.BaseURL)
	}
}

func TestLoad_DefaultsBaseURLWhenOmitted(t *testing.T) {
	withTempXDG(t)
	if err := Save(&Config{APIKey: "fyr_test_1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.BaseURL != DefaultBaseURL {
		t.Errorf("BaseURL = %q, want default %q", out.BaseURL, DefaultBaseURL)
	}
}

func TestLoad_TreatsEmptyAPIKeyAsNotLoggedIn(t *testing.T) {
	root := withTempXDG(t)
	// Write a config file with no apiKey — Load should treat
	// it as "logged out" rather than handing back an empty token.
	path := filepath.Join(root, "fyredocs", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"apiKey":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("err = %v, want ErrNotLoggedIn", err)
	}
}

func TestDelete_Idempotent(t *testing.T) {
	withTempXDG(t)
	// Delete with no file present — must not error.
	if err := Delete(); err != nil {
		t.Errorf("Delete on missing file should be nil; got %v", err)
	}
	// Now create + delete + delete again.
	if err := Save(&Config{APIKey: "fyr_test_1"}); err != nil {
		t.Fatal(err)
	}
	if err := Delete(); err != nil {
		t.Errorf("first Delete failed: %v", err)
	}
	if err := Delete(); err != nil {
		t.Errorf("second Delete should be idempotent; got %v", err)
	}
}

func TestSave_AtomicReplace(t *testing.T) {
	// Save writes a *.tmp then renames — confirm the .tmp file
	// doesn't survive (a leftover .tmp would suggest a crash
	// during the rename, which would leave readers seeing stale
	// content).
	root := withTempXDG(t)
	if err := Save(&Config{APIKey: "fyr_v1"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(&Config{APIKey: "fyr_v2"}); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(root, "fyredocs", "config.json.tmp")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("leftover .tmp file after Save: %v", err)
	}
}
