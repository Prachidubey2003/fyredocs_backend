package storage

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const helloSHA256 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func TestHashStream_KnownVector(t *testing.T) {
	got, n, err := HashStream(strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("HashStream: %v", err)
	}
	if got != helloSHA256 {
		t.Errorf("digest: got %s, want %s", got, helloSHA256)
	}
	if n != 5 {
		t.Errorf("size: got %d, want 5", n)
	}
}

func TestHashStream_Empty(t *testing.T) {
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got, n, err := HashStream(strings.NewReader(""))
	if err != nil {
		t.Fatalf("HashStream: %v", err)
	}
	if got != emptySHA256 {
		t.Errorf("digest: got %s, want %s", got, emptySHA256)
	}
	if n != 0 {
		t.Errorf("size: got %d, want 0", n)
	}
}

func TestHashStream_NilReader(t *testing.T) {
	_, _, err := HashStream(nil)
	if err == nil {
		t.Fatal("expected error for nil reader")
	}
}

func TestHashFile_KnownVector(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, n, err := HashFile(path)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	if got != helloSHA256 {
		t.Errorf("digest: got %s, want %s", got, helloSHA256)
	}
	if n != 5 {
		t.Errorf("size: got %d, want 5", n)
	}
}

func TestHashFile_Missing(t *testing.T) {
	_, _, err := HashFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestVerify_Match(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(path, helloSHA256); err != nil {
		t.Errorf("Verify: %v", err)
	}
	// case-insensitive
	if err := Verify(path, strings.ToUpper(helloSHA256)); err != nil {
		t.Errorf("Verify uppercase: %v", err)
	}
}

func TestVerify_Mismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrong := strings.Repeat("0", 64)
	err := Verify(path, wrong)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("expected ErrChecksumMismatch, got %v", err)
	}
}

func TestVerify_BadLength(t *testing.T) {
	err := Verify("/tmp/anything", "abc")
	if err == nil || errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("expected length error, got %v", err)
	}
}

func TestTeeHasher_MatchesDirectHash(t *testing.T) {
	payload := []byte("the quick brown fox jumps over the lazy dog")
	var sink bytes.Buffer
	tee, hasher := TeeHasher(bytes.NewReader(payload))

	if _, err := io.Copy(&sink, tee); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if !bytes.Equal(sink.Bytes(), payload) {
		t.Errorf("sink bytes diverged from source")
	}

	gotHex, gotN := hasher.Sum()
	wantHex, wantN, err := HashStream(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if gotHex != wantHex {
		t.Errorf("tee digest: got %s, want %s", gotHex, wantHex)
	}
	if gotN != wantN {
		t.Errorf("tee size: got %d, want %d", gotN, wantN)
	}
	if gotN != int64(len(payload)) {
		t.Errorf("tee size != payload size: got %d, want %d", gotN, len(payload))
	}
}

func TestSHA256HexLen(t *testing.T) {
	if SHA256HexLen != 64 {
		t.Errorf("SHA256HexLen = %d, want 64", SHA256HexLen)
	}
}
