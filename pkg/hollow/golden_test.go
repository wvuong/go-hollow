package hollow

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// findSnapshotFixture returns the path to the first snapshot-* blob in
// fakehollowdata/, or "" if none is present.
func findSnapshotFixture(t *testing.T) string {
	t.Helper()

	matches, err := filepath.Glob("../../fakehollowdata/snapshot-*")
	if err != nil {
		t.Fatalf("globbing for snapshot fixture: %v", err)
	}
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// TestGoldenSnapshot parses a real Hollow snapshot blob produced by the
// Java hollow-fakedata generator and checks it end-to-end: schema shape,
// and full referential/structural self-consistency across every decoded
// record. It's skipped when fakehollowdata/ hasn't been populated locally
// (see fakehollowdata/README.md) — this repo intentionally doesn't commit
// the (500MB) fixture blobs.
func TestGoldenSnapshot(t *testing.T) {
	path := findSnapshotFixture(t)
	if path == "" {
		t.Skip("no fakehollowdata/snapshot-* fixture found; see fakehollowdata/README.md to populate it")
	}

	fileBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	blob, err := readSnapshotBlob(bytes.NewReader(fileBytes))
	if err != nil {
		t.Fatalf("readSnapshotBlob(%s): %v", path, err)
	}

	if blob.Header.BlobFormatVersion != HollowBlobVersionHeader {
		t.Errorf("BlobFormatVersion = %d, want %d", blob.Header.BlobFormatVersion, HollowBlobVersionHeader)
	}

	const wantTypeCount = 18 // the fakedata book-catalog schema never changes shape across cycles
	if len(blob.Types) != wantTypeCount {
		t.Errorf("len(blob.Types) = %d, want %d", len(blob.Types), wantTypeCount)
	}

	byName := make(map[string]*TypeState, len(blob.Types))
	for _, ts := range blob.Types {
		byName[ts.Schema.Name()] = ts
	}

	checkSchemaShape(t, byName)
	checkObjectTypesSelfConsistent(t, blob, byName)
	checkListTypesSelfConsistent(t, blob, byName)
	checkSetTypesSelfConsistent(t, blob, byName)
	checkMapTypesSelfConsistent(t, blob, byName)
}

// checkSchemaShape asserts on facts about the fakedata schema that are
// fixed across every cycle in the delta chain (only the data changes).
func checkSchemaShape(t *testing.T, byName map[string]*TypeState) {
	t.Helper()

	bookID, ok := byName["BookId"].Schema.(*ObjectSchema)
	if !ok {
		t.Fatal("BookId schema missing or not an ObjectSchema")
	}
	if len(bookID.Fields) != 1 || bookID.Fields[0].Name != "value" || bookID.Fields[0].Type != FieldTypeInt {
		t.Errorf("BookId.Fields = %+v, want single INT field named \"value\"", bookID.Fields)
	}

	book, ok := byName["Book"].Schema.(*ObjectSchema)
	if !ok {
		t.Fatal("Book schema missing or not an ObjectSchema")
	}
	wantBookFields := map[string]string{
		"id":           "BookId",
		"country":      "Country",
		"images":       "BookImages",
		"bookMetadata": "BookMetadata",
	}
	if len(book.Fields) != len(wantBookFields) {
		t.Errorf("len(Book.Fields) = %d, want %d", len(book.Fields), len(wantBookFields))
	}
	for _, f := range book.Fields {
		if f.Type != FieldTypeReference {
			t.Errorf("Book field %q has type %v, want REFERENCE", f.Name, f.Type)
			continue
		}
		if want, ok := wantBookFields[f.Name]; !ok || f.ReferencedType != want {
			t.Errorf("Book field %q references %q, want %q", f.Name, f.ReferencedType, want)
		}
	}

	listOfArt, ok := byName["ListOfArt"].Schema.(*ListSchema)
	if !ok || listOfArt.ElementType != "Art" {
		t.Errorf("ListOfArt schema = %+v, want ListSchema with ElementType \"Art\"", byName["ListOfArt"].Schema)
	}

	setOfString, ok := byName["SetOfString"].Schema.(*SetSchema)
	if !ok || setOfString.ElementType != "String" {
		t.Errorf("SetOfString schema = %+v, want SetSchema with ElementType \"String\"", byName["SetOfString"].Schema)
	}

	m, ok := byName["MapOfStringToListOfArt"].Schema.(*MapSchema)
	if !ok || m.KeyType != "String" || m.ValueType != "ListOfArt" {
		t.Errorf("MapOfStringToListOfArt schema = %+v, want MapSchema{KeyType: String, ValueType: ListOfArt}", byName["MapOfStringToListOfArt"].Schema)
	}
}

// checkObjectTypesSelfConsistent decodes every populated record of every
// Object-schema type and verifies every REFERENCE field points at a
// populated ordinal within the target type's range. This exercises the
// full bit-packing/var-length decode path (VarInt, UTF-16 string decode,
// zigzag, multi-shard dispatch) across the entire blob.
func checkObjectTypesSelfConsistent(t *testing.T, blob *Blob, byName map[string]*TypeState) {
	t.Helper()

	for _, ts := range blob.Types {
		if ts.Object == nil {
			continue
		}
		schema := ts.Object.Schema

		for ordinal := int32(0); ordinal <= ts.MaxOrdinal; ordinal++ {
			if !ts.Populated.IsPopulated(ordinal) {
				continue
			}

			for i, field := range schema.Fields {
				switch field.Type {
				case FieldTypeInt:
					ts.Object.GetInt(ordinal, i)
				case FieldTypeLong:
					ts.Object.GetLong(ordinal, i)
				case FieldTypeFloat:
					ts.Object.GetFloat(ordinal, i)
				case FieldTypeDouble:
					ts.Object.GetDouble(ordinal, i)
				case FieldTypeBoolean:
					ts.Object.GetBoolean(ordinal, i)
				case FieldTypeString:
					ts.Object.GetString(ordinal, i)
				case FieldTypeBytes:
					ts.Object.GetBytes(ordinal, i)
				case FieldTypeReference:
					refOrdinal, ok := ts.Object.GetReference(ordinal, i)
					if !ok {
						continue
					}
					target := byName[field.ReferencedType]
					if target == nil {
						t.Fatalf("%s[%d].%s references unknown type %q", schema.Name(), ordinal, field.Name, field.ReferencedType)
					}
					if refOrdinal < 0 || refOrdinal > target.MaxOrdinal {
						t.Fatalf("%s[%d].%s = %s(%d), out of range [0, %d]",
							schema.Name(), ordinal, field.Name, field.ReferencedType, refOrdinal, target.MaxOrdinal)
					}
					if !target.Populated.IsPopulated(refOrdinal) {
						t.Fatalf("%s[%d].%s = %s(%d), but that ordinal is not populated",
							schema.Name(), ordinal, field.Name, field.ReferencedType, refOrdinal)
					}
				}
			}
		}
	}
}

// checkListTypesSelfConsistent verifies Size() agrees with Elements(), and
// every element ordinal is populated in its target type.
func checkListTypesSelfConsistent(t *testing.T, blob *Blob, byName map[string]*TypeState) {
	t.Helper()

	for _, ts := range blob.Types {
		if ts.List == nil {
			continue
		}
		target := byName[ts.List.Schema.ElementType]

		for ordinal := int32(0); ordinal <= ts.MaxOrdinal; ordinal++ {
			if !ts.Populated.IsPopulated(ordinal) {
				continue
			}
			elements := ts.List.Elements(ordinal)
			if len(elements) != ts.List.Size(ordinal) {
				t.Fatalf("%s[%d]: len(Elements()) = %d, Size() = %d", ts.Schema.Name(), ordinal, len(elements), ts.List.Size(ordinal))
			}
			for _, e := range elements {
				if e < 0 || e > target.MaxOrdinal || !target.Populated.IsPopulated(e) {
					t.Fatalf("%s[%d] contains unpopulated/out-of-range element %s(%d)", ts.Schema.Name(), ordinal, ts.List.Schema.ElementType, e)
				}
			}
		}
	}
}

// checkSetTypesSelfConsistent mirrors checkListTypesSelfConsistent for Set
// types (bucket-table walk instead of a flat pointer array).
func checkSetTypesSelfConsistent(t *testing.T, blob *Blob, byName map[string]*TypeState) {
	t.Helper()

	for _, ts := range blob.Types {
		if ts.Set == nil {
			continue
		}
		target := byName[ts.Set.Schema.ElementType]

		for ordinal := int32(0); ordinal <= ts.MaxOrdinal; ordinal++ {
			if !ts.Populated.IsPopulated(ordinal) {
				continue
			}
			elements := ts.Set.Elements(ordinal)
			if len(elements) != ts.Set.Size(ordinal) {
				t.Fatalf("%s[%d]: len(Elements()) = %d, Size() = %d", ts.Schema.Name(), ordinal, len(elements), ts.Set.Size(ordinal))
			}
			for _, e := range elements {
				if e < 0 || e > target.MaxOrdinal || !target.Populated.IsPopulated(e) {
					t.Fatalf("%s[%d] contains unpopulated/out-of-range element %s(%d)", ts.Schema.Name(), ordinal, ts.Set.Schema.ElementType, e)
				}
			}
		}
	}
}

// checkMapTypesSelfConsistent mirrors checkListTypesSelfConsistent for Map
// types: every key and value ordinal must be populated in their respective
// target types.
func checkMapTypesSelfConsistent(t *testing.T, blob *Blob, byName map[string]*TypeState) {
	t.Helper()

	for _, ts := range blob.Types {
		if ts.Map == nil {
			continue
		}
		keyTarget := byName[ts.Map.Schema.KeyType]
		valueTarget := byName[ts.Map.Schema.ValueType]

		for ordinal := int32(0); ordinal <= ts.MaxOrdinal; ordinal++ {
			if !ts.Populated.IsPopulated(ordinal) {
				continue
			}
			entries := ts.Map.Entries(ordinal)
			if len(entries) != ts.Map.Size(ordinal) {
				t.Fatalf("%s[%d]: len(Entries()) = %d, Size() = %d", ts.Schema.Name(), ordinal, len(entries), ts.Map.Size(ordinal))
			}
			for _, e := range entries {
				if e.Key < 0 || e.Key > keyTarget.MaxOrdinal || !keyTarget.Populated.IsPopulated(e.Key) {
					t.Fatalf("%s[%d] contains unpopulated/out-of-range key %s(%d)", ts.Schema.Name(), ordinal, ts.Map.Schema.KeyType, e.Key)
				}
				if e.Value < 0 || e.Value > valueTarget.MaxOrdinal || !valueTarget.Populated.IsPopulated(e.Value) {
					t.Fatalf("%s[%d] contains unpopulated/out-of-range value %s(%d)", ts.Schema.Name(), ordinal, ts.Map.Schema.ValueType, e.Value)
				}
			}
		}
	}
}
