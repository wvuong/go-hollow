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

	reader := bytes.NewReader(fileBytes)
	header, err := readHeader(reader)
	if err != nil {
		panic(err)
	}

	fmt.Printf("blob format version: %v\n", header.BlobFormatVersion)
	fmt.Printf("origin randomized tag: %v\n", header.OriginRandomizedTag)
	fmt.Printf("destination randomized tag: %v\n", header.DestinationRandomizedTag)
	fmt.Printf("schemas: %v\n", len(header.Schemas))
	for _, schema := range header.Schemas {
		switch s := schema.(type) {
		case *ObjectSchema:
			fmt.Printf("  OBJECT %s (%d fields, key=%v)\n", s.Name(), len(s.Fields), s.KeyFieldPaths)
			for _, f := range s.Fields {
				if f.Type == FieldTypeReference {
					fmt.Printf("    %s: REFERENCE(%s)\n", f.Name, f.ReferencedType)
				} else {
					fmt.Printf("    %s: %s\n", f.Name, f.Type)
				}
			}
		case *ListSchema:
			fmt.Printf("  LIST %s (element=%s)\n", s.Name(), s.ElementType)
		case *SetSchema:
			fmt.Printf("  SET %s (element=%s, hashKey=%v)\n", s.Name(), s.ElementType, s.HashKeyFields)
		case *MapSchema:
			fmt.Printf("  MAP %s (key=%s, value=%s, hashKey=%v)\n", s.Name(), s.KeyType, s.ValueType, s.HashKeyFields)
		}
	}
	fmt.Printf("header tags: %v\n", header.HeaderTags)
}
