package hollow

import (
	"fmt"
	"io"
)

// HollowBlobVersionHeader is the expected magic value of the first 4 bytes
// of every Hollow blob, matching HollowBlobHeader.HOLLOW_BLOB_VERSION_HEADER.
const HollowBlobVersionHeader int32 = 1030

// BlobHeader is the parsed header of a Hollow blob, matching
// com.netflix.hollow.core.HollowBlobHeader.
type BlobHeader struct {
	BlobFormatVersion        int32
	OriginRandomizedTag      int64
	DestinationRandomizedTag int64
	// Schemas is only populated for header formats that embed schemas
	// inline (a nonzero legacy "old bytes to skip" VarInt signals this).
	Schemas    []Schema
	HeaderTags map[string]string
}

// readHeader reads a Hollow blob header, matching
// HollowBlobHeaderReader#readHeader.
func readHeader(r byteReader) (*BlobHeader, error) {
	header := &BlobHeader{}

	if err := readBigEndian(r, &header.BlobFormatVersion); err != nil {
		return nil, fmt.Errorf("hollow: reading blob format version: %w", err)
	}
	if header.BlobFormatVersion != HollowBlobVersionHeader {
		return nil, fmt.Errorf("hollow: incompatible blob version: expected %d, got %d",
			HollowBlobVersionHeader, header.BlobFormatVersion)
	}

	if err := readBigEndian(r, &header.OriginRandomizedTag); err != nil {
		return nil, fmt.Errorf("hollow: reading origin randomized tag: %w", err)
	}
	if err := readBigEndian(r, &header.DestinationRandomizedTag); err != nil {
		return nil, fmt.Errorf("hollow: reading destination randomized tag: %w", err)
	}

	// Pre-v2.2.0 envelope byte count; nonzero here signals that schemas are
	// embedded directly in the header.
	oldBytesToSkip, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading old-bytes-to-skip: %w", err)
	}

	if oldBytesToSkip != 0 {
		schemas, err := readSchemas(r)
		if err != nil {
			return nil, err
		}
		header.Schemas = schemas

		if err := skipForwardCompatibilityBytes(r); err != nil {
			return nil, err
		}
	}

	headerTags, err := readHeaderTags(r)
	if err != nil {
		return nil, err
	}
	header.HeaderTags = headerTags

	return header, nil
}

func readSchemas(r byteReader) ([]Schema, error) {
	numSchemas, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading schema count: %w", err)
	}

	schemas := make([]Schema, numSchemas)
	for i := range schemas {
		schemas[i], err = readSchema(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading schema %d: %w", i, err)
		}
	}
	return schemas, nil
}

func skipForwardCompatibilityBytes(r byteReader) error {
	bytesToSkip, err := readVInt(r)
	if err != nil {
		return fmt.Errorf("hollow: reading forward-compatibility byte count: %w", err)
	}
	if bytesToSkip > 0 {
		if _, err := io.CopyN(io.Discard, r, int64(bytesToSkip)); err != nil {
			return fmt.Errorf("hollow: skipping forward-compatibility bytes: %w", err)
		}
	}
	return nil
}

func readHeaderTags(r byteReader) (map[string]string, error) {
	var numHeaderTags int16
	if err := readBigEndian(r, &numHeaderTags); err != nil {
		return nil, fmt.Errorf("hollow: reading header tag count: %w", err)
	}

	headerTags := make(map[string]string)
	for i := 0; i < int(numHeaderTags); i++ {
		key, err := readUTF(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading header tag %d key: %w", i, err)
		}
		value, err := readUTF(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading header tag %d value: %w", i, err)
		}
		headerTags[key] = value
	}
	return headerTags, nil
}
