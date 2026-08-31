package hollow

import "fmt"

// ApplyDelta reads a Hollow delta (or reverse delta) blob and merges it
// onto b, returning the resulting new Blob. b itself is left untouched —
// Java's model always produces a new decoded state from a delta too, but
// does so via in-place mutation of shared state (see
// go-hollow-porting-notes.md §7 on why that's not the natural Go shape);
// returning a new value instead avoids aliasing surprises for callers still
// holding a reference to the prior Blob.
//
// Types with no changes this cycle don't appear in a delta's body at all
// (the producer omits them) — those TypeStates carry forward unchanged
// (same pointer) into the new Blob.
func (b *Blob) ApplyDelta(r byteReader) (*Blob, error) {
	header, err := readHeader(r)
	if err != nil {
		return nil, err
	}
	if header.OriginRandomizedTag != b.Header.DestinationRandomizedTag {
		return nil, fmt.Errorf("hollow: delta origin tag %d does not match current state's tag %d (delta not applicable to this state)",
			header.OriginRandomizedTag, b.Header.DestinationRandomizedTag)
	}

	numStates, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading delta type-state count: %w", err)
	}

	byName := make(map[string]*TypeState, len(b.Types))
	for _, t := range b.Types {
		byName[t.Schema.Name()] = t
	}

	changed := make(map[string]*TypeState, numStates)
	for i := 0; i < int(numStates); i++ {
		typeState, err := readTypeStateDelta(r, byName)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading delta type-state %d: %w", i, err)
		}
		changed[typeState.Schema.Name()] = typeState
	}

	newBlob := &Blob{Header: header, Types: make([]*TypeState, len(b.Types))}
	for i, t := range b.Types {
		if updated, ok := changed[t.Schema.Name()]; ok {
			newBlob.Types[i] = updated
		} else {
			newBlob.Types[i] = t
		}
	}

	return newBlob, nil
}

// readTypeStateDelta reads and applies one type's delta, matching
// HollowBlobReader#readTypeStateDelta for the case where the type already
// exists in the current state (a delta introducing a brand-new type mid-
// chain isn't supported — not exercised by this project's fixtures).
func readTypeStateDelta(r byteReader, byName map[string]*TypeState) (*TypeState, error) {
	schema, err := readSchema(r)
	if err != nil {
		return nil, fmt.Errorf("reading schema: %w", err)
	}

	numShards, err := readNumShards(r)
	if err != nil {
		return nil, fmt.Errorf("reading %s num shards: %w", schema.Name(), err)
	}

	prior, ok := byName[schema.Name()]
	if !ok {
		return nil, fmt.Errorf("hollow: delta references type %q with no prior state (new types mid-chain are not supported)", schema.Name())
	}

	typeState := &TypeState{Schema: schema, NumShards: numShards}

	switch s := schema.(type) {
	case *ObjectSchema:
		data, populated, err := applyObjectDelta(r, s, numShards, prior.Object, prior.Populated)
		if err != nil {
			return nil, err
		}
		typeState.Object = data
		typeState.MaxOrdinal = data.MaxOrdinal
		typeState.Populated = populated
	case *ListSchema:
		data, populated, err := applyListDelta(r, s, numShards, prior.List, prior.Populated)
		if err != nil {
			return nil, err
		}
		typeState.List = data
		typeState.MaxOrdinal = data.MaxOrdinal
		typeState.Populated = populated
	case *SetSchema:
		data, populated, err := applySetDelta(r, s, numShards, prior.Set, prior.Populated)
		if err != nil {
			return nil, err
		}
		typeState.Set = data
		typeState.MaxOrdinal = data.MaxOrdinal
		typeState.Populated = populated
	case *MapSchema:
		data, populated, err := applyMapDelta(r, s, numShards, prior.Map, prior.Populated)
		if err != nil {
			return nil, err
		}
		typeState.Map = data
		typeState.MaxOrdinal = data.MaxOrdinal
		typeState.Populated = populated
	default:
		return nil, fmt.Errorf("unhandled schema type %v for %s", schema.SchemaType(), schema.Name())
	}

	return typeState, nil
}
