package persister

import (
	"bytes"
	"testing"
)

func TestNoop_LoadReturnsNil(t *testing.T) {
	var p Persister = Noop{}
	if got := p.Load("doc-1"); got != nil {
		t.Errorf("Noop.Load = %v, want nil", got)
	}
}

func TestNoop_SaveDoesNotPanic(t *testing.T) {
	var p Persister = Noop{}
	p.Save("doc-1", [][]byte{[]byte("ignored")})
	// Survived without panic — that's the whole assertion.
}

func TestInMemory_RoundTrip(t *testing.T) {
	p := NewInMemory()
	frames := [][]byte{[]byte("hello"), []byte("world")}
	p.Save("doc-1", frames)
	got := p.Load("doc-1")
	if len(got) != 2 || !bytes.Equal(got[0], []byte("hello")) || !bytes.Equal(got[1], []byte("world")) {
		t.Errorf("Load after Save = %v, want [hello world]", got)
	}
}

func TestInMemory_LoadReturnsNilForUnknownDoc(t *testing.T) {
	p := NewInMemory()
	if got := p.Load("never-saved"); got != nil {
		t.Errorf("Load(unknown) = %v, want nil", got)
	}
}

func TestInMemory_SaveOverwrites(t *testing.T) {
	p := NewInMemory()
	p.Save("doc-1", [][]byte{[]byte("v1")})
	p.Save("doc-1", [][]byte{[]byte("v2")})
	got := p.Load("doc-1")
	if len(got) != 1 || !bytes.Equal(got[0], []byte("v2")) {
		t.Errorf("after overwrite Load = %v, want [v2]", got)
	}
}

func TestInMemory_LoadCopiesFrames(t *testing.T) {
	// Mutating the returned slice must not affect the stored
	// snapshot. This prevents accidental corruption of the
	// "persisted" state when a caller (e.g. the room's appendLog
	// path) reuses byte buffers.
	p := NewInMemory()
	p.Save("doc-1", [][]byte{[]byte("original")})
	got := p.Load("doc-1")
	got[0][0] = 'X'
	again := p.Load("doc-1")
	if !bytes.Equal(again[0], []byte("original")) {
		t.Errorf("snapshot mutated through aliasing: %q", again[0])
	}
}

func TestInMemory_SaveCopiesFrames(t *testing.T) {
	// Symmetric guard: mutating the source slice after Save
	// must not affect the stored snapshot either.
	p := NewInMemory()
	src := []byte("original")
	p.Save("doc-1", [][]byte{src})
	src[0] = 'X'
	got := p.Load("doc-1")
	if !bytes.Equal(got[0], []byte("original")) {
		t.Errorf("snapshot mutated through aliasing on Save: %q", got[0])
	}
}

func TestInMemory_HasReportsExistence(t *testing.T) {
	p := NewInMemory()
	if p.Has("doc-1") {
		t.Error("Has before Save should be false")
	}
	p.Save("doc-1", [][]byte{[]byte("x")})
	if !p.Has("doc-1") {
		t.Error("Has after Save should be true")
	}
}
