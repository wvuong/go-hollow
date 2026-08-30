package hollow

import "fmt"

// readFixedLengthData reads a FixedLengthData section, matching
// FixedLengthElementArray#newFrom / SegmentedLongArray#readFrom: a VarLong
// word count followed by that many big-endian uint64 words, packed
// low-bit-first (see getElementValue).
func readFixedLengthData(r byteReader) ([]uint64, error) {
	numWords, err := readVLong(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading fixed-length data word count: %w", err)
	}

	words := make([]uint64, numWords)
	for i := range words {
		if err := readBigEndian(r, &words[i]); err != nil {
			return nil, fmt.Errorf("hollow: reading fixed-length data word %d: %w", i, err)
		}
	}
	return words, nil
}
