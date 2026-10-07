package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"loki/internal/config"
	githubsetup "loki/internal/integrations/github/setup"
	"loki/internal/management"
	"loki/internal/tools"
	githubapp "loki/modules/github/keys"
)

type githubImport struct {
	Config     []byte `json:"config"`
	PrivateKey []byte `json:"private_key"`
}

func integrationFile(path string, private bool) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("integration input file cannot be opened")
	}
	if !info.Mode().IsRegular() || info.Size() > tools.MaxManifestBytes {
		return nil, fmt.Errorf("integration input must be a bounded regular file; private keys require owner-only permissions")
	}
	if private {
		if err := privateIntegrationFile(path, info); err != nil {
			return nil, err
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("integration input file cannot be opened")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, tools.MaxManifestBytes+1))
	if err != nil || len(data) > tools.MaxManifestBytes {
		return nil, fmt.Errorf("integration input exceeds its supported bound")
	}
	return data, nil
}

func readGitHubSetup(store management.Store, configPath, keyPath string, stdin bool, input io.Reader) (result githubImport, err error) {
	if stdin && (configPath != "" || keyPath != "") || (configPath == "") != (keyPath == "") {
		return result, fmt.Errorf("use --config-file and --private-key-file together, or --stdin")
	}
	if stdin {
		data, readErr := io.ReadAll(io.LimitReader(input, tools.MaxManifestBytes+1))
		defer clear(data)
		if readErr != nil || len(data) > tools.MaxManifestBytes {
			return result, fmt.Errorf("invalid protected setup input")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&result) != nil {
			clear(result.PrivateKey)
			return githubImport{}, fmt.Errorf("invalid protected setup input")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			clear(result.PrivateKey)
			return githubImport{}, fmt.Errorf("protected setup input contains trailing data")
		}
	} else if configPath != "" {
		result.Config, err = integrationFile(configPath, false)
		if err != nil {
			return result, err
		}
		result.PrivateKey, err = integrationFile(keyPath, true)
		if err != nil {
			return result, err
		}
	} else {
		result.Config, err = store.ProviderConfiguration("github")
		if err != nil {
			return result, err
		}
		if len(result.Config) == 0 {
			return result, fmt.Errorf("initial GitHub setup requires --config-file PATH --private-key-file PATH, or a private --stdin envelope")
		}
	}
	parsed, parseErr := config.ParseGitHubFragment(result.Config)
	if parseErr != nil || parsed.GitHubAppID == 0 || len(parsed.GitHubTargets) == 0 {
		clear(result.PrivateKey)
		return githubImport{}, fmt.Errorf("GitHub configuration requires an App ID and installation repository targets")
	}
	if result.PrivateKey != nil && githubapp.ValidatePrivateKey(string(result.PrivateKey)) != nil {
		clear(result.PrivateKey)
		return githubImport{}, fmt.Errorf("GitHub private key is invalid")
	}
	return result, nil
}

func runIntegrations(ctx context.Context, store management.Store, args []string, input io.Reader, output, diagnostics io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("choose an action and integration; see 'loki integrations --help'")
	}
	leaf := toolArguments(args[1:])
	if len(leaf) == 0 {
		return fmt.Errorf("choose an integration")
	}
	args = append([]string{args[0], leaf[len(leaf)-1]}, leaf[:len(leaf)-1]...)
	if args[1] == "git" {
		return runGitIntegration(ctx, store, args, input, output, diagnostics)
	}
	action := args[0]
	f := flag.NewFlagSet("integrations "+action, flag.ContinueOnError)
	f.SetOutput(diagnostics)
	configPath := f.String("config-file", "", "public GitHub App/installation TOML fragment on the execution host")
	keyPath := f.String("private-key-file", "", "private owner-only GitHub App PEM file on the execution host")
	stdin := f.Bool("stdin", false, "read config/private_key byte fields from a private JSON envelope")
	personal := f.Bool("personal-projects", false, "enable optional account-owned Projects and authorize with Device Flow")
	noBrowser := f.Bool("no-browser", false, "print the optional device authorization URL")
	if err := f.Parse(toolArguments(args[1:])); err != nil {
		return err
	}
	if f.NArg() != 1 || f.Arg(0) != "github" {
		return fmt.Errorf("this integration action requires github; see 'loki integrations %s --help'", action)
	}
	if !slices.Contains([]string{"setup", "status", "doctor", "refresh"}, action) {
		return fmt.Errorf("unknown integrations action %q; see 'loki integrations --help'", action)
	}
	if action != "setup" && (*configPath != "" || *keyPath != "" || *stdin || *personal || *noBrowser) {
		return fmt.Errorf("setup options require integrations setup github")
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	if state.Config.Mode != tools.Full {
		return fmt.Errorf("GitHub integration requires the selected Linux full execution host")
	}
	var choice *tools.Selection
	for _, current := range state.Config.Tools {
		if current.ID == "github" && current.Enabled {
			copy := current
			choice = &copy
			break
		}
	}
	if choice == nil {
		return fmt.Errorf("install and enable github before configuring its integration")
	}
	full, err := management.NewFullBackend(store, diagnostics)
	if err != nil {
		return err
	}
	backend, ok := full.(management.IntegrationBackend)
	if !ok {
		return fmt.Errorf("selected backend does not provide protected integration administration")
	}
	if action == "status" || action == "doctor" {
		return githubIntegrationStatus(ctx, backend, *choice, action == "doctor", output, diagnostics)
	}
	if action == "refresh" {
		fmt.Fprintln(diagnostics, "Refreshing GitHub installation token permissions...")
		response, err := backend.Administration(ctx, map[string]any{"operation": "github_refresh"})
		if err != nil {
			return err
		}
		return result(output, "GitHub installation permissions refreshed", json.RawMessage(response))
	}
	if *personal && !slices.Contains(choice.Capabilities, "personal-projects") {
		caps := append(slices.Clone(choice.Capabilities), "personal-projects")
		if err := store.SetEnabled("github", true, caps); err != nil {
			return err
		}
	}
	if *configPath == "" && *keyPath == "" && !*stdin {
		return runGitHubWizard(ctx, store, backend, *personal, *noBrowser, input, output, diagnostics)
	}
	imported, err := readGitHubSetup(store, *configPath, *keyPath, *stdin, input)
	if err != nil {
		return err
	}
	defer clear(imported.PrivateKey)
	if imported.PrivateKey != nil || *personal {
		fmt.Fprintln(diagnostics, "Restarting owned services to apply GitHub configuration...")
		if err := store.StopFull(ctx, backend); err != nil {
			return err
		}
		if imported.PrivateKey != nil {
			if err := store.SaveProviderConfiguration("github", imported.Config); err != nil {
				return err
			}
			fmt.Fprintln(diagnostics, "Saving the key in the protected GitHub provider store...")
			if err := backend.ImportGitHub(ctx, imported.Config, imported.PrivateKey); err != nil {
				return err
			}
		}
	}
	if _, err := store.ReconcileFull(ctx, backend); err != nil {
		return err
	}
	if *personal {
		transport := func(ctx context.Context, request githubsetup.UserRequest) (githubsetup.UserView, error) {
			body := map[string]any{"operation": "github_user_" + request.Action}
			if request.Account != "" {
				body["account"] = request.Account
			}
			if request.SessionID != "" {
				body["session_id"] = request.SessionID
			}
			response, err := backend.Administration(ctx, body)
			if err != nil {
				return githubsetup.UserView{}, githubsetup.UserLoginError(err.Error())
			}
			var view githubsetup.UserView
			if err := json.Unmarshal(response, &view); err != nil {
				return view, fmt.Errorf("invalid personal Projects authorization response")
			}
			return view, nil
		}
		if err := githubsetup.RunUser(ctx, transport, githubsetup.Options{NoBrowser: *noBrowser}, diagnostics); err != nil {
			return err
		}
	}
	return githubSetupResult(output)
}

func githubIntegrationStatus(ctx context.Context, backend management.IntegrationBackend, choice tools.Selection, doctor bool, output, diagnostics io.Writer) error {
	fmt.Fprintln(diagnostics, "Checking GitHub installation and optional account authorization...")
	response, err := backend.Administration(ctx, map[string]any{"operation": "status"})
	if err != nil {
		return err
	}
	var runtime struct {
		GitHub struct {
			Configured          bool `json:"configured"`
			CredentialAvailable bool `json:"credential_available"`
			Targets             int  `json:"target_count"`
		} `json:"github"`
	}
	if err := json.Unmarshal(response, &runtime); err != nil {
		return fmt.Errorf("invalid GitHub status response")
	}
	ready := runtime.GitHub.Configured && runtime.GitHub.CredentialAvailable && runtime.GitHub.Targets > 0
	personalStatus := json.RawMessage(`{"status":"not_required"}`)
	if slices.Contains(choice.Capabilities, "personal-projects") {
		personalStatus, err = backend.Administration(ctx, map[string]any{"operation": "github_user_status"})
		if err != nil {
			return err
		}
		var user struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(personalStatus, &user); err != nil {
			return err
		}
		ready = ready && (user.Status == "ready" || user.Status == "not_required")
	}
	if err := result(output, "GitHub integration", map[string]any{"integration": "github", "authentication": "GitHub App installation tokens", "ready": ready, "github": runtime.GitHub, "personal_projects": personalStatus}); err != nil {
		return err
	}
	if doctor && !ready {
		return fmt.Errorf("GitHub needs configuration or authorization; run loki integrations setup github")
	}
	return nil
}

func githubSetupResult(output io.Writer) error {
	return success(output, "GitHub integration ready. Repository-linked Projects use installation tokens.", map[string]any{"integration": "github", "ready": true, "authentication": "GitHub App installation tokens"})
}
