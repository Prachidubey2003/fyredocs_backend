package persister

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestEncode_EmptyReturnsNil(t *testing.T) {
	if got := Encode(nil); got != nil {
		t.Errorf("Encode(nil) = %v, want nil", got)
	}
	if got := Encode([][]byte{}); got != nil {
		t.Errorf("Encode([]) = %v, want nil", got)
	}
}

func TestEncode_LengthPrefix(t *testing.T) {
	out := Encode([][]byte{[]byte("hi"), []byte("world")})
	// Expected layout: [0,0,0,2,'h','i', 0,0,0,5,'w','o','r','l','d']
	want := []byte{
		0, 0, 0, 2, 'h', 'i',
		0, 0, 0, 5, 'w', 'o', 'r', 'l', 'd',
	}
	if !bytes.Equal(out, want) {
		t.Errorf("Encode = %v, want %v", out, want)
	}
}

func TestRoundTrip(t *testing.T) {
	frames := [][]byte{
		[]byte("first"),
		{}, // empty frame — must round-trip
		[]byte("third"),
	}
	encoded := Encode(frames)
	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(decoded) != len(frames) {
		t.Fatalf("Decode returned %d frames, want %d", len(decoded), len(frames))
	}
	for i, f := range frames {
		if !bytes.Equal(decoded[i], f) {
			t.Errorf("frame %d = %q, want %q", i, decoded[i], f)
		}
	}
}

func TestDecode_EmptyInput(t *testing.T) {
	got, err := Decode(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Decode of empty input returned err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Decode of empty input returned %d frames, want 0", len(got))
	}
}

func TestDecode_TruncatedBodyReturnsPartial(t *testing.T) {
	// Encode two frames, then chop off the last 2 bytes of the
	// second frame's body. Decode should hand us frame 1 plus an
	// ErrUnexpectedEOF.
	full := Encode([][]byte{[]byte("hello"), []byte("world")})
	truncated := full[:len(full)-2]
	got, err := Decode(bytes.NewReader(truncated))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want ErrUnexpectedEOF", err)
	}
	if len(got) != 1 || !bytes.Equal(got[0], []byte("hello")) {
		t.Errorf("partial decode = %v, want [hello]", got)
	}
}

func TestDecode_RejectsOversizedFrame(t *testing.T) {
	// Craft a header that claims a frame larger than the cap.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(maxFrameSize+1))
	_, err := Decode(bytes.NewReader(hdr[:]))
	if err == nil {
		t.Error("Decode should reject oversized frame")
	}
}
