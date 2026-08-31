package hollow

import "fmt"

// TypeState is one type's data from a blob snapshot body, matching one
// iteration of HollowBlobReader#readTypeStateSnapshot.
type TypeState struct {
	Schema     Schema
	NumShards  int32
	MaxOrdinal int32
	Populated  *PopulatedOrdinals

	// Exactly one of the following is set, matching Schema's concrete type.
	Object *ObjectTypeData
	List   *ListTypeData
	Set    *SetTypeData
	Map    *MapTypeData
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
		data, err := readListTypeData(r, s, numShards)
		if err != nil {
			return nil, err
		}
		typeState.List = data
		typeState.MaxOrdinal = data.MaxOrdinal
	case *SetSchema:
		data, err := readSetTypeData(r, s, numShards)
		if err != nil {
			return nil, err
		}
		typeState.Set = data
		typeState.MaxOrdinal = data.MaxOrdinal
	case *MapSchema:
		data, err := readMapTypeData(r, s, numShards)
		if err != nil {
			return nil, err
		}
		typeState.Map = data
		typeState.MaxOrdinal = data.MaxOrdinal
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
