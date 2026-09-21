package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"loki/internal/work/toolchains"
)

func main() {
	manifestPath := flag.String("manifest", "", "toolchain manifest")
	catalogPath := flag.String("catalog", "", "managed toolchain catalog")
	output := flag.String("output", "", "new output directory")
	flag.Parse()
	if *manifestPath == "" || *output == "" || flag.NArg() != 0 {
		os.Exit(2)
	}
	manifestRaw, err := os.ReadFile(*manifestPath)
	var catalogRaw []byte
	if err == nil && *catalogPath != "" {
		catalogRaw, err = os.ReadFile(*catalogPath)
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err = toolchain.FetchBundleWithCatalog(
			ctx, &http.Client{Timeout: 10 * time.Minute}, manifestRaw, catalogRaw, *output,
		)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
