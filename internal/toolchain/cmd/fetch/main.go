package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"loki/internal/toolchain"
)

func main() {
	manifestPath := flag.String("manifest", "", "toolchain manifest")
	output := flag.String("output", "", "new output directory")
	flag.Parse()
	if *manifestPath == "" || *output == "" || flag.NArg() != 0 {
		os.Exit(2)
	}
	raw, err := os.ReadFile(*manifestPath)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		err = toolchain.FetchBundle(ctx, &http.Client{Timeout: 10 * time.Minute}, raw, *output)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
