package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	flags := flag.NewFlagSet("loki-keepalive", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	distribution := flags.String("distribution", "", "WSL distribution")
	frontend := flags.String("frontend", "", "native frontend for restoring managed connections")
	root := flags.String("root", "", "frontend management directory")
	if flags.Parse(os.Args[1:]) != nil || flags.NArg() != 0 ||
		!regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(*distribution) {
		os.Exit(2)
	}
	if *frontend != "" {
		if !filepath.IsAbs(*frontend) || !filepath.IsAbs(*root) || strings.ContainsAny(*frontend+*root, "\x00\r\n") {
			os.Exit(2)
		}
		os.Exit(restoreConnections(*frontend, *root, *distribution))
	}
	if *root != "" {
		os.Exit(2)
	}
	os.Exit(keepalive(*distribution))
}
