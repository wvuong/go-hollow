package hollow

import (
	"fmt"
	"io"
)

// byteReader is satisfied by *bytes.Reader (and *bufio.Reader): it's what
// readVInt/readVLong (io.ByteReader) and readUTF (io.Reader) both need.
type byteReader interface {
	io.Reader
	io.ByteReader
}

// FieldType is a HollowObjectSchema field type, matching
// com.netflix.hollow.core.schema.HollowObjectSchema.FieldType.
type FieldType string

const (
	FieldTypeReference FieldType = "REFERENCE"
	FieldTypeInt       FieldType = "INT"
	FieldTypeLong      FieldType = "LONG"
	FieldTypeBoolean   FieldType = "BOOLEAN"
	FieldTypeFloat     FieldType = "FLOAT"
	FieldTypeDouble    FieldType = "DOUBLE"
	FieldTypeString    FieldType = "STRING"
	FieldTypeBytes     FieldType = "BYTES"
)

// SchemaType identifies which of the four HollowSchema shapes a Schema is.
type SchemaType int

const (
	SchemaTypeObject SchemaType = iota
	SchemaTypeSet
	SchemaTypeList
	SchemaTypeMap
)

func (t SchemaType) String() string {
	switch t {
	case SchemaTypeObject:
		return "OBJECT"
	case SchemaTypeSet:
		return "SET"
	case SchemaTypeList:
		return "LIST"
	case SchemaTypeMap:
		return "MAP"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(t))
	}
}

// Schema is implemented by ObjectSchema, ListSchema, SetSchema, and MapSchema.
type Schema interface {
	Name() string
	SchemaType() SchemaType
}

// ObjectField is a single field of an ObjectSchema.
type ObjectField struct {
	Name string
	Type FieldType
	// ReferencedType is the name of the referenced type, set only when
	// Type == FieldTypeReference.
	ReferencedType string
}

// ObjectSchema corresponds to Java's HollowObjectSchema: a fixed set of
// typed fields.
type ObjectSchema struct {
	SchemaName string
	// KeyFieldPaths is non-nil when the schema declares a primary key.
	KeyFieldPaths []string
	Fields        []ObjectField
}

func (s *ObjectSchema) Name() string           { return s.SchemaName }
func (s *ObjectSchema) SchemaType() SchemaType { return SchemaTypeObject }

// ListSchema corresponds to Java's HollowListSchema: an ordered collection
// of records of ElementType.
type ListSchema struct {
	SchemaName  string
	ElementType string
}

func (s *ListSchema) Name() string           { return s.SchemaName }
func (s *ListSchema) SchemaType() SchemaType { return SchemaTypeList }

// SetSchema corresponds to Java's HollowSetSchema: an unordered collection
// of records of ElementType, without duplicates.
type SetSchema struct {
	SchemaName  string
	ElementType string
	// HashKeyFields is non-nil when the schema declares a hash key.
	HashKeyFields []string
}

func (s *SetSchema) Name() string           { return s.SchemaName }
func (s *SetSchema) SchemaType() SchemaType { return SchemaTypeSet }

// MapSchema corresponds to Java's HollowMapSchema: a key/value mapping
// between KeyType and ValueType.
type MapSchema struct {
	SchemaName string
	KeyType    string
	ValueType  string
	// HashKeyFields is non-nil when the schema declares a hash key.
	HashKeyFields []string
}

func (s *MapSchema) Name() string           { return s.SchemaName }
func (s *MapSchema) SchemaType() SchemaType { return SchemaTypeMap }

// schemaTypeID mirrors HollowSchema.SchemaType.fromTypeId / hasKey: the
// on-the-wire type id byte doubles as a "has key" flag for OBJECT/SET/MAP.
func schemaTypeFromTypeID(id byte) (SchemaType, bool, error) {
	switch id {
	case 0:
		return SchemaTypeObject, false, nil
	case 6:
		return SchemaTypeObject, true, nil
	case 1:
		return SchemaTypeSet, false, nil
	case 4:
		return SchemaTypeSet, true, nil
	case 2:
		return SchemaTypeList, false, nil
	case 3:
		return SchemaTypeMap, false, nil
	case 5:
		return SchemaTypeMap, true, nil
	default:
		return 0, false, fmt.Errorf("hollow: unrecognized schema type id %d", id)
	}
}

// readSchema reads a single HollowSchema, matching HollowSchema#readFrom.
func readSchema(r byteReader) (Schema, error) {
	typeIDByte, err := r.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("hollow: reading schema type id: %w", err)
	}

	schemaType, hasKey, err := schemaTypeFromTypeID(typeIDByte)
	if err != nil {
		return nil, err
	}

	name, err := readUTF(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading schema name: %w", err)
	}

	switch schemaType {
	case SchemaTypeObject:
		return readObjectSchema(r, name, hasKey)
	case SchemaTypeList:
		return readListSchema(r, name)
	case SchemaTypeSet:
		return readSetSchema(r, name, hasKey)
	case SchemaTypeMap:
		return readMapSchema(r, name, hasKey)
	default:
		return nil, fmt.Errorf("hollow: unhandled schema type %v", schemaType)
	}
}

func readKeyFieldPaths(r byteReader) ([]string, error) {
	numFields, err := readVInt(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading key field count: %w", err)
	}
	paths := make([]string, numFields)
	for i := range paths {
		paths[i], err = readUTF(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading key field path %d: %w", i, err)
		}
	}
	return paths, nil
}

func readObjectSchema(r byteReader, name string, hasPrimaryKey bool) (*ObjectSchema, error) {
	schema := &ObjectSchema{SchemaName: name}

	if hasPrimaryKey {
		paths, err := readKeyFieldPaths(r)
		if err != nil {
			return nil, err
		}
		schema.KeyFieldPaths = paths
	}

	var numFields uint16
	if err := readBigEndian(r, &numFields); err != nil {
		return nil, fmt.Errorf("hollow: reading object field count: %w", err)
	}

	schema.Fields = make([]ObjectField, numFields)
	for i := range schema.Fields {
		fieldName, err := readUTF(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading field %d name: %w", i, err)
		}
		fieldTypeStr, err := readUTF(r)
		if err != nil {
			return nil, fmt.Errorf("hollow: reading field %d type: %w", i, err)
		}
		fieldType := FieldType(fieldTypeStr)

		var referencedType string
		if fieldType == FieldTypeReference {
			referencedType, err = readUTF(r)
			if err != nil {
				return nil, fmt.Errorf("hollow: reading field %d referenced type: %w", i, err)
			}
		}

		schema.Fields[i] = ObjectField{Name: fieldName, Type: fieldType, ReferencedType: referencedType}
	}

	return schema, nil
}

func readListSchema(r byteReader, name string) (*ListSchema, error) {
	elementType, err := readUTF(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading list element type: %w", err)
	}
	return &ListSchema{SchemaName: name, ElementType: elementType}, nil
}

func readSetSchema(r byteReader, name string, hasHashKey bool) (*SetSchema, error) {
	elementType, err := readUTF(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading set element type: %w", err)
	}

	schema := &SetSchema{SchemaName: name, ElementType: elementType}
	if hasHashKey {
		fields, err := readKeyFieldPaths(r)
		if err != nil {
			return nil, err
		}
		schema.HashKeyFields = fields
	}
	return schema, nil
}

func readMapSchema(r byteReader, name string, hasHashKey bool) (*MapSchema, error) {
	keyType, err := readUTF(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading map key type: %w", err)
	}
	valueType, err := readUTF(r)
	if err != nil {
		return nil, fmt.Errorf("hollow: reading map value type: %w", err)
	}

	schema := &MapSchema{SchemaName: name, KeyType: keyType, ValueType: valueType}
	if hasHashKey {
		fields, err := readKeyFieldPaths(r)
		if err != nil {
			return nil, err
		}
		schema.HashKeyFields = fields
	}
	return schema, nil
}
