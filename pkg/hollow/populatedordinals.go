package hollow

import "fmt"

// PopulatedOrdinals is the bitset of which ordinals are actually populated
// (vs. "holes" left by removals) for one type-state, matching
// SnapshotPopulatedOrdinalsReader.
type PopulatedOrdinals struct {
	words []uint64
}

// IsPopulated reports whether ordinal is populated.
func (p *PopulatedOrdinals) IsPopulated(ordinal int32) bool {
	word := ordinal / 64
	if int(word) >= len(p.words) {
		return false
	}
	return p.words[word]&(uint64(1)<<uint(ordinal%64)) != 0
}

// Count returns the number of populated ordinals.
func (p *PopulatedOrdinals) Count() int {
	count := 0
	for _, w := range p.words {
		for w != 0 {
			count++
			w &= w - 1
		}
	}
	return count
}

// newPopulatedOrdinals allocates an empty (all-unpopulated) bitset sized to
// hold ordinals up to maxOrdinal, for building up during delta application
// (there's no wire-encoded bitset for deltas — see readTypeStateDelta).
func newPopulatedOrdinals(maxOrdinal int32) *PopulatedOrdinals {
	if maxOrdinal < 0 {
		return &PopulatedOrdinals{}
	}
	return &PopulatedOrdinals{words: make([]uint64, maxOrdinal/64+1)}
}

// set marks ordinal as populated.
func (p *PopulatedOrdinals) set(ordinal int32) {
	p.words[ordinal/64] |= uint64(1) << uint(ordinal%64)
}

// readPopulatedOrdinals reads the populated-ordinals bitset that follows a
// type's shard data in a snapshot, matching
// SnapshotPopulatedOrdinalsReader#readOrdinals.
func readPopulatedOrdinals(r byteReader) (*PopulatedOrdinals, error) {
	var numWords int32
	if err := readBigEndian(r, &numWords); err != nil {
		return nil, fmt.Errorf("hollow: reading populated-ordinals word count: %w", err)
	}

	words := make([]uint64, numWords)
	for i := range words {
		if err := readBigEndian(r, &words[i]); err != nil {
			return nil, fmt.Errorf("hollow: reading populated-ordinals word %d: %w", i, err)
		}
	}
	return &PopulatedOrdinals{words: words}, nil
}
