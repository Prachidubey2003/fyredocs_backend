// Package storage provides primitive helpers for the local-filesystem storage
// layer described in docs/developer/architecture/STORAGE.md.
//
// The package is deliberately small and side-effect-free:
//   - hash.go: SHA-256 helpers used to record + verify integrity of files on
//     disk (the values are persisted in the file_metadata.sha256_hash column).
//   - paths.go: per-owner path builders that enforce the §4.4.3 directory
//     convention (users/<user_id>/... and guests/<job_id>/...) so every worker
//     producing files lays them out the same way.
//
// These helpers contain no business logic — they are pure I/O + string
// manipulation — so they live in shared/ rather than duplicated across
// services (consistent with CLAUDE.md §2 allowed shared utilities).
//
// Workers adopt the helpers incrementally; legacy paths continue to work
// during the migration window.
package storage
