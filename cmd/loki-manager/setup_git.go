package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
	signing "loki/modules/git/signing/public"
)

func setupGitSigning(ctx context.Context, host string, input, terminal *os.File, invoke func([]string, io.Writer) error) (bool, error) {
	var status bytes.Buffer
	if err := invoke([]string{"--json", "integrations", "status", "git"}, &status); err != nil {
		return false, err
	}
	var current struct {
		Identity signing.PublicIdentity `json:"identity"`
	}
	if err := json.Unmarshal(status.Bytes(), &current); err != nil {
		return false, fmt.Errorf("cannot read the current Git signing identity: %w", err)
	}
	name, email := current.Identity.Name, current.Identity.Email
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Git signing name").Description("Host: "+host+"\nName recorded on your commits.").Value(&name).
			Validate(func(value string) error {
				return signing.ValidateIdentity(strings.TrimSpace(value), "fixture@example.com")
			}),
		huh.NewInput().Title("Git signing email").Description("Email recorded on your commits.").Value(&email).
			Validate(func(value string) error { return signing.ValidateIdentity("Git author", strings.TrimSpace(value)) }),
	))
	aborted, err := runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted {
		return false, err
	}
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	confirmed := false
	form = huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Configure commit signing?").
		Description("Host: " + host + "\nIdentity: " + name + " <" + email + ">\nReuse the existing key or generate one. Owned services will restart.").
		Affirmative("Configure signing").Negative("Back").Value(&confirmed)))
	aborted, err = runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted || !confirmed {
		return false, err
	}
	setupWorking(terminal, "Configuring Git signing", host)
	var output bytes.Buffer
	if err := invoke([]string{"integrations", "setup", "git", "--identity-name", name, "--identity-email", email}, &output); err != nil {
		return false, err
	}
	return true, showSetupResult(ctx, "Git signing — configured", output.String(), "", input, terminal)
}
