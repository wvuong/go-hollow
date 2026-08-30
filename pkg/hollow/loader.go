package hollow

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

func Loader(path string) {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	fileBytes, err := io.ReadAll(f)
	if err != nil {
		panic(err)
	}
	fmt.Println(path)
	fmt.Printf("file length bytes: %v\n", len(fileBytes))

	blob, err := readSnapshotBlob(bytes.NewReader(fileBytes))
	if err != nil {
		panic(err)
	}

	header := blob.Header
	fmt.Printf("blob format version: %v\n", header.BlobFormatVersion)
	fmt.Printf("origin randomized tag: %v\n", header.OriginRandomizedTag)
	fmt.Printf("destination randomized tag: %v\n", header.DestinationRandomizedTag)
	fmt.Printf("header tags: %v\n", header.HeaderTags)
	fmt.Printf("types: %v\n", len(blob.Types))

	byName := make(map[string]*TypeState, len(blob.Types))
	for _, t := range blob.Types {
		byName[t.Schema.Name()] = t
	}

	for _, t := range blob.Types {
		fmt.Printf("  %s %s (shards=%d maxOrdinal=%d populated=%d)\n",
			t.Schema.SchemaType(), t.Schema.Name(), t.NumShards, t.MaxOrdinal, t.Populated.Count())

		if t.Object != nil {
			printSampleRecords(t, byName)
		}
	}
}

// printSampleRecords prints up to 3 decoded records for an Object-schema
// type, resolving REFERENCE fields into String type into their values.
func printSampleRecords(t *TypeState, byName map[string]*TypeState) {
	schema := t.Object.Schema
	printed := 0
	for ordinal := int32(0); ordinal <= t.MaxOrdinal && printed < 3; ordinal++ {
		if !t.Populated.IsPopulated(ordinal) {
			continue
		}
		fmt.Printf("    [%d] %s\n", ordinal, formatRecord(t.Object, schema, ordinal, byName))
		printed++
	}
}

func formatRecord(data *ObjectTypeData, schema *ObjectSchema, ordinal int32, byName map[string]*TypeState) string {
	out := "{"
	for i, field := range schema.Fields {
		if i > 0 {
			out += ", "
		}
		out += field.Name + "="
		switch field.Type {
		case FieldTypeInt:
			if v, ok := data.GetInt(ordinal, i); ok {
				out += fmt.Sprintf("%d", v)
			} else {
				out += "null"
			}
		case FieldTypeLong:
			if v, ok := data.GetLong(ordinal, i); ok {
				out += fmt.Sprintf("%d", v)
			} else {
				out += "null"
			}
		case FieldTypeFloat:
			if v, ok := data.GetFloat(ordinal, i); ok {
				out += fmt.Sprintf("%v", v)
			} else {
				out += "null"
			}
		case FieldTypeDouble:
			if v, ok := data.GetDouble(ordinal, i); ok {
				out += fmt.Sprintf("%v", v)
			} else {
				out += "null"
			}
		case FieldTypeBoolean:
			if v, ok := data.GetBoolean(ordinal, i); ok {
				out += fmt.Sprintf("%v", v)
			} else {
				out += "null"
			}
		case FieldTypeString:
			if v, ok := data.GetString(ordinal, i); ok {
				out += fmt.Sprintf("%q", v)
			} else {
				out += "null"
			}
		case FieldTypeBytes:
			if v, ok := data.GetBytes(ordinal, i); ok {
				out += fmt.Sprintf("<%d bytes>", len(v))
			} else {
				out += "null"
			}
		case FieldTypeReference:
			refOrdinal, ok := data.GetReference(ordinal, i)
			if !ok {
				out += "null"
				break
			}
			if referenced, isString := byName[field.ReferencedType]; isString && referenced.Object != nil && field.ReferencedType == "String" {
				if v, ok := referenced.Object.GetString(refOrdinal, 0); ok {
					out += fmt.Sprintf("%s(%d)=%q", field.ReferencedType, refOrdinal, v)
					break
				}
			}
			out += fmt.Sprintf("%s(%d)", field.ReferencedType, refOrdinal)
		}
	}
	return out + "}"
}
