package hollow

import (
	"fmt"
	"math/bits"
)

// setShardData is one shard's decoded pointer/bucket arrays for a Set
// schema type, matching HollowSetTypeDataElements.
type setShardData struct {
	maxOrdinal                   int32
	bitsPerSetPointer            int
	bitsPerSetSizeValue          int
	bitsPerElement               int
	bitsPerFixedLengthSetPortion int
	emptyBucketValue             uint64

	setPointerAndSizeData []uint64
	elementData           []uint64
}

// SetTypeData holds the fully decoded records for one Set-schema type from
// a Hollow blob, matching (the read side of) HollowSetTypeReadState.
type SetTypeData struct {
	Schema            *SetSchema
	MaxOrdinal        int32
	shardOrdinalShift uint
	shardMask         int32
	shards            []*setShardData
}

// readSetTypeData reads all shards of one Set-schema type's snapshot data,
// matching HollowSetTypeReadState#readSnapshot +
// HollowSetTypeDataElements#readFromInput (isDelta=false).
func readSetTypeData(r byteReader, schema *SetSchema, numShards int32) (*SetTypeData, error) {
	data := &SetTypeData{
		Schema:            schema,
		shardOrdinalShift: uint(bits.Len32(uint32(numShards)) - 1),
		shardMask:         numShards - 1,
		shards:            make([]*setShardData, numShards),
	}

	if numShards > 1 {
		overallMaxOrdinal, err := readVInt(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading %s overall max ordinal: %w", schema.Name(), err)
		}
		data.MaxOrdinal = overallMaxOrdinal
	}

	for i := int32(0); i < numShards; i++ {
		shard, err := readSetShardData(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading %s shard %d: %w", schema.Name(), i, err)
		}
		data.shards[i] = shard
	}

	if numShards == 1 {
		data.MaxOrdinal = data.shards[0].maxOrdinal
	}

	return data, nil
}

func readSetShardData(r byteReader) (*setShardData, error) {
	maxOrdinal, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading max ordinal: %w", err)
	}

	shard, err := readSetShardFieldsAndData(r)
	if err != nil {
		return nil, err
	}
	shard.maxOrdinal = maxOrdinal
	return shard, nil
}

// readSetShardFieldsAndData reads the part of a shard's data that follows
// its max ordinal (snapshot) or removals/additions (delta) — identical
// between the two, matching HollowSetTypeDataElements#readFromInput.
func readSetShardFieldsAndData(r byteReader) (*setShardData, error) {
	bitsPerSetPointer, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading bits-per-set-pointer: %w", err)
	}
	bitsPerSetSizeValue, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading bits-per-set-size-value: %w", err)
	}
	bitsPerElement, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading bits-per-element: %w", err)
	}
	if _, err := readVLong(r); err != nil { // totalNumberOfBuckets; recoverable from pointer data
		return nil, fmt.Errorf("reading total bucket count: %w", err)
	}

	setPointerAndSizeData, err := readFixedLengthData(r)
	if err != nil {
		return nil, fmt.Errorf("reading set pointer/size data: %w", err)
	}
	elementData, err := readFixedLengthData(r)
	if err != nil {
		return nil, fmt.Errorf("reading element data: %w", err)
	}

	return &setShardData{
		bitsPerSetPointer:            int(bitsPerSetPointer),
		bitsPerSetSizeValue:          int(bitsPerSetSizeValue),
		bitsPerElement:               int(bitsPerElement),
		bitsPerFixedLengthSetPortion: int(bitsPerSetPointer + bitsPerSetSizeValue),
		emptyBucketValue:             nullValueForBits(int(bitsPerElement)),
		setPointerAndSizeData:        setPointerAndSizeData,
		elementData:                  elementData,
	}, nil
}

func (d *SetTypeData) shardFor(ordinal int32) (*setShardData, int32) {
	shard := d.shards[ordinal&d.shardMask]
	return shard, ordinal >> d.shardOrdinalShift
}

// startBucket/endBucket mirror HollowSetTypeDataElements#getStartBucket/
// #getEndBucket: cumulative bucket-range pointers, delta-encoded the same
// way as a List's element pointers.
func (s *setShardData) startBucket(shardOrdinal int32) int64 {
	if shardOrdinal == 0 {
		return 0
	}
	return int64(getElementValue(s.setPointerAndSizeData, int64(shardOrdinal-1)*int64(s.bitsPerFixedLengthSetPortion), s.bitsPerSetPointer))
}

func (s *setShardData) endBucket(shardOrdinal int32) int64 {
	return int64(getElementValue(s.setPointerAndSizeData, int64(shardOrdinal)*int64(s.bitsPerFixedLengthSetPortion), s.bitsPerSetPointer))
}

// size returns the number of elements in the set at shardOrdinal, matching
// HollowSetTypeReadStateShard#size.
func (s *setShardData) size(shardOrdinal int32) int {
	bitOffset := int64(shardOrdinal)*int64(s.bitsPerFixedLengthSetPortion) + int64(s.bitsPerSetPointer)
	return int(getElementValue(s.setPointerAndSizeData, bitOffset, s.bitsPerSetSizeValue))
}

// elements returns every element ordinal in the set at shardOrdinal, in
// unspecified (hash-bucket) order, matching a full-table walk of
// HollowSetTypeDataElements's bucket range skipping empty buckets.
func (s *setShardData) elements(shardOrdinal int32) []int32 {
	start := s.startBucket(shardOrdinal)
	end := s.endBucket(shardOrdinal)

	result := make([]int32, 0, end-start)
	for bucket := start; bucket < end; bucket++ {
		v := getElementValue(s.elementData, bucket*int64(s.bitsPerElement), s.bitsPerElement)
		if v != s.emptyBucketValue {
			result = append(result, int32(v))
		}
	}
	return result
}

// Size returns the number of elements in the set at ordinal, matching
// HollowSetTypeReadStateShard#size.
func (d *SetTypeData) Size(ordinal int32) int {
	shard, shardOrdinal := d.shardFor(ordinal)
	return shard.size(shardOrdinal)
}

// Elements returns every element ordinal in the set at ordinal, in
// unspecified (hash-bucket) order, matching a full-table walk of
// HollowSetTypeDataElements's bucket range skipping empty buckets.
func (d *SetTypeData) Elements(ordinal int32) []int32 {
	shard, shardOrdinal := d.shardFor(ordinal)
	return shard.elements(shardOrdinal)
}
