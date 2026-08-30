package hollow

import "fmt"

// TypeState is one type's data from a blob snapshot body, matching one
// iteration of HollowBlobReader#readTypeStateSnapshot.
type TypeState struct {
	Schema     Schema
	NumShards  int32
	MaxOrdinal int32
	Populated  *PopulatedOrdinals

	// Object holds fully decoded records when Schema is an *ObjectSchema.
	Object *ObjectTypeData
	// Collection holds shard/size metadata when Schema is a *ListSchema,
	// *SetSchema, or *MapSchema — see CollectionSummary for what's not yet
	// decoded.
	Collection *CollectionSummary
}

// Blob is a fully parsed Hollow snapshot blob: its header plus every
// type-state in the body, in on-wire order.
type Blob struct {
	Header *BlobHeader
	Types  []*TypeState
}

// readSnapshotBlob reads a complete Hollow snapshot blob, matching
// HollowBlobReader#readSnapshot for the on-heap, unfiltered case.
func readSnapshotBlob(r byteReader) (*Blob, error) {
	header, err := readHeader(r)
	if err != nil {
		return nil, err
	}

	numStates, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading type-state count: %w", err)
	}

	blob := &Blob{Header: header, Types: make([]*TypeState, numStates)}
	for i := range blob.Types {
		typeState, err := readTypeStateSnapshot(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading type-state %d: %w", i, err)
		}
		blob.Types[i] = typeState
	}

	return blob, nil
}

func readTypeStateSnapshot(r byteReader) (*TypeState, error) {
	schema, err := readSchema(r)
	if err != nil {
		return nil, fmt.Errorf("reading schema: %w", err)
	}

	numShards, err := readNumShards(r)
	if err != nil {
		return nil, fmt.Errorf("reading %s num shards: %w", schema.Name(), err)
	}

	typeState := &TypeState{Schema: schema, NumShards: numShards}

	switch s := schema.(type) {
	case *ObjectSchema:
		data, err := readObjectTypeData(r, s, numShards)
		if err != nil {
			return nil, err
		}
		typeState.Object = data
		typeState.MaxOrdinal = data.MaxOrdinal
	case *ListSchema:
		summary, err := readAndDiscardCollectionTypeData(r, SchemaTypeList, numShards)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", schema.Name(), err)
		}
		typeState.Collection = summary
		typeState.MaxOrdinal = summary.MaxOrdinal
	case *SetSchema:
		summary, err := readAndDiscardCollectionTypeData(r, SchemaTypeSet, numShards)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", schema.Name(), err)
		}
		typeState.Collection = summary
		typeState.MaxOrdinal = summary.MaxOrdinal
	case *MapSchema:
		summary, err := readAndDiscardCollectionTypeData(r, SchemaTypeMap, numShards)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", schema.Name(), err)
		}
		typeState.Collection = summary
		typeState.MaxOrdinal = summary.MaxOrdinal
	default:
		return nil, fmt.Errorf("unhandled schema type %v for %s", schema.SchemaType(), schema.Name())
	}

	populated, err := readPopulatedOrdinals(r)
	if err != nil {
		return nil, fmt.Errorf("reading %s populated ordinals: %w", schema.Name(), err)
	}
	typeState.Populated = populated

	return typeState, nil
}

// readNumShards reads the shard count that follows a type's schema in the
// blob body, matching HollowBlobReader#readNumShards.
func readNumShards(r byteReader) (int32, error) {
	backwardsCompatibilityBytes, err := readVInt(r)
	if err != nil {
		return 0, fmt.Errorf("reading backwards-compatibility marker: %w", err)
	}
	if backwardsCompatibilityBytes == 0 {
		return 1, nil // produced by a version of hollow prior to 2.1.0, always only 1 shard.
	}

	if err := skipForwardCompatibilityBytes(r); err != nil {
		return 0, err
	}

	return readVInt(r)
}
