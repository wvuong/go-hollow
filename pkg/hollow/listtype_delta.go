package hollow

import "fmt"

// applyListDelta reads and applies one List-schema type's delta onto from,
// matching HollowListTypeReadState#applyDelta + HollowListDeltaApplicator
// (always the "slow path" — see objecttype_delta.go's doc comment on why
// that's fine here).
func applyListDelta(r byteReader, schema *ListSchema, numShards int32, from *ListTypeData, fromPopulated *PopulatedOrdinals) (*ListTypeData, *PopulatedOrdinals, error) {
	if from == nil {
		return nil, nil, fmt.Errorf("hollow: delta for %s has no prior state to apply to", schema.Name())
	}
	if int32(len(from.shards)) != numShards {
		return nil, nil, fmt.Errorf("hollow: %s: delta has %d shards but prior state has %d (resharding is not supported)",
			schema.Name(), numShards, len(from.shards))
	}

	target := &ListTypeData{
		Schema:            schema,
		shardOrdinalShift: from.shardOrdinalShift,
		shardMask:         from.shardMask,
		shards:            make([]*listShardData, numShards),
	}

	if numShards > 1 {
		overallMaxOrdinal, err := readVInt(r)
		if err != nil {
			return nil, nil, fmt.Errorf("hollow: reading %s overall max ordinal: %w", schema.Name(), err)
		}
		target.MaxOrdinal = overallMaxOrdinal
	}

	var populated *PopulatedOrdinals
	if numShards > 1 {
		populated = newPopulatedOrdinals(target.MaxOrdinal)
	}

	for i := int32(0); i < numShards; i++ {
		deltaMaxOrdinal, removals, additions, err := readDeltaPrefix(r)
		if err != nil {
			return nil, nil, fmt.Errorf("hollow: %s shard %d: %w", schema.Name(), i, err)
		}

		if numShards == 1 {
			target.MaxOrdinal = deltaMaxOrdinal
			populated = newPopulatedOrdinals(target.MaxOrdinal)
		}

		deltaShard, err := readListShardFieldsAndData(r)
		if err != nil {
			return nil, nil, fmt.Errorf("hollow: reading %s shard %d delta data: %w", schema.Name(), i, err)
		}
		deltaShard.maxOrdinal = deltaMaxOrdinal

		target.shards[i] = mergeListShard(from.shards[i], deltaMaxOrdinal, removals, additions, deltaShard, i, target.shardOrdinalShift, fromPopulated, populated)
	}

	return target, populated, nil
}

// mergeListShard produces one shard's merged lists. Unlike Java (which
// preserves the prior state's exact pointer/element bytes for a bulk-copied
// run), this rebuilds every record's element run from decoded content via
// listShardData.elements — Hollow doesn't require any particular on-disk
// layout for a reader to be correct, only the decoded contents, so this is
// simpler and still produces an equivalent-content result. See
// mergeObjectShard's doc comment for why keepFromPrior consults
// fromPopulated rather than just checking the ordinal is in range.
func mergeListShard(from *listShardData, deltaMaxOrdinal int32, removals, additions []int32, deltaShard *listShardData, shardIndex int32, shardOrdinalShift uint, fromPopulated *PopulatedOrdinals, populated *PopulatedOrdinals) *listShardData {
	target := &listShardData{
		maxOrdinal:         deltaMaxOrdinal,
		bitsPerListPointer: deltaShard.bitsPerListPointer,
		bitsPerElement:     deltaShard.bitsPerElement,
	}

	ptrBits := int64(target.bitsPerListPointer) * (int64(target.maxOrdinal) + 1)
	target.listPointerData = make([]uint64, (ptrBits+63)/64)

	var elements []int32
	additionsIdx := &ordinalIndex{ordinals: additions}
	removalsIdx := &ordinalIndex{ordinals: removals}

	for ordinal := int32(0); ordinal <= target.maxOrdinal; ordinal++ {
		addFromDelta := additionsIdx.matches(ordinal)
		removeData := removalsIdx.matches(ordinal)
		fromGlobalOrdinal := (ordinal << shardOrdinalShift) | shardIndex
		keepFromPrior := !addFromDelta && !removeData && ordinal <= from.maxOrdinal && fromPopulated.IsPopulated(fromGlobalOrdinal)

		switch {
		case addFromDelta:
			elements = append(elements, deltaShard.elements(int32(additionsIdx.pos))...)
		case keepFromPrior:
			elements = append(elements, from.elements(ordinal)...)
		}

		setElementValue(target.listPointerData, int64(ordinal)*int64(target.bitsPerListPointer), target.bitsPerListPointer, uint64(len(elements)))

		if addFromDelta || keepFromPrior {
			populated.set(fromGlobalOrdinal)
		}
		if addFromDelta {
			additionsIdx.advance()
		}
		if removeData {
			removalsIdx.advance()
		}
	}

	target.elementData = make([]uint64, (int64(len(elements))*int64(target.bitsPerElement)+63)/64)
	for i, e := range elements {
		setElementValue(target.elementData, int64(i)*int64(target.bitsPerElement), target.bitsPerElement, uint64(e))
	}

	return target
}
