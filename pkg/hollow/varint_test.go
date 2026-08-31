package hollow

import (
	"bytes"
	"errors"
	"testing"
)

func TestReadVInt(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		want  int32
	}{
		{"single byte zero", []byte{0x00}, 0},
		{"single byte max", []byte{0x7F}, 127},
		{"two bytes min", []byte{0x81, 0x00}, 128},
		// 0x88 0x0f: real bytes from fakehollowdata/snapshot header (the
		// "old bytes to skip" VarInt), known to decode to 1039.
		{"known real fixture bytes", []byte{0x88, 0x0f}, 1039},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readVInt(bytes.NewReader(c.bytes))
			if err != nil {
				t.Fatalf("readVInt(%x) returned error: %v", c.bytes, err)
			}
			if got != c.want {
				t.Errorf("readVInt(%x) = %d, want %d", c.bytes, got, c.want)
			}
		})
	}
}

func TestReadVIntNullSentinel(t *testing.T) {
	_, err := readVInt(bytes.NewReader([]byte{0x80}))
	if !errors.Is(err, ErrNullVarInt) {
		t.Errorf("readVInt(0x80) error = %v, want ErrNullVarInt", err)
	}
}

func TestReadVIntEOF(t *testing.T) {
	if _, err := readVInt(bytes.NewReader(nil)); err == nil {
		t.Error("readVInt on empty input: expected error, got nil")
	}
	// Continuation bit set but no following byte.
	if _, err := readVInt(bytes.NewReader([]byte{0x81})); err == nil {
		t.Error("readVInt on truncated multi-byte input: expected error, got nil")
	}
}

func TestReadVLong(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		want  int64
	}{
		{"single byte zero", []byte{0x00}, 0},
		{"single byte max", []byte{0x7F}, 127},
		{"two bytes min", []byte{0x81, 0x00}, 128},
		{"known real fixture bytes", []byte{0x88, 0x0f}, 1039},
		// Five-byte encoding spanning well beyond a 32-bit VarInt's range.
		{"large value", []byte{0x8F, 0xFF, 0xFF, 0xFF, 0x7F}, 0xFFFFFFFF},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readVLong(bytes.NewReader(c.bytes))
			if err != nil {
				t.Fatalf("readVLong(%x) returned error: %v", c.bytes, err)
			}
			if got != c.want {
				t.Errorf("readVLong(%x) = %d, want %d", c.bytes, got, c.want)
			}
		})
	}
}

// readVIntFromBytes is a separate implementation from readVInt (byte-slice
// based, used for decoding STRING fields), so it needs its own coverage of
// the same encoding.
func TestReadVIntFromBytes(t *testing.T) {
	cases := []struct {
		name    string
		bytes   []byte
		want    int32
		wantPos int
	}{
		{"single byte zero", []byte{0x00}, 0, 1},
		{"single byte max", []byte{0x7F}, 127, 1},
		{"two bytes min", []byte{0x81, 0x00}, 128, 2},
		{"known real fixture bytes", []byte{0x88, 0x0f}, 1039, 2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, nextPos, err := readVIntFromBytes(c.bytes, 0)
			if err != nil {
				t.Fatalf("readVIntFromBytes(%x, 0) returned error: %v", c.bytes, err)
			}
			if got != c.want || nextPos != c.wantPos {
				t.Errorf("readVIntFromBytes(%x, 0) = (%d, %d), want (%d, %d)", c.bytes, got, nextPos, c.want, c.wantPos)
			}
		})
	}
}

func TestReadVIntFromBytesNullSentinel(t *testing.T) {
	_, _, err := readVIntFromBytes([]byte{0x80}, 0)
	if !errors.Is(err, ErrNullVarInt) {
		t.Errorf("readVIntFromBytes(0x80) error = %v, want ErrNullVarInt", err)
	}
}
