package hollow

import (
	"encoding/binary"
	"fmt"
	"io"
	"unicode/utf16"
)

// readBigEndian reads a fixed-width big-endian value, matching Java
// DataInputStream's readShort/readInt/readLong.
func readBigEndian(r io.Reader, v any) error {
	return binary.Read(r, binary.BigEndian, v)
}

// readUTF reads a string encoded in Java's "modified UTF-8" format, matching
// java.io.DataInputStream#readUTF: a big-endian uint16 byte-length prefix
// (not a character count), followed by that many modified-UTF-8 bytes.
//
// Modified UTF-8 differs from standard UTF-8 in two ways: the null character
// (U+0000) is encoded as the two bytes 0xC0 0x80 instead of a single 0x00
// byte, and characters above the Basic Multilingual Plane are encoded as a
// surrogate pair, each half written as its own 3-byte sequence.
func readUTF(r io.Reader) (string, error) {
	var length uint16
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return "", err
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}

	return decodeModifiedUTF8(buf)
}

func decodeModifiedUTF8(buf []byte) (string, error) {
	units := make([]uint16, 0, len(buf))

	for i := 0; i < len(buf); {
		b0 := buf[i]
		switch {
		case b0&0x80 == 0: // 0xxxxxxx
			units = append(units, uint16(b0))
			i++
		case b0&0xE0 == 0xC0: // 110xxxxx 10xxxxxx
			if i+1 >= len(buf) {
				return "", fmt.Errorf("hollow: truncated modified UTF-8 sequence at byte %d", i)
			}
			b1 := buf[i+1]
			units = append(units, (uint16(b0&0x1F)<<6)|uint16(b1&0x3F))
			i += 2
		case b0&0xF0 == 0xE0: // 1110xxxx 10xxxxxx 10xxxxxx
			if i+2 >= len(buf) {
				return "", fmt.Errorf("hollow: truncated modified UTF-8 sequence at byte %d", i)
			}
			b1, b2 := buf[i+1], buf[i+2]
			units = append(units, (uint16(b0&0x0F)<<12)|(uint16(b1&0x3F)<<6)|uint16(b2&0x3F))
			i += 3
		default:
			return "", fmt.Errorf("hollow: invalid modified UTF-8 lead byte 0x%02x at byte %d", b0, i)
		}
	}

	return string(utf16.Decode(units)), nil
}
