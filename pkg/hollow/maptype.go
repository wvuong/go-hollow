package hollow

import (
	"fmt"
	"math/bits"
)

// MapEntry is one decoded key/value ordinal pair from a Map-schema record.
type MapEntry struct {
	Key   int32
	Value int32
}

// mapShardData is one shard's decoded pointer/entry arrays for a Map
// schema type, matching HollowMapTypeDataElements.
type mapShardData struct {
	maxOrdinal                   int32
	bitsPerMapPointer            int
	bitsPerMapSizeValue          int
	bitsPerKeyElement            int
	bitsPerValueElement          int
	bitsPerFixedLengthMapPortion int
	bitsPerMapEntry              int
	emptyBucketKeyValue          uint64

	mapPointerAndSizeData []uint64
	entryData             []uint64
}

// MapTypeData holds the fully decoded records for one Map-schema type from
// a Hollow blob, matching (the read side of) HollowMapTypeReadState.
type MapTypeData struct {
	Schema            *MapSchema
	MaxOrdinal        int32
	shardOrdinalShift uint
	shardMask         int32
	shards            []*mapShardData
}

// readMapTypeData reads all shards of one Map-schema type's snapshot data,
// matching HollowMapTypeReadState#readSnapshot +
// HollowMapTypeDataElements#readFromInput (isDelta=false).
func readMapTypeData(r byteReader, schema *MapSchema, numShards int32) (*MapTypeData, error) {
	data := &MapTypeData{
		Schema:            schema,
		shardOrdinalShift: uint(bits.Len32(uint32(numShards)) - 1),
		shardMask:         numShards - 1,
		shards:            make([]*mapShardData, numShards),
	}

	if numShards > 1 {
		overallMaxOrdinal, err := readVInt(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading %s overall max ordinal: %w", schema.Name(), err)
		}
		data.MaxOrdinal = overallMaxOrdinal
	}

	for i := int32(0); i < numShards; i++ {
		shard, err := readMapShardData(r)
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

func readMapShardData(r byteReader) (*mapShardData, error) {
	maxOrdinal, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading max ordinal: %w", err)
	}

	shard, err := readMapShardFieldsAndData(r)
	if err != nil {
		return nil, err
	}
	shard.maxOrdinal = maxOrdinal
	return shard, nil
}

// readMapShardFieldsAndData reads the part of a shard's data that follows
// its max ordinal (snapshot) or removals/additions (delta) — identical
// between the two, matching HollowMapTypeDataElements#readFromInput.
func readMapShardFieldsAndData(r byteReader) (*mapShardData, error) {
	bitsPerMapPointer, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading bits-per-map-pointer: %w", err)
	}
	bitsPerMapSizeValue, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading bits-per-map-size-value: %w", err)
	}
	bitsPerKeyElement, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading bits-per-key-element: %w", err)
	}
	bitsPerValueElement, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading bits-per-value-element: %w", err)
	}
	if _, err := readVLong(r); err != nil { // totalNumberOfBuckets; recoverable from pointer data
		return nil, fmt.Errorf("reading total bucket count: %w", err)
	}

	mapPointerAndSizeData, err := readFixedLengthData(r)
	if err != nil {
		return nil, fmt.Errorf("reading map pointer/size data: %w", err)
	}
	entryData, err := readFixedLengthData(r)
	if err != nil {
		return nil, fmt.Errorf("reading entry data: %w", err)
	}

	return &mapShardData{
		bitsPerMapPointer:            int(bitsPerMapPointer),
		bitsPerMapSizeValue:          int(bitsPerMapSizeValue),
		bitsPerKeyElement:            int(bitsPerKeyElement),
		bitsPerValueElement:          int(bitsPerValueElement),
		bitsPerFixedLengthMapPortion: int(bitsPerMapPointer + bitsPerMapSizeValue),
		bitsPerMapEntry:              int(bitsPerKeyElement + bitsPerValueElement),
		emptyBucketKeyValue:          nullValueForBits(int(bitsPerKeyElement)),
		mapPointerAndSizeData:        mapPointerAndSizeData,
		entryData:                    entryData,
	}, nil
}

func (d *MapTypeData) shardFor(ordinal int32) (*mapShardData, int32) {
	shard := d.shards[ordinal&d.shardMask]
	return shard, ordinal >> d.shardOrdinalShift
}

// startBucket/endBucket mirror HollowMapTypeDataElements#getStartBucket/
// #getEndBucket: cumulative bucket-range pointers, delta-encoded the same
// way as a Set's bucket pointers.
func (s *mapShardData) startBucket(shardOrdinal int32) int64 {
	if shardOrdinal == 0 {
		return 0
	}
	return int64(getElementValue(s.mapPointerAndSizeData, int64(shardOrdinal-1)*int64(s.bitsPerFixedLengthMapPortion), s.bitsPerMapPointer))
}

func (s *mapShardData) endBucket(shardOrdinal int32) int64 {
	return int64(getElementValue(s.mapPointerAndSizeData, int64(shardOrdinal)*int64(s.bitsPerFixedLengthMapPortion), s.bitsPerMapPointer))
}

// size returns the number of entries in the map at shardOrdinal.
func (s *mapShardData) size(shardOrdinal int32) int {
	bitOffset := int64(shardOrdinal)*int64(s.bitsPerFixedLengthMapPortion) + int64(s.bitsPerMapPointer)
	return int(getElementValue(s.mapPointerAndSizeData, bitOffset, s.bitsPerMapSizeValue))
}

// entries returns every key/value ordinal pair in the map at shardOrdinal,
// in unspecified (hash-bucket) order, matching a full-table walk of
// HollowMapTypeDataElements's bucket range skipping empty buckets.
func (s *mapShardData) entries(shardOrdinal int32) []MapEntry {
	start := s.startBucket(shardOrdinal)
	end := s.endBucket(shardOrdinal)

	result := make([]MapEntry, 0, end-start)
	for bucket := start; bucket < end; bucket++ {
		key := getElementValue(s.entryData, bucket*int64(s.bitsPerMapEntry), s.bitsPerKeyElement)
		if key == s.emptyBucketKeyValue {
			continue
		}
		value := getElementValue(s.entryData, bucket*int64(s.bitsPerMapEntry)+int64(s.bitsPerKeyElement), s.bitsPerValueElement)
		result = append(result, MapEntry{Key: int32(key), Value: int32(value)})
	}
	return result
}

// Size returns the number of entries in the map at ordinal.
func (d *MapTypeData) Size(ordinal int32) int {
	shard, shardOrdinal := d.shardFor(ordinal)
	return shard.size(shardOrdinal)
}

// Entries returns every key/value ordinal pair in the map at ordinal, in
// unspecified (hash-bucket) order, matching a full-table walk of
// HollowMapTypeDataElements's bucket range skipping empty buckets.
func (d *MapTypeData) Entries(ordinal int32) []MapEntry {
	shard, shardOrdinal := d.shardFor(ordinal)
	return shard.entries(shardOrdinal)
}
