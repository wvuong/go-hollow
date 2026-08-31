package hollow

import (
	"fmt"
	"math/bits"
)

// listShardData is one shard's decoded pointer/element arrays for a List
// schema type, matching HollowListTypeDataElements.
type listShardData struct {
	maxOrdinal         int32
	bitsPerListPointer int
	bitsPerElement     int

	listPointerData []uint64
	elementData     []uint64
}

// ListTypeData holds the fully decoded records for one List-schema type
// from a Hollow blob, matching (the read side of) HollowListTypeReadState.
type ListTypeData struct {
	Schema            *ListSchema
	MaxOrdinal        int32
	shardOrdinalShift uint
	shardMask         int32
	shards            []*listShardData
}

// readListTypeData reads all shards of one List-schema type's snapshot
// data, matching HollowListTypeReadState#readSnapshot +
// HollowListTypeDataElements#readFromInput (isDelta=false).
func readListTypeData(r byteReader, schema *ListSchema, numShards int32) (*ListTypeData, error) {
	data := &ListTypeData{
		Schema:            schema,
		shardOrdinalShift: uint(bits.Len32(uint32(numShards)) - 1),
		shardMask:         numShards - 1,
		shards:            make([]*listShardData, numShards),
	}

	if numShards > 1 {
		overallMaxOrdinal, err := readVInt(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading %s overall max ordinal: %w", schema.Name(), err)
		}
		data.MaxOrdinal = overallMaxOrdinal
	}

	for i := int32(0); i < numShards; i++ {
		shard, err := readListShardData(r)
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

func readListShardData(r byteReader) (*listShardData, error) {
	maxOrdinal, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading max ordinal: %w", err)
	}

	shard, err := readListShardFieldsAndData(r)
	if err != nil {
		return nil, err
	}
	shard.maxOrdinal = maxOrdinal
	return shard, nil
}

// readListShardFieldsAndData reads the part of a shard's data that follows
// its max ordinal (snapshot) or removals/additions (delta) — identical
// between the two, matching HollowListTypeDataElements#readFromInput.
func readListShardFieldsAndData(r byteReader) (*listShardData, error) {
	bitsPerListPointer, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading bits-per-list-pointer: %w", err)
	}
	bitsPerElement, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading bits-per-element: %w", err)
	}
	if _, err := readVLong(r); err != nil { // totalNumberOfElements; recoverable from pointer data
		return nil, fmt.Errorf("reading total element count: %w", err)
	}

	listPointerData, err := readFixedLengthData(r)
	if err != nil {
		return nil, fmt.Errorf("reading list pointer data: %w", err)
	}
	elementData, err := readFixedLengthData(r)
	if err != nil {
		return nil, fmt.Errorf("reading element data: %w", err)
	}

	return &listShardData{
		bitsPerListPointer: int(bitsPerListPointer),
		bitsPerElement:     int(bitsPerElement),
		listPointerData:    listPointerData,
		elementData:        elementData,
	}, nil
}

func (d *ListTypeData) shardFor(ordinal int32) (*listShardData, int32) {
	shard := d.shards[ordinal&d.shardMask]
	return shard, ordinal >> d.shardOrdinalShift
}

// startElement/endElement mirror HollowListTypeDataElements#getStartElement/
// #getEndElement: cumulative end-pointers, delta-encoded the same way as an
// Object record's STRING/BYTES var-length fields.
func (s *listShardData) startElement(shardOrdinal int32) int64 {
	if shardOrdinal == 0 {
		return 0
	}
	return int64(getElementValue(s.listPointerData, int64(shardOrdinal-1)*int64(s.bitsPerListPointer), s.bitsPerListPointer))
}

func (s *listShardData) endElement(shardOrdinal int32) int64 {
	return int64(getElementValue(s.listPointerData, int64(shardOrdinal)*int64(s.bitsPerListPointer), s.bitsPerListPointer))
}

// size returns the number of elements in the list at shardOrdinal.
func (s *listShardData) size(shardOrdinal int32) int {
	return int(s.endElement(shardOrdinal) - s.startElement(shardOrdinal))
}

// element reads the element ordinal at the given index within the list at
// shardOrdinal, matching HollowListTypeReadStateShard#getElementOrdinal. ok
// is false if index is out of range.
func (s *listShardData) element(shardOrdinal int32, index int) (elementOrdinal int32, ok bool) {
	start := s.startElement(shardOrdinal)
	end := s.endElement(shardOrdinal)

	elementIndex := start + int64(index)
	if elementIndex < 0 || elementIndex >= end {
		return 0, false
	}

	v := getElementValue(s.elementData, elementIndex*int64(s.bitsPerElement), s.bitsPerElement)
	return int32(v), true
}

// elements returns every element ordinal in the list at shardOrdinal, in order.
func (s *listShardData) elements(shardOrdinal int32) []int32 {
	size := s.size(shardOrdinal)
	result := make([]int32, size)
	for i := 0; i < size; i++ {
		result[i], _ = s.element(shardOrdinal, i)
	}
	return result
}

// Size returns the number of elements in the list at ordinal.
func (d *ListTypeData) Size(ordinal int32) int {
	shard, shardOrdinal := d.shardFor(ordinal)
	return shard.size(shardOrdinal)
}

// Element reads the element ordinal at the given index within the list at
// ordinal, matching HollowListTypeReadStateShard#getElementOrdinal. ok is
// false if index is out of range.
func (d *ListTypeData) Element(ordinal int32, index int) (elementOrdinal int32, ok bool) {
	shard, shardOrdinal := d.shardFor(ordinal)
	return shard.element(shardOrdinal, index)
}

// Elements returns every element ordinal in the list at ordinal, in order.
func (d *ListTypeData) Elements(ordinal int32) []int32 {
	shard, shardOrdinal := d.shardFor(ordinal)
	return shard.elements(shardOrdinal)
}
