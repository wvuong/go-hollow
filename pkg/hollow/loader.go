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

		switch {
		case t.Object != nil:
			printSampleRecords(t, byName)
		case t.List != nil:
			printSampleLists(t)
		case t.Set != nil:
			printSampleSets(t)
		case t.Map != nil:
			printSampleMaps(t)
		}
	}
}

// printSampleLists prints up to 3 decoded lists' element ordinals.
func printSampleLists(t *TypeState) {
	printed := 0
	for ordinal := int32(0); ordinal <= t.MaxOrdinal && printed < 3; ordinal++ {
		if !t.Populated.IsPopulated(ordinal) {
			continue
		}
		fmt.Printf("    [%d] %s %v\n", ordinal, t.List.Schema.ElementType, t.List.Elements(ordinal))
		printed++
	}
}

// printSampleSets prints up to 3 decoded sets' element ordinals.
func printSampleSets(t *TypeState) {
	printed := 0
	for ordinal := int32(0); ordinal <= t.MaxOrdinal && printed < 3; ordinal++ {
		if !t.Populated.IsPopulated(ordinal) {
			continue
		}
		fmt.Printf("    [%d] %s %v\n", ordinal, t.Set.Schema.ElementType, t.Set.Elements(ordinal))
		printed++
	}
}

// printSampleMaps prints up to 3 decoded maps' key/value ordinal pairs.
func printSampleMaps(t *TypeState) {
	printed := 0
	for ordinal := int32(0); ordinal <= t.MaxOrdinal && printed < 3; ordinal++ {
		if !t.Populated.IsPopulated(ordinal) {
			continue
		}
		fmt.Printf("    [%d] %s->%s%v\n", ordinal, t.Map.Schema.KeyType, t.Map.Schema.ValueType, t.Map.Entries(ordinal))
		printed++
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
			referenced := byName[field.ReferencedType]
			out += formatReference(field.ReferencedType, refOrdinal, referenced)
		}
	}
	return out + "}"
}

// formatReference renders a REFERENCE field's target: a String's value, a
// collection's size, or just its type and ordinal.
func formatReference(typeName string, refOrdinal int32, referenced *TypeState) string {
	switch {
	case referenced == nil:
	case typeName == "String" && referenced.Object != nil:
		if v, ok := referenced.Object.GetString(refOrdinal, 0); ok {
			return fmt.Sprintf("%s(%d)=%q", typeName, refOrdinal, v)
		}
	case referenced.List != nil:
		return fmt.Sprintf("%s(%d)[%d items]", typeName, refOrdinal, referenced.List.Size(refOrdinal))
	case referenced.Set != nil:
		return fmt.Sprintf("%s(%d)[%d items]", typeName, refOrdinal, referenced.Set.Size(refOrdinal))
	case referenced.Map != nil:
		return fmt.Sprintf("%s(%d)[%d items]", typeName, refOrdinal, referenced.Map.Size(refOrdinal))
	}
	return fmt.Sprintf("%s(%d)", typeName, refOrdinal)
}
