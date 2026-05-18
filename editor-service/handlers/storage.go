package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// StorageDir is the on-disk root under which editor-service reads
// document bytes and writes revision bytes. Workers that produced the
// original document write under this same root (see plan §4.4.3).
//
// Set this from main() with the EDITOR_STORAGE_DIR (or FILES_DIR)
// environment value; tests override it via a t.TempDir() and the
// SetStorageDir helper below.
//
// We keep it as a package-level var rather than threading it through
// every handler signature because (a) it never changes at runtime
// after startup, (b) the handler signatures here are gin-handler-
// shaped and any change to them ripples through routes.go, and (c) the
// rest of the editor-service config (DB handle, NATS conn, JWT keys)
// follows the same package-var pattern.
var StorageDir string

// SetStorageDir is a thin setter exposed for tests. Production code
// should assign StorageDir directly in main().
func SetStorageDir(dir string) { StorageDir = dir }

// resolveStoragePath turns a Document.StorageKey (or any other
// relative storage key) into the path the handler can hand to
// os.Open / os.WriteFile.
//
// It refuses:
//   - empty / whitespace-only keys,
//   - absolute paths,
//   - keys that resolve outside their root after [filepath.Clean]
//     (e.g. "../etc/passwd", "users/../../etc/passwd").
//
// When [StorageDir] is configured, the returned path is absolute
// (joined under StorageDir). When StorageDir is unset, the returned
// path is the cleaned relative key — this preserves the original
// behavior used by handlers like [GetSPDOM] which run with cwd =
// /files in deployment and a relative key in tests.
//
// Refusing path traversal here is belt-and-braces — uploads must
// sanitise on the way in too (see shared/storage.SafeFileName) — but
// it ensures a corrupt or hostile Document row cannot trick a handler
// into reading an arbitrary file.
func resolveStoragePath(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("storage: empty storage key")
	}
	if filepath.IsAbs(key) {
		return "", fmt.Errorf("storage: refusing absolute storage key %q", key)
	}
	cleaned := filepath.Clean(key)
	// After Clean, any escape attempt remains rooted at "..".
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("storage: refusing path traversal %q", key)
	}

	if StorageDir == "" {
		return cleaned, nil
	}
	full := filepath.Join(StorageDir, cleaned)
	cleanFull := filepath.Clean(full)
	cleanRoot := filepath.Clean(StorageDir)
	rootPrefix := cleanRoot + string(os.PathSeparator)
	if cleanFull != cleanRoot && !strings.HasPrefix(cleanFull, rootPrefix) {
		return "", fmt.Errorf("storage: refusing path traversal %q (resolves outside %q)", key, cleanRoot)
	}
	return cleanFull, nil
}

// revisionStorageKey is the relative path under StorageDir where a new
// revision's PDF bytes live. Per plan §4.4.3 we group by owner/doc.
//
//	<owner-namespace>/docs/<docId>/edits/<revId>.pdf
//
// We use the user-namespace prefix to match what shared/storage.OwnerForUser
// emits for the rest of the platform. Tests construct the same shape so the
// path-traversal guard in resolveStoragePath is exercised end to end.
func revisionStorageKey(ownerUserID, docID, revID uuid.UUID) string {
	return filepath.Join(
		"users", ownerUserID.String(),
		"docs", docID.String(),
		"edits", revID.String()+".pdf",
	)
}
