package main

import (
	"flag"
	"io"
	"os"
	"regexp"
)

func main() {
	flags := flag.NewFlagSet("loki-keepalive", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	distribution := flags.String("distribution", "", "WSL distribution")
	if flags.Parse(os.Args[1:]) != nil || flags.NArg() != 0 ||
		!regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(*distribution) {
		os.Exit(2)
	}
	os.Exit(keepalive(*distribution))
}
