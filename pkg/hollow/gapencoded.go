package hollow

import "fmt"

// readGapEncodedOrdinals reads a gap-encoded ascending ordinal list, matching
// GapEncodedVariableLengthIntegerReader: a VarLong byte-length prefix,
// followed by that many bytes of successive VarInt gaps, each added to a
// running total starting at 0 to produce the next ordinal.
//
// Go doesn't need Java's lazy/incremental reader here (that exists to avoid
// materializing large data structures under JVM GC pressure — see
// go-hollow-porting-notes.md §2-4) — decoding eagerly into a plain sorted
// slice is simpler and just as correct.
func readGapEncodedOrdinals(r byteReader) ([]int32, error) {
	numBytes, err := readVLong(r)
	if err != nil {
		return nil, fmt.Errorf("reading gap-encoded ordinals byte count: %w", err)
	}
	if numBytes == 0 {
		return nil, nil
	}

	buf := make([]byte, numBytes)
	if err := readFull(r, buf); err != nil {
		return nil, fmt.Errorf("reading gap-encoded ordinals: %w", err)
	}

	var ordinals []int32
	current := int32(0)
	for pos := 0; pos < len(buf); {
		gap, nextPos, err := readVIntFromBytes(buf, pos)
		if err != nil {
			return nil, fmt.Errorf("decoding gap-encoded ordinal at byte %d: %w", pos, err)
		}
		current += gap
		ordinals = append(ordinals, current)
		pos = nextPos
	}
	return ordinals, nil
}

// readDeltaPrefix reads the part of a delta shard's data that precedes the
// field/pointer data — identical across Object/List/Set/Map — matching the
// isDelta branch shared by every *TypeDataElements#readFromInput: max
// ordinal, then the removed and added ordinal lists.
func readDeltaPrefix(r byteReader) (maxOrdinal int32, removals, additions []int32, err error) {
	maxOrdinal, err = readVInt(r)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("reading delta max ordinal: %w", err)
	}
	removals, err = readGapEncodedOrdinals(r)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("reading delta removals: %w", err)
	}
	additions, err = readGapEncodedOrdinals(r)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("reading delta additions: %w", err)
	}
	return maxOrdinal, removals, additions, nil
}

// ordinalIndex tracks a position within a sorted ascending ordinal slice,
// matching GapEncodedVariableLengthIntegerReader's nextElement()/advance()
// (with a sentinel of "no more elements" once exhausted).
type ordinalIndex struct {
	ordinals []int32
	pos      int
}

// matches reports whether the current element equals ordinal.
func (o *ordinalIndex) matches(ordinal int32) bool {
	return o.pos < len(o.ordinals) && o.ordinals[o.pos] == ordinal
}

// advance moves to the next element.
func (o *ordinalIndex) advance() {
	o.pos++
}
