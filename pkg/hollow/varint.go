package hollow

import (
	"errors"
	"io"
)

// ErrNullVarInt is returned when a VarInt read encounters the reserved
// null-sentinel byte (0x80) as the first byte of the encoding.
var ErrNullVarInt = errors.New("hollow: attempted to read null value as VarInt")

// readVInt reads a Hollow-encoded variable-length integer, matching
// com.netflix.hollow.core.memory.encoding.VarInt#readVInt.
//
// Encoding: big-endian groups of 7 bits, high bit (0x80) set on every byte
// except the last. A single 0x80 byte is a reserved "null" sentinel.
func readVInt(r io.ByteReader) (int32, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	if b == 0x80 {
		return 0, ErrNullVarInt
	}

	value := int32(b & 0x7F)
	for b&0x80 != 0 {
		b, err = r.ReadByte()
		if err != nil {
			return 0, err
		}
		value = (value << 7) | int32(b&0x7F)
	}

	return value, nil
}

// readVLong reads a Hollow-encoded variable-length long, matching
// com.netflix.hollow.core.memory.encoding.VarInt#readVLong.
func readVLong(r io.ByteReader) (int64, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	if b == 0x80 {
		return 0, ErrNullVarInt
	}

	value := int64(b & 0x7F)
	for b&0x80 != 0 {
		b, err = r.ReadByte()
		if err != nil {
			return 0, err
		}
		value = (value << 7) | int64(b&0x7F)
	}

	return value, nil
}
