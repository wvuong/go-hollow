package hollow

import "fmt"

// CollectionSummary holds the shard/size metadata read for a List, Set, or
// Map schema type. The element/entry data itself is not yet decoded (see
// go-hollow-porting-notes.md open threads) — this package currently decodes
// full records only for Object-schema types.
type CollectionSummary struct {
	MaxOrdinal int32
	// TotalElements is totalNumberOfElements for LIST, totalNumberOfBuckets
	// for SET and MAP.
	TotalElements int64
}

// readAndDiscardCollectionTypeData reads past a List/Set/Map type's shard
// data without decoding it, matching the *TypeDataElements#readFromInput
// (isDelta=false) methods for those three schema kinds, and returns the
// size metadata read along the way.
func readAndDiscardCollectionTypeData(r byteReader, schemaType SchemaType, numShards int32) (*CollectionSummary, error) {
	summary := &CollectionSummary{}

	if numShards > 1 {
		overallMaxOrdinal, err := readVInt(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading overall max ordinal: %w", err)
		}
		summary.MaxOrdinal = overallMaxOrdinal
	}

	for i := int32(0); i < numShards; i++ {
		maxOrdinal, totalElements, err := readAndDiscardCollectionShard(r, schemaType)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading shard %d: %w", i, err)
		}
		if numShards == 1 {
			summary.MaxOrdinal = maxOrdinal
		}
		summary.TotalElements += totalElements
	}

	return summary, nil
}

// readAndDiscardCollectionShard reads one shard of List/Set/Map data,
// matching HollowListTypeDataElements/HollowSetTypeDataElements/
// HollowMapTypeDataElements#readFromInput(isDelta=false).
func readAndDiscardCollectionShard(r byteReader, schemaType SchemaType) (maxOrdinal int32, totalElements int64, err error) {
	maxOrdinal, err = readVInt(r)
	if err != nil {
		return 0, 0, fmt.Errorf("reading max ordinal: %w", err)
	}

	// Number of VarInt bit-width fields preceding the element-count VarLong,
	// and the number of FixedLengthData sections that follow it.
	var numBitWidthFields, numFixedLengthSections int
	switch schemaType {
	case SchemaTypeList:
		numBitWidthFields, numFixedLengthSections = 2, 2 // bitsPerListPointer, bitsPerElement
	case SchemaTypeSet:
		numBitWidthFields, numFixedLengthSections = 3, 2 // bitsPerSetPointer, bitsPerSetSizeValue, bitsPerElement
	case SchemaTypeMap:
		numBitWidthFields, numFixedLengthSections = 4, 2 // bitsPerMapPointer, bitsPerMapSizeValue, bitsPerKeyElement, bitsPerValueElement
	default:
		return 0, 0, fmt.Errorf("not a collection schema type: %v", schemaType)
	}

	for i := 0; i < numBitWidthFields; i++ {
		if _, err := readVInt(r); err != nil {
			return 0, 0, fmt.Errorf("reading bit-width field %d: %w", i, err)
		}
	}

	totalElements, err = readVLong(r)
	if err != nil {
		return 0, 0, fmt.Errorf("reading total element/bucket count: %w", err)
	}

	for i := 0; i < numFixedLengthSections; i++ {
		if _, err := readFixedLengthData(r); err != nil {
			return 0, 0, fmt.Errorf("reading fixed-length section %d: %w", i, err)
		}
	}

	return maxOrdinal, totalElements, nil
}
