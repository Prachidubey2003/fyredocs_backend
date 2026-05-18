package audit

import (
	"bytes"
	"testing"
)

func TestCompute_IsDeterministic(t *testing.T) {
	// Same inputs → same digest, every time. This is the
	// foundational property; if it ever breaks, every existing
	// chain is bricked.
	a := Compute(42, "user-1", "auth.login", "", []byte(`null`), GenesisPrevHash)
	b := Compute(42, "user-1", "auth.login", "", []byte(`null`), GenesisPrevHash)
	if !bytes.Equal(a, b) {
		t.Errorf("Compute is non-deterministic: %x vs %x", a, b)
	}
}

func TestCompute_DiffersOnAnyFieldChange(t *testing.T) {
	// Sanity: flipping any field must change the digest. This is
	// what makes the chain tamper-evident — a row whose actor
	// gets rewritten produces a different hash, and the verifier
	// notices.
	base := Compute(1, "actor", "action", "resource", []byte(`{"k":1}`), GenesisPrevHash)
	cases := []struct {
		name string
		got  []byte
	}{
		{"seq", Compute(2, "actor", "action", "resource", []byte(`{"k":1}`), GenesisPrevHash)},
		{"actor", Compute(1, "actorX", "action", "resource", []byte(`{"k":1}`), GenesisPrevHash)},
		{"action", Compute(1, "actor", "actionX", "resource", []byte(`{"k":1}`), GenesisPrevHash)},
		{"resource", Compute(1, "actor", "action", "resourceX", []byte(`{"k":1}`), GenesisPrevHash)},
		{"metadata", Compute(1, "actor", "action", "resource", []byte(`{"k":2}`), GenesisPrevHash)},
		{"prev_hash", Compute(1, "actor", "action", "resource", []byte(`{"k":1}`), bytes.Repeat([]byte{0xff}, 32))},
	}
	for _, c := range cases {
		if bytes.Equal(base, c.got) {
			t.Errorf("changing %s did not change the digest", c.name)
		}
	}
}

func TestCompute_SeparatorPreventsConfusion(t *testing.T) {
	// "ab" + "c" should not hash the same as "a" + "bc". The
	// `:` separators between fields guarantee that. This
	// pinned property protects against an attacker who could
	// otherwise migrate bytes between adjacent fields without
	// breaking the digest.
	withFirst := Compute(1, "ab", "c", "", nil, GenesisPrevHash)
	withSecond := Compute(1, "a", "bc", "", nil, GenesisPrevHash)
	if bytes.Equal(withFirst, withSecond) {
		t.Error("field-boundary confusion attack succeeded")
	}
}

func TestVerify_IntactChainReturnsOK(t *testing.T) {
	rows := makeChain(t, 5)
	res := Verify(rows)
	if !res.OK {
		t.Errorf("intact chain reported broken: %+v", res)
	}
	if res.Count != 5 {
		t.Errorf("Count = %d, want 5", res.Count)
	}
}

func TestVerify_EmptyChainIsOK(t *testing.T) {
	res := Verify(nil)
	if !res.OK {
		t.Errorf("empty chain should be OK; got %+v", res)
	}
	if res.Count != 0 {
		t.Errorf("Count = %d, want 0", res.Count)
	}
}

func TestVerify_DetectsTamperedHashContent(t *testing.T) {
	rows := makeChain(t, 5)
	// Flip a byte in row 3's metadata. The stored Hash still
	// matches the OLD metadata; recompute against the new
	// metadata must fail.
	rows[2].Metadata = []byte(`{"tampered":true}`)
	res := Verify(rows)
	if res.OK {
		t.Fatal("verifier missed a metadata tamper")
	}
	if res.BrokenAtSeq != 3 {
		t.Errorf("BrokenAtSeq = %d, want 3", res.BrokenAtSeq)
	}
}

func TestVerify_DetectsBrokenPrevHashLinkage(t *testing.T) {
	rows := makeChain(t, 5)
	// Rewrite row 4's prev_hash so it doesn't match row 3's
	// stored hash — simulates a row whose predecessor was
	// deleted and renumbered.
	rows[3].PrevHash = bytes.Repeat([]byte{0xab}, 32)
	res := Verify(rows)
	if res.OK {
		t.Fatal("verifier missed a prev_hash linkage break")
	}
	if res.BrokenAtSeq != 4 {
		t.Errorf("BrokenAtSeq = %d, want 4", res.BrokenAtSeq)
	}
}

func TestVerify_GenesisRowRequiresZeroPrevHash(t *testing.T) {
	rows := makeChain(t, 3)
	rows[0].PrevHash = bytes.Repeat([]byte{0x01}, 32)
	// Recompute row 0's stored hash against the wrong prev so
	// the content-hash check would pass but the genesis-linkage
	// check fails.
	rows[0].Hash = Compute(rows[0].Seq, rows[0].Actor, rows[0].Action,
		rows[0].Resource, rows[0].Metadata, rows[0].PrevHash)
	res := Verify(rows)
	if res.OK {
		t.Fatal("verifier accepted a non-zero genesis prev_hash")
	}
	if res.BrokenAtSeq != 1 {
		t.Errorf("BrokenAtSeq = %d, want 1 (genesis)", res.BrokenAtSeq)
	}
}

// makeChain builds a `length`-row chain by walking Compute
// forward. Test helper for Verify cases — keeps the actual
// chain-building logic that production code uses out of the
// test path so we're only exercising the verifier.
func makeChain(t *testing.T, length int) []Row {
	t.Helper()
	rows := make([]Row, 0, length)
	prev := GenesisPrevHash
	for i := 1; i <= length; i++ {
		md := []byte(`{"i":` + itoa(i) + `}`)
		row := Row{
			Seq:      int64(i),
			Actor:    "actor",
			Action:   "test.event",
			Resource: "",
			Metadata: md,
			PrevHash: prev,
		}
		row.Hash = Compute(row.Seq, row.Actor, row.Action, row.Resource, row.Metadata, row.PrevHash)
		rows = append(rows, row)
		prev = row.Hash
	}
	return rows
}

// itoa is fmt.Sprintf("%d", i) without the import overhead —
// purely cosmetic.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	n := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}
