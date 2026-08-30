package main

import (
	"fmt"
	"os"

	loader "willvuong.com/go-hollow/pkg/hollow"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <hollow-blob-path>\n", os.Args[0])
		os.Exit(1)
	}

	loader.Loader(os.Args[1])
}
