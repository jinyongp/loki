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
	"time"

	"loki/internal/config"
	"loki/internal/fault"
	githubsetup "loki/internal/integrations/github/setup"
	"loki/internal/rpc"
	"loki/internal/tools"
	githubapp "loki/modules/github"
)

func fullInput(input io.Reader, value any) error {
	data, err := io.ReadAll(io.LimitReader(input, tools.MaxManifestBytes+1))
	if err != nil {
		return errors.New("cannot read protected integration input")
	}
	defer clear(data)
	if len(data) > tools.MaxManifestBytes {
		return errors.New("protected integration input exceeds its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return errors.New("invalid protected integration input")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("protected integration input has trailing data")
	}
	return nil
}

func runFullProviderImport(args []string, input io.Reader, output, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("full-provider-import", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	state := flags.String("state", "", "provider-owned encrypted state directory")
	activation := flags.String("tools-config", "", "full activation snapshot")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *state != "/var/lib/loki/providers/github" || *activation != "/etc/loki/activation/state.json" || os.Geteuid() != 0 {
		fmt.Fprintln(diagnostics, "provider import requires its protected administrator role layout")
		return 2
	}
	var envelope struct {
		Config     []byte `json:"config"`
		PrivateKey []byte `json:"private_key"`
	}
	if err := fullInput(input, &envelope); err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	defer clear(envelope.PrivateKey)
	gate := config.ToolGate{Path: *activation, Release: "0.2.0", Mode: tools.Full, Snapshot: true}
	if _, err := gate.Selection("github"); err != nil {
		fmt.Fprintln(diagnostics, "GitHub tool is disabled or unavailable")
		return 1
	}
	configuration, err := config.ParseGitHubFragment(envelope.Config)
	if err != nil || configuration.GitHubAppID == 0 || len(configuration.GitHubTargets) == 0 || githubapp.ValidatePrivateKey(string(envelope.PrivateKey)) != nil {
		fmt.Fprintln(diagnostics, "GitHub setup requires valid App configuration, repository targets and a private key")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := (githubapp.Credentials{StateDirectory: *state}).Set(ctx, githubapp.AppPrivateKey, string(envelope.PrivateKey))
	if err != nil {
		fmt.Fprintln(diagnostics, "GitHub provider credential could not be saved")
		return 1
	}
	if err := (githubapp.Setup{Credentials: githubapp.Credentials{StateDirectory: *state}}).Reset(ctx); err != nil {
		fmt.Fprintln(diagnostics, "GitHub key was saved but pending registration could not be cleared; retry setup")
		return 1
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return 1
	}
	return 0
}

// The finite public status/device-flow operations use the existing runtime
// administration grants. No generic secret read or arbitrary RPC is exposed by
// this management relay, and requests enter through stdin rather than argv.
func runFullAdministration(args []string, input io.Reader, output, diagnostics io.Writer) int {
	if len(args) != 0 || os.Geteuid() != 0 {
		fmt.Fprintln(diagnostics, "full administration requires its protected root runtime role")
		return 2
	}
	var request struct {
		Operation string               `json:"operation"`
		Account   string               `json:"account,omitempty"`
		SessionID string               `json:"session_id,omitempty"`
		Request   *githubsetup.Request `json:"setup_request,omitempty"`
	}
	if err := fullInput(input, &request); err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	switch request.Operation {
	case "status", "github_refresh", "github_setup", "github_user_status", "github_user_begin", "github_user_poll", "github_user_logout":
	default:
		fmt.Fprintln(diagnostics, "unsupported management administration operation")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	uid := uint32(0)
	response, err := (rpc.Client{Socket: "/run/loki/runtime/control.sock", ExpectedUID: &uid, Limits: rpc.Limits{Timeout: 30 * time.Second}}).Call(ctx, request)
	if err != nil {
		fmt.Fprintln(diagnostics, "loki:", fault.Public(err))
		return 1
	}
	_, err = output.Write(append(response, '\n'))
	if err != nil {
		return 1
	}
	return 0
}
