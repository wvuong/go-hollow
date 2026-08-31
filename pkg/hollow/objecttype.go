package hollow

import (
	"fmt"
	"math"
	"math/bits"
	"unicode/utf16"
)

// objectShardData is one shard's decoded fields for an ObjectSchema type,
// matching HollowObjectTypeDataElements.
type objectShardData struct {
	maxOrdinal        int32
	bitsPerField      []int
	bitOffsetPerField []int
	nullValueForField []uint64
	bitsPerRecord     int

	fixedLengthData []uint64
	// varLengthData[fieldIndex] is nil for fixed-length fields (everything
	// but STRING/BYTES).
	varLengthData [][]byte
}

// ObjectTypeData holds the fully decoded records for one Object-schema type
// from a Hollow blob, matching (the read side of) HollowObjectTypeReadState.
type ObjectTypeData struct {
	Schema            *ObjectSchema
	MaxOrdinal        int32
	shardOrdinalShift uint
	shardMask         int32
	shards            []*objectShardData
}

// readObjectTypeData reads all shards of one Object-schema type's snapshot
// data, matching HollowObjectTypeReadState#readSnapshot +
// HollowObjectTypeDataElements#readFromInput (isDelta=false).
func readObjectTypeData(r byteReader, schema *ObjectSchema, numShards int32) (*ObjectTypeData, error) {
	data := &ObjectTypeData{
		Schema:            schema,
		shardOrdinalShift: uint(bits.Len32(uint32(numShards)) - 1),
		shardMask:         numShards - 1,
		shards:            make([]*objectShardData, numShards),
	}

	if numShards > 1 {
		overallMaxOrdinal, err := readVInt(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading %s overall max ordinal: %w", schema.Name(), err)
		}
		data.MaxOrdinal = overallMaxOrdinal
	}

	for i := int32(0); i < numShards; i++ {
		shard, err := readObjectShardData(r, schema)
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

func readObjectShardData(r byteReader, schema *ObjectSchema) (*objectShardData, error) {
	maxOrdinal, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("reading max ordinal: %w", err)
	}

	shard, err := readObjectShardFieldsAndData(r, len(schema.Fields))
	if err != nil {
		return nil, err
	}
	shard.maxOrdinal = maxOrdinal
	return shard, nil
}

// readObjectShardFieldsAndData reads the bits-per-field statistics,
// fixed-length data, and var-length data that follow a shard's max ordinal
// (snapshot) or removals/additions (delta) — the part of
// HollowObjectTypeDataElements#readFromInput that's identical for both.
func readObjectShardFieldsAndData(r byteReader, numFields int) (*objectShardData, error) {
	shard := &objectShardData{
		bitsPerField:      make([]int, numFields),
		bitOffsetPerField: make([]int, numFields),
		nullValueForField: make([]uint64, numFields),
		varLengthData:     make([][]byte, numFields),
	}

	bitOffset := 0
	for i := 0; i < numFields; i++ {
		fieldBits, err := readVInt(r)
		if err != nil {
			return nil, fmt.Errorf("reading bits-per-field %d: %w", i, err)
		}
		shard.bitsPerField[i] = int(fieldBits)
		shard.nullValueForField[i] = nullValueForBits(int(fieldBits))
		shard.bitOffsetPerField[i] = bitOffset
		bitOffset += int(fieldBits)
	}
	shard.bitsPerRecord = bitOffset

	var err error
	shard.fixedLengthData, err = readFixedLengthData(r)
	if err != nil {
		return nil, fmt.Errorf("reading fixed-length data: %w", err)
	}

	for i := 0; i < numFields; i++ {
		numBytes, err := readVLong(r)
		if err != nil {
			return nil, fmt.Errorf("reading var-length byte count for field %d: %w", i, err)
		}
		if numBytes > 0 {
			buf := make([]byte, numBytes)
			if err := readFull(r, buf); err != nil {
				return nil, fmt.Errorf("reading var-length data for field %d: %w", i, err)
			}
			shard.varLengthData[i] = buf
		}
	}

	return shard, nil
}

func (d *ObjectTypeData) shardFor(ordinal int32) (*objectShardData, int32) {
	shard := d.shards[ordinal&d.shardMask]
	return shard, ordinal >> d.shardOrdinalShift
}

func (s *objectShardData) fieldOffset(shardOrdinal int32, fieldIndex int) int64 {
	return int64(s.bitsPerRecord)*int64(shardOrdinal) + int64(s.bitOffsetPerField[fieldIndex])
}

func (s *objectShardData) rawFixedValue(shardOrdinal int32, fieldIndex int) uint64 {
	bitOffset := s.fieldOffset(shardOrdinal, fieldIndex)
	return getElementValue(s.fixedLengthData, bitOffset, s.bitsPerField[fieldIndex])
}

// GetInt reads an INT field, matching HollowObjectTypeReadState#readInt.
// ok is false if the field is null.
func (d *ObjectTypeData) GetInt(ordinal int32, fieldIndex int) (value int32, ok bool) {
	shard, shardOrdinal := d.shardFor(ordinal)
	raw := shard.rawFixedValue(shardOrdinal, fieldIndex)
	if raw == shard.nullValueForField[fieldIndex] {
		return 0, false
	}
	return zigzagDecodeInt32(uint32(raw)), true
}

// GetLong reads a LONG field, matching HollowObjectTypeReadState#readLong.
func (d *ObjectTypeData) GetLong(ordinal int32, fieldIndex int) (value int64, ok bool) {
	shard, shardOrdinal := d.shardFor(ordinal)
	raw := shard.rawFixedValue(shardOrdinal, fieldIndex)
	if raw == shard.nullValueForField[fieldIndex] {
		return 0, false
	}
	return zigzagDecodeInt64(raw), true
}

// GetFloat reads a FLOAT field, matching HollowObjectTypeReadState#readFloat.
func (d *ObjectTypeData) GetFloat(ordinal int32, fieldIndex int) (value float32, ok bool) {
	shard, shardOrdinal := d.shardFor(ordinal)
	raw := uint32(shard.rawFixedValue(shardOrdinal, fieldIndex))
	if raw == nullFloatBits {
		return 0, false
	}
	return math.Float32frombits(raw), true
}

// GetDouble reads a DOUBLE field, matching HollowObjectTypeReadState#readDouble.
// The value is always stored in a full 64-bit element regardless of the
// field's declared bit width, matching the Java reader.
func (d *ObjectTypeData) GetDouble(ordinal int32, fieldIndex int) (value float64, ok bool) {
	shard, shardOrdinal := d.shardFor(ordinal)
	bitOffset := shard.fieldOffset(shardOrdinal, fieldIndex)
	raw := getElementValue(shard.fixedLengthData, bitOffset, 64)
	if raw == nullDoubleBits {
		return 0, false
	}
	return math.Float64frombits(raw), true
}

// GetBoolean reads a BOOLEAN field, matching HollowObjectTypeReadState#readBoolean.
func (d *ObjectTypeData) GetBoolean(ordinal int32, fieldIndex int) (value bool, ok bool) {
	shard, shardOrdinal := d.shardFor(ordinal)
	raw := shard.rawFixedValue(shardOrdinal, fieldIndex)
	if raw == shard.nullValueForField[fieldIndex] {
		return false, false
	}
	return raw == 1, true
}

// GetReference reads a REFERENCE field, returning the referenced type's
// ordinal, matching HollowObjectTypeReadState#readOrdinal.
func (d *ObjectTypeData) GetReference(ordinal int32, fieldIndex int) (refOrdinal int32, ok bool) {
	shard, shardOrdinal := d.shardFor(ordinal)
	raw := shard.rawFixedValue(shardOrdinal, fieldIndex)
	if raw == shard.nullValueForField[fieldIndex] {
		return 0, false
	}
	return int32(raw), true
}

// varLengthRange returns the [startByte, endByte) range of a STRING/BYTES
// field's bytes within varLengthData[fieldIndex], matching the startByte/
// endByte computation shared by HollowObjectTypeReadState#readString and
// #readBytes.
func (s *objectShardData) varLengthRange(shardOrdinal int32, fieldIndex int) (start, end int64, isNull bool) {
	numBits := s.bitsPerField[fieldIndex]
	bitOffset := s.fieldOffset(shardOrdinal, fieldIndex)

	endRaw := getElementValue(s.fixedLengthData, bitOffset, numBits)
	nullBit := uint64(1) << uint(numBits-1)
	if endRaw&nullBit != 0 {
		return 0, 0, true
	}

	var startRaw uint64
	if shardOrdinal != 0 {
		startRaw = getElementValue(s.fixedLengthData, bitOffset-int64(s.bitsPerRecord), numBits)
	}
	startRaw &= nullBit - 1

	return int64(startRaw), int64(endRaw), false
}

// GetBytes reads a BYTES field, matching HollowObjectTypeReadState#readBytes.
func (d *ObjectTypeData) GetBytes(ordinal int32, fieldIndex int) ([]byte, bool) {
	shard, shardOrdinal := d.shardFor(ordinal)
	start, end, isNull := shard.varLengthRange(shardOrdinal, fieldIndex)
	if isNull {
		return nil, false
	}
	return shard.varLengthData[fieldIndex][start:end], true
}

// GetString reads a STRING field, matching HollowObjectTypeReadState#readString.
// Per go-hollow-porting-notes.md §6, Hollow encodes strings as a VarInt per
// UTF-16 code unit (not UTF-8 bytes), so surrogate pairs must be reassembled
// via unicode/utf16.
func (d *ObjectTypeData) GetString(ordinal int32, fieldIndex int) (string, bool) {
	shard, shardOrdinal := d.shardFor(ordinal)
	start, end, isNull := shard.varLengthRange(shardOrdinal, fieldIndex)
	if isNull {
		return "", false
	}
	data := shard.varLengthData[fieldIndex][start:end]

	units := make([]uint16, 0, len(data))
	for pos := 0; pos < len(data); {
		v, nextPos, err := readVIntFromBytes(data, pos)
		if err != nil {
			return "", false
		}
		units = append(units, uint16(v))
		pos = nextPos
	}
	return string(utf16.Decode(units)), true
}
