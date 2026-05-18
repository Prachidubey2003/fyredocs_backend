// Package audit implements the hash-chain primitive that makes
// the `audit_events` table tamper-evident.
//
// Format (see also models.AuditEvent doc):
//
//	hash = sha256(
//	    decimal(seq) || ":" || actor || ":" || action || ":" ||
//	    resource    || ":" || metadata || ":" || hex(prev_hash)
//	)
//
// The `:` separators prevent length-extension confusion between
// fields (e.g., actor="ab" + action="c" should not hash the same
// as actor="a" + action="bc"). hex(prev_hash) keeps the digest
// printable so the formula reads cleanly in audit-log dumps.
//
// The genesis row has prev_hash = 32 zero bytes. The verifier
// rejects any row whose computed hash doesn't match its stored
// hash; it returns the seq of the first broken link so on-call
// can localise tampering.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// GenesisPrevHash is the prev_hash value the very first row uses.
// 32 zero bytes — distinguishable from a real digest (which is
// effectively never all-zero with overwhelming probability).
var GenesisPrevHash = make([]byte, sha256.Size)

// Compute returns the chain hash for the given fields. The same
// inputs always produce the same digest — the verifier uses this
// to detect tampering, and the inserter uses it to write the
// chain forward.
func Compute(seq int64, actor, action, resource string, metadata []byte, prevHash []byte) []byte {
	h := sha256.New()
	h.Write([]byte(strconv.FormatInt(seq, 10)))
	h.Write([]byte{':'})
	h.Write([]byte(actor))
	h.Write([]byte{':'})
	h.Write([]byte(action))
	h.Write([]byte{':'})
	h.Write([]byte(resource))
	h.Write([]byte{':'})
	h.Write(metadata)
	h.Write([]byte{':'})
	h.Write([]byte(hex.EncodeToString(prevHash)))
	return h.Sum(nil)
}

// Row is the field-subset Verify needs from a stored AuditEvent.
// Defined here (not in models) so the audit package stays
// dependency-free of GORM — that keeps the chain logic pure and
// the tests cheap.
type Row struct {
	Seq      int64
	Actor    string
	Action   string
	Resource string
	Metadata []byte
	PrevHash []byte
	Hash     []byte
}

// VerifyResult is the outcome of walking the chain.
type VerifyResult struct {
	// OK is true when the entire chain is intact.
	OK bool
	// BrokenAtSeq is the seq of the FIRST row whose computed
	// hash didn't match its stored hash, or whose prev_hash
	// didn't match the previous row's hash. Zero when OK is true.
	BrokenAtSeq int64
	// Reason is a human-readable explanation of the break. Empty
	// when OK is true.
	Reason string
	// Count is the number of rows verified (i.e., where the
	// chain was intact through). Useful for "the chain is good
	// up to and including row N" reporting.
	Count int64
}

// Verify walks `rows` in seq order and confirms each row's hash
// and the prev_hash linkage. Returns a VerifyResult that
// pinpoints the first tampered row (if any).
//
// Pre-condition: rows MUST be sorted by Seq ascending. The
// caller (handlers/audit.go) is responsible for the SQL ORDER
// BY; this function only checks the chain math, not row order.
func Verify(rows []Row) VerifyResult {
	prevHash := GenesisPrevHash
	for _, r := range rows {
		// prev_hash linkage: the stored prev_hash must equal
		// the previous row's stored hash (or genesis for row 1).
		if !equalBytes(r.PrevHash, prevHash) {
			return VerifyResult{
				BrokenAtSeq: r.Seq,
				Reason:      "prev_hash does not match previous row's hash",
				Count:       r.Seq - 1,
			}
		}
		// Hash content: recompute from the row's fields and
		// confirm it matches the stored hash.
		want := Compute(r.Seq, r.Actor, r.Action, r.Resource, r.Metadata, r.PrevHash)
		if !equalBytes(r.Hash, want) {
			return VerifyResult{
				BrokenAtSeq: r.Seq,
				Reason:      "computed hash does not match stored hash",
				Count:       r.Seq - 1,
			}
		}
		prevHash = r.Hash
	}
	return VerifyResult{OK: true, Count: int64(len(rows))}
}

// equalBytes is a stdlib-style constant-time equality check.
// Audit verification isn't in a side-channel-attack regime
// (auditors run it against trusted DB snapshots), but using
// constant-time comparison is the right habit for hash-equality
// code that might one day live in a more adversarial context.
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
