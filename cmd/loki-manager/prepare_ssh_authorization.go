package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/term"
	"io"
	"loki/internal/management"
	"os"
)

func prepareSSHAuthorization(ctx context.Context, selected management.ExecutionSelection, input io.Reader, diagnostics io.Writer) error {
	call := func(action string, material []byte, response *githubRelayOutput) error {
		args := []string{action}
		if selected.Root != "" {
			args = append([]string{"--root", selected.Root}, args...)
		}
		relay, err := management.RelaySelection(ctx, selected, args)
		if err != nil {
			return err
		}
		relay.Stdin, relay.Stdout, relay.Stderr = bytes.NewReader(material), response, diagnostics
		return relay.Run()
	}
	var response githubRelayOutput
	if err := call("_host-authorization", nil, &response); err != nil {
		return fmt.Errorf("checking SSH administrator authorization: %w", err)
	}
	var authority struct {
		Required bool `json:"administrator_required"`
	}
	if response.overflow || json.Unmarshal(response.Bytes(), &authority) != nil {
		return fmt.Errorf("invalid SSH host authorization response")
	}
	if !authority.Required {
		return nil
	}
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return fmt.Errorf("SSH host needs administrator authentication; rerun loki setup in an interactive terminal")
	}
	fmt.Fprint(diagnostics, "SSH host administrator password (input hidden; used once for host preparation): ")
	password, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(diagnostics)
	if err != nil {
		return err
	}
	defer clear(password)
	response.Reset()
	if err := call("_prepare-host-relay", password, &response); err != nil {
		return fmt.Errorf("SSH administrator preparation failed; retry loki setup: %w", err)
	}
	var prepared management.ExecutionSelection
	if response.overflow || json.Unmarshal(response.Bytes(), &prepared) != nil || prepared.Validate() != nil || !prepared.System {
		return fmt.Errorf("SSH host returned invalid system preparation metadata")
	}
	return nil
}
