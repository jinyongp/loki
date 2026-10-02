package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"loki/internal/host/lifecycle"
)

func runHostGitHubRefresh(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("host integration refresh", flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "operate on the system host installation")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil || flags.NArg() != 1 || flags.Arg(0) != "github" {
		fmt.Fprintln(stderr, "usage: loki host integration refresh --system github")
		return 2
	}
	options, err := resolveHostIntegrationOptions(hostIntegrationOptions{System: *system, StateRoot: strings.TrimSpace(*stateRoot)})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if options.System && os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "system GitHub token refresh requires root")
		return 1
	}
	store, err := lifecycle.OpenFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, "Loki is not installed for this scope")
		return 1
	}
	backend, err := newHostComposeBackend(store)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = backend.GitHubRefresh(ctx)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "GitHub installation token cache cleared. New commands use the approved permissions.")
	return 0
}
