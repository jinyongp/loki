package main

import (
	"flag"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
)

// Validate tool acquisition arguments before creating a privileged host.
func validateToolUpdateRequest(args []string) error {
	// Reuse acquisition option validation with one synthetic tool. Update
	// accepts no positional names; any supplied name is rejected below.
	full, err := fullInstallationRequest(append(slices.Clone(args), "browser"))
	if err != nil {
		return err
	}
	if full {
		return fmt.Errorf("tools update accepts no tool names; see 'loki tools update --help'")
	}
	return nil
}

func fullInstallationRequest(args []string) (bool, error) {
	f := flag.NewFlagSet("tools install", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	catalog := f.String("catalog", "", "offline catalog")
	version := f.String("version", "", "release")
	archives := f.String("archives", "", "offline archives")
	if err := f.Parse(toolArguments(args)); err != nil {
		return false, err
	}
	if f.NArg() == 0 {
		return false, fmt.Errorf("select tools or run loki setup; see 'loki tools install --help'")
	}
	// This validation runs on the frontend before host preparation. A Windows
	// frontend may forward POSIX paths to WSL/SSH; the execution host performs
	// the final native path validation before acquiring an archive.
	if *archives != "" && !filepath.IsAbs(*archives) && !path.IsAbs(*archives) {
		return false, fmt.Errorf("--archives requires an absolute directory")
	}
	if *catalog != "" && *version != "" {
		return false, fmt.Errorf("use --catalog or --version, not both")
	}
	if *version != "" && !stableVersionPattern.MatchString(*version) {
		return false, fmt.Errorf("version must be a stable MAJOR.MINOR.PATCH release")
	}
	full := false
	seen := map[string]bool{}
	for _, name := range f.Args() {
		if !slices.Contains(publicToolNames, name) || seen[name] {
			return false, fmt.Errorf("unknown or duplicate tool %q", name)
		}
		seen[name] = true
		full = full || name != "browser"
	}
	return full, nil
}
