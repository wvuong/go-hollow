package hollow

import "fmt"

// applySetDelta reads and applies one Set-schema type's delta onto from,
// matching HollowSetTypeReadState#applyDelta + HollowSetDeltaApplicator.
func applySetDelta(r byteReader, schema *SetSchema, numShards int32, from *SetTypeData, fromPopulated *PopulatedOrdinals) (*SetTypeData, *PopulatedOrdinals, error) {
	if from == nil {
		return nil, nil, fmt.Errorf("hollow: delta for %s has no prior state to apply to", schema.Name())
	}
	if int32(len(from.shards)) != numShards {
		return nil, nil, fmt.Errorf("hollow: %s: delta has %d shards but prior state has %d (resharding is not supported)",
			schema.Name(), numShards, len(from.shards))
	}

	target := &SetTypeData{
		Schema:            schema,
		shardOrdinalShift: from.shardOrdinalShift,
		shardMask:         from.shardMask,
		shards:            make([]*setShardData, numShards),
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

		deltaShard, err := readSetShardFieldsAndData(r)
		if err != nil {
			return nil, nil, fmt.Errorf("hollow: reading %s shard %d delta data: %w", schema.Name(), i, err)
		}
		deltaShard.maxOrdinal = deltaMaxOrdinal

		target.shards[i] = mergeSetShard(from.shards[i], deltaMaxOrdinal, removals, additions, deltaShard, i, target.shardOrdinalShift, fromPopulated, populated)
	}

	return target, populated, nil
}

// mergeSetShard produces one shard's merged sets. As with List (see
// listtype_delta.go), this rebuilds each record's bucket range densely
// from decoded content (setShardData.elements, which already filters the
// empty-bucket sentinel) rather than preserving Java's exact hash-table
// byte layout — a set's decoded contents don't depend on table size/load
// factor, so a dense (no empty slots) rebuild is an equally valid encoding.
// See mergeObjectShard's doc comment for why keepFromPrior consults
// fromPopulated rather than just checking the ordinal is in range.
func mergeSetShard(from *setShardData, deltaMaxOrdinal int32, removals, additions []int32, deltaShard *setShardData, shardIndex int32, shardOrdinalShift uint, fromPopulated *PopulatedOrdinals, populated *PopulatedOrdinals) *setShardData {
	target := &setShardData{
		maxOrdinal:                   deltaMaxOrdinal,
		bitsPerSetPointer:            deltaShard.bitsPerSetPointer,
		bitsPerSetSizeValue:          deltaShard.bitsPerSetSizeValue,
		bitsPerElement:               deltaShard.bitsPerElement,
		bitsPerFixedLengthSetPortion: deltaShard.bitsPerSetPointer + deltaShard.bitsPerSetSizeValue,
		emptyBucketValue:             nullValueForBits(deltaShard.bitsPerElement),
	}

	ptrBits := int64(target.bitsPerFixedLengthSetPortion) * (int64(target.maxOrdinal) + 1)
	target.setPointerAndSizeData = make([]uint64, (ptrBits+63)/64)

	var elements []int32
	additionsIdx := &ordinalIndex{ordinals: additions}
	removalsIdx := &ordinalIndex{ordinals: removals}

	for ordinal := int32(0); ordinal <= target.maxOrdinal; ordinal++ {
		addFromDelta := additionsIdx.matches(ordinal)
		removeData := removalsIdx.matches(ordinal)
		fromGlobalOrdinal := (ordinal << shardOrdinalShift) | shardIndex
		keepFromPrior := !addFromDelta && !removeData && ordinal <= from.maxOrdinal && fromPopulated.IsPopulated(fromGlobalOrdinal)

		var recordElements []int32
		switch {
		case addFromDelta:
			recordElements = deltaShard.elements(int32(additionsIdx.pos))
		case keepFromPrior:
			recordElements = from.elements(ordinal)
		}
		elements = append(elements, recordElements...)

		writeBit := int64(ordinal) * int64(target.bitsPerFixedLengthSetPortion)
		setElementValue(target.setPointerAndSizeData, writeBit, target.bitsPerSetPointer, uint64(len(elements)))
		setElementValue(target.setPointerAndSizeData, writeBit+int64(target.bitsPerSetPointer), target.bitsPerSetSizeValue, uint64(len(recordElements)))

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
