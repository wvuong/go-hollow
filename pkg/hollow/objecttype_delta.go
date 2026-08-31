package hollow

import "fmt"

// applyObjectDelta reads and applies one Object-schema type's delta onto
// from, matching HollowObjectTypeReadState#applyDelta +
// HollowObjectDeltaApplicator. It always does a full rebuild (Java's "slow
// path"; the "fast path" there is a pure bulk-copy performance optimization
// with identical results, not needed here).
//
// Simplification: this assumes the delta's schema has the same fields, in
// the same order, as the type's existing schema (true for any delta chain
// that doesn't evolve the type's schema mid-chain, which is the only case
// exercised by this project's fixtures) — Java instead remaps fields by
// name (HollowObjectDeltaApplicator#deltaFieldIndexMapping) to tolerate
// schema evolution across a delta chain.
func applyObjectDelta(r byteReader, schema *ObjectSchema, numShards int32, from *ObjectTypeData, fromPopulated *PopulatedOrdinals) (*ObjectTypeData, *PopulatedOrdinals, error) {
	if from == nil {
		return nil, nil, fmt.Errorf("hollow: delta for %s has no prior state to apply to", schema.Name())
	}
	if int32(len(from.shards)) != numShards {
		return nil, nil, fmt.Errorf("hollow: %s: delta has %d shards but prior state has %d (resharding is not supported)",
			schema.Name(), numShards, len(from.shards))
	}

	target := &ObjectTypeData{
		Schema:            schema,
		shardOrdinalShift: from.shardOrdinalShift,
		shardMask:         from.shardMask,
		shards:            make([]*objectShardData, numShards),
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

		deltaShard, err := readObjectShardFieldsAndData(r, len(schema.Fields))
		if err != nil {
			return nil, nil, fmt.Errorf("hollow: reading %s shard %d delta data: %w", schema.Name(), i, err)
		}
		deltaShard.maxOrdinal = deltaMaxOrdinal

		target.shards[i] = mergeObjectShard(schema, from.shards[i], deltaMaxOrdinal, removals, additions, deltaShard, i, target.shardOrdinalShift, fromPopulated, populated)
	}

	return target, populated, nil
}

// mergeObjectShard produces one shard's merged records, matching
// HollowObjectDeltaApplicator's ordinal-by-ordinal merge: an ordinal added
// in this delta takes its fields from deltaShard; an ordinal that existed
// before, is currently populated, and isn't removed keeps its fields from
// `from`; anything else is a hole (no data, not populated).
//
// The "currently populated" check (via fromPopulated) matters because
// `from` can already contain holes from an earlier cycle that this delta's
// own removals list has no reason to mention again. Java's reference
// reader instead threads each cycle's removals through with a deliberate
// one-cycle lag, so a hole is only ever "rediscovered" (and its stale
// bytes finally dropped) the next time that shard's array gets rebuilt —
// see the delta-decoding discussion in go-hollow-porting-notes.md. Since
// this port always maintains an accurate, non-lagged PopulatedOrdinals
// for every state, consulting it directly is simpler and gives the same
// observable result without replicating that lag.
func mergeObjectShard(schema *ObjectSchema, from *objectShardData, deltaMaxOrdinal int32, removals, additions []int32, deltaShard *objectShardData, shardIndex int32, shardOrdinalShift uint, fromPopulated *PopulatedOrdinals, populated *PopulatedOrdinals) *objectShardData {
	numFields := len(schema.Fields)
	target := &objectShardData{
		maxOrdinal:        deltaMaxOrdinal,
		bitsPerField:      append([]int(nil), deltaShard.bitsPerField...),
		bitOffsetPerField: make([]int, numFields),
		nullValueForField: make([]uint64, numFields),
		varLengthData:     make([][]byte, numFields),
	}

	bitOffset := 0
	for i, fieldBits := range target.bitsPerField {
		target.nullValueForField[i] = nullValueForBits(fieldBits)
		target.bitOffsetPerField[i] = bitOffset
		bitOffset += fieldBits
	}
	target.bitsPerRecord = bitOffset

	totalBits := int64(target.bitsPerRecord) * (int64(target.maxOrdinal) + 1)
	target.fixedLengthData = make([]uint64, (totalBits+63)/64)

	varLenBuf := make([][]byte, numFields)

	additionsIdx := &ordinalIndex{ordinals: additions}
	removalsIdx := &ordinalIndex{ordinals: removals}

	for ordinal := int32(0); ordinal <= target.maxOrdinal; ordinal++ {
		addFromDelta := additionsIdx.matches(ordinal)
		removeData := removalsIdx.matches(ordinal)
		fromGlobalOrdinal := (ordinal << shardOrdinalShift) | shardIndex
		keepFromPrior := !addFromDelta && !removeData && ordinal <= from.maxOrdinal && fromPopulated.IsPopulated(fromGlobalOrdinal)
		writeBit := int64(target.bitsPerRecord) * int64(ordinal)

		for fieldIdx, field := range schema.Fields {
			isVarLen := field.Type == FieldTypeString || field.Type == FieldTypeBytes
			fieldWriteBit := writeBit + int64(target.bitOffsetPerField[fieldIdx])

			switch {
			case addFromDelta:
				copyObjectField(target, fieldIdx, isVarLen, deltaShard, int32(additionsIdx.pos), fieldWriteBit, varLenBuf)
			case keepFromPrior:
				copyObjectField(target, fieldIdx, isVarLen, from, ordinal, fieldWriteBit, varLenBuf)
			default:
				writeNullObjectField(target, fieldIdx, isVarLen, fieldWriteBit, varLenBuf)
			}
		}

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

	copy(target.varLengthData, varLenBuf)
	return target
}

// copyObjectField copies one field's value from src (at srcOrdinal) into
// target at fieldWriteBit, re-encoding src's null sentinel as target's own
// (their bit widths may differ — see readFieldStatistics/nullValueForBits).
func copyObjectField(target *objectShardData, fieldIdx int, isVarLen bool, src *objectShardData, srcOrdinal int32, fieldWriteBit int64, varLenBuf [][]byte) {
	numBits := target.bitsPerField[fieldIdx]

	if !isVarLen {
		raw := src.rawFixedValue(srcOrdinal, fieldIdx)
		if raw == src.nullValueForField[fieldIdx] {
			setElementValue(target.fixedLengthData, fieldWriteBit, numBits, target.nullValueForField[fieldIdx])
		} else {
			setElementValue(target.fixedLengthData, fieldWriteBit, numBits, raw)
		}
		return
	}

	start, end, isNull := src.varLengthRange(srcOrdinal, fieldIdx)
	if isNull {
		writeNullVarLengthObjectField(target, fieldIdx, numBits, fieldWriteBit, varLenBuf)
		return
	}
	varLenBuf[fieldIdx] = append(varLenBuf[fieldIdx], src.varLengthData[fieldIdx][start:end]...)
	setElementValue(target.fixedLengthData, fieldWriteBit, numBits, uint64(len(varLenBuf[fieldIdx])))
}

// writeNullObjectField writes a hole/null value for a field that has no
// source this cycle (removed, or beyond the prior state's ordinal range).
func writeNullObjectField(target *objectShardData, fieldIdx int, isVarLen bool, fieldWriteBit int64, varLenBuf [][]byte) {
	numBits := target.bitsPerField[fieldIdx]
	if !isVarLen {
		setElementValue(target.fixedLengthData, fieldWriteBit, numBits, target.nullValueForField[fieldIdx])
		return
	}
	writeNullVarLengthObjectField(target, fieldIdx, numBits, fieldWriteBit, varLenBuf)
}

// writeNullVarLengthObjectField sets the null-flag bit while leaving the
// var-length write cursor at its current position, matching
// HollowObjectTypeDataElements#writeNullVarLengthField — a null STRING/BYTES
// field is a zero-width entry in the delta-pointer chain.
func writeNullVarLengthObjectField(target *objectShardData, fieldIdx int, numBits int, fieldWriteBit int64, varLenBuf [][]byte) {
	nullBit := uint64(1) << uint(numBits-1)
	writeVal := nullBit | uint64(len(varLenBuf[fieldIdx]))
	setElementValue(target.fixedLengthData, fieldWriteBit, numBits, writeVal)
}
