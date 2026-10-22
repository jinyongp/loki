//go:build linux

package main

import (
	"loki/internal/app/jobbootstrap"
	"os"
)

func main() {
	if jobbootstrap.Run(os.Args[1:]) != nil {
		// No input, exec error, path or environment is printed on this raw stream.
		os.Exit(126)
	}
}
