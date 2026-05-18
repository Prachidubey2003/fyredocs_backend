package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
)

// SHA256HexLen is the length in characters of a lowercase hex-encoded
// SHA-256 digest. Stored as a CHAR(64)-equivalent in Postgres.
const SHA256HexLen = 64

// HashStream reads r to EOF and returns the lowercase hex SHA-256 digest
// of the bytes consumed plus the total byte count.
//
// Callers that already stream bytes elsewhere (e.g., to disk) should prefer
// TeeHasher to avoid a second read.
func HashStream(r io.Reader) (string, int64, error) {
	if r == nil {
		return "", 0, errors.New("storage.HashStream: nil reader")
	}
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, fmt.Errorf("storage.HashStream: copy: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// HashFile opens path and returns its lowercase hex SHA-256 digest plus the
// file size in bytes. The file handle is closed before HashFile returns.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("storage.HashFile: open %s: %w", path, err)
	}
	defer f.Close()
	return HashStream(f)
}

// Verify reads path and confirms its SHA-256 matches expectedHex
// (case-insensitive). Returns nil on match, ErrChecksumMismatch on mismatch,
// or a wrapped I/O error.
//
// Use this for periodic verification jobs (deployment/storage/verify-checksums.sh)
// and for high-stakes reads (signing, audit export, customer download).
func Verify(path, expectedHex string) error {
	if len(expectedHex) != SHA256HexLen {
		return fmt.Errorf("storage.Verify: bad expected length %d, want %d", len(expectedHex), SHA256HexLen)
	}
	got, _, err := HashFile(path)
	if err != nil {
		return err
	}
	if !equalFold(got, expectedHex) {
		return fmt.Errorf("%w: file=%s want=%s got=%s", ErrChecksumMismatch, path, expectedHex, got)
	}
	return nil
}

// ErrChecksumMismatch is returned by Verify when the on-disk file's hash
// does not match the expected value. Callers should treat this as a
// data-integrity incident: quarantine the file, alert ops, restore from
// the most recent verified backup.
var ErrChecksumMismatch = errors.New("storage: sha256 checksum mismatch")

// TeeHasher wraps r so that bytes read through it are also fed into a
// rolling SHA-256. Use this when you're already piping bytes somewhere
// else (e.g., os.Create + io.Copy):
//
//	dst, _ := os.Create(path)
//	tee, hasher := storage.TeeHasher(src)
//	if _, err := io.Copy(dst, tee); err != nil { ... }
//	digest, n := hasher.Sum()
//
// One read, one pass over the bytes — no double I/O.
func TeeHasher(r io.Reader) (io.Reader, *Hasher) {
	hasher := &Hasher{h: sha256.New()}
	return io.TeeReader(&countingReader{r: r, h: hasher}, hasher.h), hasher
}

// Hasher tracks the running SHA-256 and byte count seen by a TeeHasher.
type Hasher struct {
	h hash.Hash
	n int64
}

// Sum returns the lowercase hex digest and total bytes hashed.
// Safe to call after the underlying read completes.
func (h *Hasher) Sum() (string, int64) {
	return hex.EncodeToString(h.h.Sum(nil)), h.n
}

type countingReader struct {
	r io.Reader
	h *Hasher
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.h.n += int64(n)
	return n, err
}

// equalFold compares two hex strings case-insensitively without allocating.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 32
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}
