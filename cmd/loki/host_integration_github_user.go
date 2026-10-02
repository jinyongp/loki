package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"loki/internal/host/githubsetup"
	"loki/internal/host/lifecycle"
)

func runHostGitHubUser(action string, args []string, stdout, stderr io.Writer) int {
	if action != "login" && action != "logout" {
		fmt.Fprintln(stderr, "Use loki host integration status --system github to inspect GitHub authorization.")
		return 2
	}
	flags := flag.NewFlagSet("host integration "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "operate on the system host installation")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	relay := flags.Bool("browser-request", false, "read a device login relay request from stdin")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil || flags.NArg() != 1 || flags.Arg(0) != "github" ||
		*relay && action != "login" || !*relay && action == "login" {
		if action == "login" {
			fmt.Fprintln(stderr, "Use loki host integration setup --system --personal-projects github to authorize personal Projects.")
		} else {
			fmt.Fprintf(stderr, "usage: loki host integration %s --system github\n", action)
		}
		return 2
	}
	options, err := resolveHostIntegrationOptions(hostIntegrationOptions{System: *system, StateRoot: strings.TrimSpace(*stateRoot)})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if options.System && os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "system GitHub user authorization requires root")
		return 1
	}
	store, err := lifecycle.OpenFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, "Loki is not installed for this scope")
		return 1
	}
	backend, err := newHostComposeBackend(store)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	transport := backend.GitHubUserAuthorization
	if *relay {
		var request githubsetup.UserRequest
		request, err = readGitHubUserRequest(hostIntegrationStdin)
		if err == nil {
			view, callErr := transport(ctx, request)
			err = callErr
			if err == nil {
				err = json.NewEncoder(stdout).Encode(view)
			}
		}
	} else {
		view, callErr := transport(ctx, githubsetup.UserRequest{Action: "logout"})
		err = callErr
		if err == nil {
			err = json.NewEncoder(stdout).Encode(view)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func readGitHubUserRequest(reader io.Reader) (githubsetup.UserRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, 4097))
	defer clear(raw)
	if err != nil || len(raw) == 0 || len(raw) > 4096 {
		return githubsetup.UserRequest{}, errors.New("invalid GitHub user authorization request")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request githubsetup.UserRequest
	var trailing any
	if decoder.Decode(&request) != nil || !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return request, errors.New("invalid GitHub user authorization request")
	}
	return request, nil
}
