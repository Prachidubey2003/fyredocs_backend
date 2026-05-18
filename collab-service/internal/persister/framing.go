package persister

import (
	"encoding/binary"
	"errors"
	"io"
)

// Wire format for a Persister snapshot:
//
//	repeat:
//	  [4 bytes big-endian uint32: frame length N]
//	  [N bytes: frame payload]
//
// Versioning: not needed yet. Yjs frames are already self-versioned
// inside their bytes (the protocol carries its own message-type
// header), and a single document's snapshot is either readable by
// this version or not — no migration path. If we ever need a
// schema change, prepend a magic + version byte and refuse older
// blobs at Load.

// maxFrameSize bounds an individual frame on Decode to keep a
// malformed prefix from triggering a multi-GB allocation. 8 MiB
// matches the per-room MaxLogBytes cap in internal/room.
const maxFrameSize = 8 * 1024 * 1024

// Encode serialises a slice of frames to the wire format above.
// Returns nil on an empty input — letting callers write zero
// bytes if a room has no history rather than a non-empty
// "0 frames" envelope.
func Encode(frames [][]byte) []byte {
	if len(frames) == 0 {
		return nil
	}
	total := 0
	for _, f := range frames {
		total += 4 + len(f)
	}
	out := make([]byte, 0, total)
	var hdr [4]byte
	for _, f := range frames {
		binary.BigEndian.PutUint32(hdr[:], uint32(len(f)))
		out = append(out, hdr[:]...)
		out = append(out, f...)
	}
	return out
}

// Decode reads frames from r until EOF. A truncated trailing frame
// returns the frames decoded so far PLUS an error — callers can
// choose to ignore the error and use the partial decode (Yjs
// idempotency makes partial replays safe).
//
// A frame larger than maxFrameSize is treated as a malformed
// envelope and the function returns immediately with whatever was
// decoded up to that point.
func Decode(r io.Reader) ([][]byte, error) {
	var frames [][]byte
	var hdr [4]byte
	for {
		_, err := io.ReadFull(r, hdr[:])
		if errors.Is(err, io.EOF) {
			return frames, nil
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return frames, io.ErrUnexpectedEOF
		}
		if err != nil {
			return frames, err
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n > maxFrameSize {
			return frames, errors.New("persister: frame exceeds maxFrameSize")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			// Truncated frame body — keep what we have, signal
			// the caller.
			return frames, io.ErrUnexpectedEOF
		}
		frames = append(frames, buf)
	}
}
