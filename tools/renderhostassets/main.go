package main

import (
	"fmt"
	"os"
	"path/filepath"

	"loki/internal/host/assets"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for {
		if _, err = os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			fmt.Fprintln(os.Stderr, "repository root not found")
			os.Exit(1)
		}
		root = parent
	}
	if err = assets.WriteDeveloperViews(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
