package hollow

// getElementValue reads a bitsPerElement-wide unsigned value starting at the
// given bit index from a packed array of 64-bit words, matching Java's
// FixedLengthElementArray#getLargeElementValue: fields are packed low-bit
// first within each word, and a field may span two adjacent words.
func getElementValue(words []uint64, bitIndex int64, bitsPerElement int) uint64 {
	whichWord := bitIndex >> 6
	whichBit := uint(bitIndex & 63)

	value := words[whichWord] >> whichBit

	bitsRemaining := 64 - whichBit
	if uint(bitsPerElement) > bitsRemaining {
		value |= words[whichWord+1] << bitsRemaining
	}

	if bitsPerElement == 64 {
		return value
	}
	mask := (uint64(1) << uint(bitsPerElement)) - 1
	return value & mask
}

// nullValueForBits mirrors HollowObjectTypeDataElements's nullValueForField:
// the reserved all-ones sentinel for a field of the given bit width.
func nullValueForBits(bits int) uint64 {
	if bits == 64 {
		return ^uint64(0)
	}
	return (uint64(1) << uint(bits)) - 1
}

// zigzagDecodeInt32 matches com.netflix.hollow.core.memory.encoding.ZigZag#decodeInt.
func zigzagDecodeInt32(v uint32) int32 {
	return int32(v>>1) ^ -int32(v&1)
}

// zigzagDecodeInt64 matches com.netflix.hollow.core.memory.encoding.ZigZag#decodeLong.
func zigzagDecodeInt64(v uint64) int64 {
	return int64(v>>1) ^ -int64(v&1)
}

// nullFloatBits matches HollowObjectWriteRecord.NULL_FLOAT_BITS:
// Float.floatToIntBits(Float.NaN) + 1.
const nullFloatBits uint32 = 0x7fc00001

// nullDoubleBits matches HollowObjectWriteRecord.NULL_DOUBLE_BITS:
// Double.doubleToLongBits(Double.NaN) + 1.
const nullDoubleBits uint64 = 0x7ff8000000000001
