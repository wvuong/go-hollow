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
		maxOrdinal:         maxOrdinal,
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

// Size returns the number of elements in the list at ordinal.
func (d *ListTypeData) Size(ordinal int32) int {
	shard, shardOrdinal := d.shardFor(ordinal)
	return int(shard.endElement(shardOrdinal) - shard.startElement(shardOrdinal))
}

// Element reads the element ordinal at the given index within the list at
// ordinal, matching HollowListTypeReadStateShard#getElementOrdinal. ok is
// false if index is out of range.
func (d *ListTypeData) Element(ordinal int32, index int) (elementOrdinal int32, ok bool) {
	shard, shardOrdinal := d.shardFor(ordinal)
	start := shard.startElement(shardOrdinal)
	end := shard.endElement(shardOrdinal)

	elementIndex := start + int64(index)
	if elementIndex < 0 || elementIndex >= end {
		return 0, false
	}

	v := getElementValue(shard.elementData, elementIndex*int64(shard.bitsPerElement), shard.bitsPerElement)
	return int32(v), true
}

// Elements returns every element ordinal in the list at ordinal, in order.
func (d *ListTypeData) Elements(ordinal int32) []int32 {
	size := d.Size(ordinal)
	result := make([]int32, size)
	for i := 0; i < size; i++ {
		result[i], _ = d.Element(ordinal, i)
	}
	return result
}
