package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"loki/internal/host/githubsetup"
	"loki/internal/host/lifecycle"
)

func runHostGitHubBrowserSetup(options hostIntegrationOptions, store *lifecycle.FileStore, relay bool, browser githubsetup.Options, stdout, stderr io.Writer) int {
	ctx := context.Background()
	snapshot, preflightErr := store.Snapshot(ctx)
	if preflightErr != nil || snapshot.Installed == nil || snapshot.Installation == nil {
		fmt.Fprintln(stderr, "Loki host installation is unavailable; run host doctor before GitHub setup")
		return 1
	}
	handler := &hostGitHubSetup{Store: store}
	handler.Ready = func(ctx context.Context) (bool, error) {
		backend, err := newHostComposeBackend(store)
		if err != nil {
			return false, err
		}
		report, err := inspectHostIntegration(ctx, store, backend, "github")
		if err != nil {
			return false, err
		}
		if !report.Ready {
			return false, nil
		}
		candidate, err := loadManagedGitHubFromStore(ctx, store)
		if err != nil {
			return false, err
		}
		defer clear(candidate.KeyRaw)
		if err = validateManagedGitHubCandidate(ctx, candidate, nil); err != nil {
			return false, err
		}
		return true, nil
	}
	handler.Apply = func(ctx context.Context, candidate managedGitHubCandidate) error {
		if err := validateManagedGitHubCandidate(ctx, candidate, nil); err != nil {
			return err
		}
		current, err := store.ReadManagedIntegrations(ctx)
		if err != nil {
			return err
		}
		if current.GitHub.Configured {
			return errors.New("GitHub was configured during setup; inspect the current integration before continuing")
		}
		reporter, stop := startHostIntegrationProgress(ctx, stderr, "setup", "github")
		defer stop()
		return applyManagedGitHubCandidate(ctx, "setup", options, store, current, candidate, reporter)
	}
	var err error
	if relay {
		var request githubsetup.Request
		request, err = readGitHubBrowserRequest(hostIntegrationStdin)
		if err == nil {
			var view githubsetup.View
			view, err = handler.Handle(ctx, request)
			if err == nil {
				err = json.NewEncoder(stdout).Encode(view)
			}
		}
	} else {
		err = githubsetup.Run(ctx, handler.Handle, browser, stdout)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
func readGitHubBrowserRequest(reader io.Reader) (githubsetup.Request, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, 8193))
	defer clear(raw)
	if err != nil || len(raw) == 0 || len(raw) > 8192 {
		return githubsetup.Request{}, errors.New("GitHub browser request is empty or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request githubsetup.Request
	if err = decoder.Decode(&request); err != nil {
		return request, errors.New("invalid GitHub browser request")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return request, errors.New("GitHub browser request contains trailing content")
	}
	return request, nil
}
