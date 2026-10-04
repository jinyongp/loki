package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"loki/internal/config"
	githubsetup "loki/internal/integrations/github/setup"
	"loki/internal/management"
	"loki/internal/tools"
)

type githubRelayRequest struct {
	Setup         *githubsetup.Request     `json:"setup,omitempty"`
	User          *githubsetup.UserRequest `json:"user,omitempty"`
	Configuration []byte                   `json:"configuration,omitempty"`
}

func remoteGitHubWizard(args []string) bool {
	if len(args) < 3 || args[0] != "integrations" || args[1] != "setup" || args[2] != "github" {
		return false
	}
	for _, arg := range args[3:] {
		name, value, explicit := strings.Cut(arg, "=")
		if name != "--personal-projects" && name != "--no-browser" {
			return false
		}
		if explicit {
			if _, err := strconv.ParseBool(value); err != nil {
				return false
			}
		}
	}
	return true
}

type githubRelayOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *githubRelayOutput) Write(data []byte) (int, error) {
	n := len(data)
	if n > tools.MaxManifestBytes-b.Len() {
		b.overflow = true
		return n, fmt.Errorf("GitHub relay response exceeded its bound")
	}
	_, err := b.Buffer.Write(data)
	return n, err
}

func runRemoteGitHubWizard(ctx context.Context, host tools.Host, command, root string, args []string, input io.Reader, output, diagnostics io.Writer) error {
	f := flag.NewFlagSet("integrations setup github", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	personal := f.Bool("personal-projects", false, "optional account-owned Projects")
	noBrowser := f.Bool("no-browser", false, "print the setup URL")
	if err := f.Parse(args[3:]); err != nil {
		return err
	}
	call := func(ctx context.Context, request githubRelayRequest, view any) error {
		ctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
		defer cancel()
		encoded, err := json.Marshal(request)
		if err != nil || len(encoded) > tools.MaxManifestBytes {
			return fmt.Errorf("invalid GitHub relay request")
		}
		defer clear(encoded)
		argv := []string{"_github-setup-relay"}
		if root != "" {
			argv = append([]string{"--root", root}, argv...)
		}
		relay, err := management.Relay(ctx, host, command, argv)
		if err != nil {
			return err
		}
		var response githubRelayOutput
		relay.Stdin, relay.Stdout, relay.Stderr = bytes.NewReader(encoded), &response, diagnostics
		if err := relay.Run(); err != nil {
			return fmt.Errorf("GitHub setup host request failed; see its reported cause: %w", err)
		}
		if response.overflow {
			return fmt.Errorf("invalid GitHub relay response")
		}
		decoder := json.NewDecoder(bytes.NewReader(response.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(view) != nil {
			return fmt.Errorf("invalid GitHub relay response")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return fmt.Errorf("GitHub relay returned multiple responses")
		}
		return nil
	}
	var configuration []byte
	setup := func(ctx context.Context, request githubsetup.Request) (githubsetup.View, error) {
		var view githubsetup.View
		body := githubRelayRequest{Setup: &request}
		if request.Action == "apply" {
			body.Configuration = configuration
		}
		if err := call(ctx, body, &view); err != nil {
			return view, err
		}
		if view.SchemaVersion != 1 {
			return view, fmt.Errorf("unsupported GitHub setup response")
		}
		if view.Phase == "configured" {
			configuration = append(configuration[:0], view.Configuration...)
		}
		return view, nil
	}
	users := func(ctx context.Context, request githubsetup.UserRequest) (githubsetup.UserView, error) {
		var view githubsetup.UserView
		if err := call(ctx, githubRelayRequest{User: &request}, &view); err != nil {
			return view, err
		}
		return view, view.Validate(request)
	}
	if err := githubsetup.Run(ctx, setup, githubsetup.Options{PersonalProjects: *personal, NoBrowser: *noBrowser, Input: input, UserTransport: users}, diagnostics); err != nil {
		return err
	}
	return githubSetupResult(output)
}

// The one-request relay accepts public setup DTOs and one-time codes through
// stdin. Provider private keys and durable authorization stay on this host.
func runGitHubSetupRelay(ctx context.Context, store management.Store, input io.Reader, output, diagnostics io.Writer) error {
	data, err := io.ReadAll(io.LimitReader(input, tools.MaxManifestBytes+1))
	defer clear(data)
	if err != nil || len(data) > tools.MaxManifestBytes {
		return fmt.Errorf("invalid bounded GitHub relay request")
	}
	var request githubRelayRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || (request.Setup == nil) == (request.User == nil) {
		return fmt.Errorf("GitHub relay requires exactly one request")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("GitHub relay requires one document")
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	var choice *tools.Selection
	for _, current := range state.Config.Tools {
		if current.ID == "github" && current.Enabled {
			copy := current
			choice = &copy
			break
		}
	}
	if state.Config.Mode != tools.Full || choice == nil {
		return fmt.Errorf("install and enable github on the Linux full host before setup")
	}
	full, err := management.NewFullBackend(store, diagnostics)
	if err != nil {
		return err
	}
	backend, ok := full.(management.IntegrationBackend)
	if !ok {
		return fmt.Errorf("host does not provide protected GitHub setup")
	}
	if request.User != nil {
		if len(request.Configuration) != 0 || !slices.Contains(choice.Capabilities, "personal-projects") || !slices.Contains([]string{"status", "begin", "poll", "logout"}, request.User.Action) {
			return fmt.Errorf("personal Projects relay is not enabled for this request")
		}
		view, err := githubUserTransport(backend)(ctx, *request.User)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(view)
	}
	setup := *request.Setup
	if !slices.Contains([]string{"begin", "exchange", "poll", "finish", "apply", "status"}, setup.Action) {
		return fmt.Errorf("unsupported GitHub setup relay action")
	}
	if setup.Action != "apply" && len(request.Configuration) != 0 {
		return fmt.Errorf("configuration is only accepted for setup apply")
	}
	if setup.Action == "begin" {
		if setup.PersonalProjects && !slices.Contains(choice.Capabilities, "personal-projects") {
			if err := store.SetEnabled("github", true, append(slices.Clone(choice.Capabilities), "personal-projects")); err != nil {
				return err
			}
		}
		fmt.Fprintln(diagnostics, "Starting the protected GitHub setup service...")
		report, err := store.ReconcileFull(ctx, backend)
		if err != nil {
			if report.Deployment == nil || report.Observation.Services["github"] == "" {
				return err
			}
			for _, service := range report.Deployment.Services {
				if report.Observation.Services[service] != "ready" {
					return err
				}
			}
		}
	}
	if setup.Action == "apply" {
		parsed, err := config.ParseGitHubFragment(request.Configuration)
		if err != nil || parsed.GitHubAppID == 0 || len(parsed.GitHubTargets) == 0 {
			return fmt.Errorf("invalid GitHub installation configuration")
		}
		// Bind frontend public configuration to the provider's encrypted setup
		// state before mutating the running deployment.
		raw, err := backend.Administration(ctx, map[string]any{"operation": "github_setup", "setup_request": githubsetup.Request{Action: "status"}})
		if err != nil {
			return err
		}
		var pending githubsetup.View
		if json.Unmarshal(raw, &pending) != nil || pending.Phase != "configured" || !bytes.Equal(pending.Configuration, request.Configuration) {
			return fmt.Errorf("installation configuration differs from protected setup state")
		}
		fmt.Fprintln(diagnostics, "Applying installation access to the owned services...")
		if err := store.StopFull(ctx, backend); err != nil {
			return err
		}
		if err := store.SaveProviderConfiguration("github", request.Configuration); err != nil {
			return err
		}
		if _, err := store.ReconcileFull(ctx, backend); err != nil {
			return err
		}
	}
	raw, err := backend.Administration(ctx, map[string]any{"operation": "github_setup", "setup_request": setup})
	if err != nil {
		return err
	}
	var view githubsetup.View
	if json.Unmarshal(raw, &view) != nil || view.SchemaVersion != 1 {
		return fmt.Errorf("invalid protected GitHub setup response")
	}
	return json.NewEncoder(output).Encode(view)
}
