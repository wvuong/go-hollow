package hollow

import "fmt"

// applyMapDelta reads and applies one Map-schema type's delta onto from,
// matching HollowMapTypeReadState#applyDelta + HollowMapDeltaApplicator.
func applyMapDelta(r byteReader, schema *MapSchema, numShards int32, from *MapTypeData, fromPopulated *PopulatedOrdinals) (*MapTypeData, *PopulatedOrdinals, error) {
	if from == nil {
		return nil, nil, fmt.Errorf("hollow: delta for %s has no prior state to apply to", schema.Name())
	}
	if int32(len(from.shards)) != numShards {
		return nil, nil, fmt.Errorf("hollow: %s: delta has %d shards but prior state has %d (resharding is not supported)",
			schema.Name(), numShards, len(from.shards))
	}

	target := &MapTypeData{
		Schema:            schema,
		shardOrdinalShift: from.shardOrdinalShift,
		shardMask:         from.shardMask,
		shards:            make([]*mapShardData, numShards),
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

		deltaShard, err := readMapShardFieldsAndData(r)
		if err != nil {
			return nil, nil, fmt.Errorf("hollow: reading %s shard %d delta data: %w", schema.Name(), i, err)
		}
		deltaShard.maxOrdinal = deltaMaxOrdinal

		target.shards[i] = mergeMapShard(from.shards[i], deltaMaxOrdinal, removals, additions, deltaShard, i, target.shardOrdinalShift, fromPopulated, populated)
	}

	return target, populated, nil
}

// mergeMapShard produces one shard's merged maps, mirroring mergeSetShard's
// dense-rebuild-from-decoded-content approach (see its doc comment, and
// mergeObjectShard's for why keepFromPrior consults fromPopulated).
func mergeMapShard(from *mapShardData, deltaMaxOrdinal int32, removals, additions []int32, deltaShard *mapShardData, shardIndex int32, shardOrdinalShift uint, fromPopulated *PopulatedOrdinals, populated *PopulatedOrdinals) *mapShardData {
	target := &mapShardData{
		maxOrdinal:                   deltaMaxOrdinal,
		bitsPerMapPointer:            deltaShard.bitsPerMapPointer,
		bitsPerMapSizeValue:          deltaShard.bitsPerMapSizeValue,
		bitsPerKeyElement:            deltaShard.bitsPerKeyElement,
		bitsPerValueElement:          deltaShard.bitsPerValueElement,
		bitsPerFixedLengthMapPortion: deltaShard.bitsPerMapPointer + deltaShard.bitsPerMapSizeValue,
		bitsPerMapEntry:              deltaShard.bitsPerKeyElement + deltaShard.bitsPerValueElement,
		emptyBucketKeyValue:          nullValueForBits(deltaShard.bitsPerKeyElement),
	}

	ptrBits := int64(target.bitsPerFixedLengthMapPortion) * (int64(target.maxOrdinal) + 1)
	target.mapPointerAndSizeData = make([]uint64, (ptrBits+63)/64)

	var entries []MapEntry
	additionsIdx := &ordinalIndex{ordinals: additions}
	removalsIdx := &ordinalIndex{ordinals: removals}

	for ordinal := int32(0); ordinal <= target.maxOrdinal; ordinal++ {
		addFromDelta := additionsIdx.matches(ordinal)
		removeData := removalsIdx.matches(ordinal)
		fromGlobalOrdinal := (ordinal << shardOrdinalShift) | shardIndex
		keepFromPrior := !addFromDelta && !removeData && ordinal <= from.maxOrdinal && fromPopulated.IsPopulated(fromGlobalOrdinal)

		var recordEntries []MapEntry
		switch {
		case addFromDelta:
			recordEntries = deltaShard.entries(int32(additionsIdx.pos))
		case keepFromPrior:
			recordEntries = from.entries(ordinal)
		}
		entries = append(entries, recordEntries...)

		writeBit := int64(ordinal) * int64(target.bitsPerFixedLengthMapPortion)
		setElementValue(target.mapPointerAndSizeData, writeBit, target.bitsPerMapPointer, uint64(len(entries)))
		setElementValue(target.mapPointerAndSizeData, writeBit+int64(target.bitsPerMapPointer), target.bitsPerMapSizeValue, uint64(len(recordEntries)))

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

	target.entryData = make([]uint64, (int64(len(entries))*int64(target.bitsPerMapEntry)+63)/64)
	for i, e := range entries {
		base := int64(i) * int64(target.bitsPerMapEntry)
		setElementValue(target.entryData, base, target.bitsPerKeyElement, uint64(e.Key))
		setElementValue(target.entryData, base+int64(target.bitsPerKeyElement), target.bitsPerValueElement, uint64(e.Value))
	}

	return target
}
