package hollow

import "testing"

func TestGetElementValueWithinOneWord(t *testing.T) {
	words := []uint64{0b1011} // low 4 bits = 0b1011

	if got := getElementValue(words, 0, 4); got != 0b1011 {
		t.Errorf("getElementValue(bitIndex=0, bits=4) = %d, want %d", got, 0b1011)
	}

	// bits 4-7 of 0xF0 (11110000) = 0b1111
	words = []uint64{0xF0}
	if got := getElementValue(words, 4, 4); got != 0xF {
		t.Errorf("getElementValue(bitIndex=4, bits=4) = %d, want %d", got, 0xF)
	}
}

func TestGetElementValueCrossesWordBoundary(t *testing.T) {
	// word0's top nibble (bits 60-63) = 0xF, word1's bottom nibble = 0xA.
	// Reading 8 bits starting at bit 60 should combine word0's remaining
	// 4 bits (low nibble of the result) with word1's low 4 bits (high
	// nibble of the result): 0xAF.
	words := []uint64{0xF << 60, 0xA}

	got := getElementValue(words, 60, 8)
	want := uint64(0xAF)
	if got != want {
		t.Errorf("getElementValue(bitIndex=60, bits=8) = 0x%X, want 0x%X", got, want)
	}
}

func TestGetElementValueFull64Bits(t *testing.T) {
	words := []uint64{0xDEADBEEFCAFEBABE}
	if got := getElementValue(words, 0, 64); got != words[0] {
		t.Errorf("getElementValue(bitIndex=0, bits=64) = 0x%X, want 0x%X", got, words[0])
	}
}

func TestNullValueForBits(t *testing.T) {
	cases := []struct {
		bits int
		want uint64
	}{
		{1, 0x1},
		{15, 0x7FFF},
		{64, ^uint64(0)},
	}
	for _, c := range cases {
		if got := nullValueForBits(c.bits); got != c.want {
			t.Errorf("nullValueForBits(%d) = 0x%X, want 0x%X", c.bits, got, c.want)
		}
	}
}

func TestZigzagDecodeInt32(t *testing.T) {
	cases := []struct {
		in   uint32
		want int32
	}{
		{0, 0},
		{1, -1},
		{2, 1},
		{3, -2},
		{4, 2},
	}
	for _, c := range cases {
		if got := zigzagDecodeInt32(c.in); got != c.want {
			t.Errorf("zigzagDecodeInt32(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestZigzagDecodeInt64(t *testing.T) {
	cases := []struct {
		in   uint64
		want int64
	}{
		{0, 0},
		{1, -1},
		{2, 1},
		{3, -2},
		{4, 2},
	}
	for _, c := range cases {
		if got := zigzagDecodeInt64(c.in); got != c.want {
			t.Errorf("zigzagDecodeInt64(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
