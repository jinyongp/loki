package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"

	"loki/internal/management"
	"loki/internal/tools"
	signing "loki/modules/git/signing/public"
)

func runGitIntegration(ctx context.Context, store management.Store, args []string, input io.Reader, output, diagnostics io.Writer) error {
	action := args[0]
	f := flag.NewFlagSet("integrations "+action+" git", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	name := f.String("identity-name", "", "Git signing user.name")
	email := f.String("identity-email", "", "Git signing user.email")
	keyFile := f.String("key-file", "", "owner-only unencrypted Ed25519 SSH key to import; omit to retain or generate")
	keyStdin := f.Bool("key-stdin", false, "read the private SSH key from stdin")
	if err := f.Parse(toolArguments(args[1:])); err != nil {
		return err
	}
	if f.NArg() != 1 || f.Arg(0) != "git" || !slices.Contains([]string{"setup", "status", "doctor"}, action) {
		return fmt.Errorf("choose setup, status or doctor for git; see 'loki integrations --help'")
	}
	if *keyFile != "" && *keyStdin {
		return fmt.Errorf("use --key-file or --key-stdin")
	}
	if action != "setup" && (*name != "" || *email != "" || *keyFile != "" || *keyStdin) {
		return fmt.Errorf("Git identity/key options require integrations setup git")
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	if state.Config.Mode != tools.Full {
		return fmt.Errorf("Git signing uses the selected Linux full execution host")
	}
	var choice *tools.Selection
	for _, current := range state.Config.Tools {
		if current.ID == "git" && current.Enabled {
			copy := current
			choice = &copy
			break
		}
	}
	if choice == nil {
		return fmt.Errorf("install and enable git before configuring signing")
	}
	metadata, err := store.IntegrationMetadata("git")
	if err != nil {
		return err
	}
	var material signing.PublicMaterial
	if len(metadata) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(metadata))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&material) != nil || material.Validate() != nil {
			return fmt.Errorf("Git public signing metadata is invalid")
		}
	}
	if action == "status" {
		return json.NewEncoder(output).Encode(map[string]any{"integration": "git", "signing_enabled": slices.Contains(choice.Capabilities, "signing"), "configured": len(metadata) != 0, "readiness": "unknown", "identity": material.Identity})
	}
	full, err := management.NewFullBackend(store, diagnostics)
	if err != nil {
		return err
	}
	backend, ok := full.(management.IntegrationBackend)
	if !ok {
		return fmt.Errorf("selected backend cannot configure protected Git signing")
	}
	if action == "doctor" {
		probes, err := store.FullProbes(backend)
		if err != nil {
			return err
		}
		if probes["git"] == nil {
			return fmt.Errorf("selected Git services have not been observed")
		}
		fmt.Fprintln(diagnostics, "Checking confined Git execution and selected signing service...")
		if err := probes["git"](ctx, ""); err != nil {
			return err
		}
		if slices.Contains(choice.Capabilities, "signing") && len(metadata) == 0 {
			return fmt.Errorf("Git signing requires setup")
		}
		return json.NewEncoder(output).Encode(map[string]any{"integration": "git", "ready": true, "signing_enabled": slices.Contains(choice.Capabilities, "signing"), "identity": material.Identity})
	}
	if *name == "" {
		*name = material.Identity.Name
	}
	if *email == "" {
		*email = material.Identity.Email
	}
	if err := signing.ValidateIdentity(*name, *email); err != nil {
		return err
	}
	var key []byte
	defer func() { clear(key) }()
	if *keyFile != "" {
		key, err = integrationFile(*keyFile, true)
		if err != nil {
			return err
		}
	}
	if *keyStdin {
		key, err = io.ReadAll(io.LimitReader(input, (64<<10)+1))
		if err != nil || len(key) == 0 || len(key) > 64<<10 {
			return fmt.Errorf("private SSH key input is empty or exceeds its bound")
		}
	}
	if !slices.Contains(choice.Capabilities, "signing") {
		if err := store.SetEnabled("git", true, append(slices.Clone(choice.Capabilities), "signing")); err != nil {
			return err
		}
	}
	fmt.Fprintln(diagnostics, "Restarting owned services to configure Git signing...")
	if err := store.StopFull(ctx, backend); err != nil {
		return err
	}
	public, err := backend.SetupGitSigning(ctx, *name, *email, key)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(public))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&material) != nil || material.Validate() != nil {
		return fmt.Errorf("protected Git setup returned invalid public materials")
	}
	if err := store.SaveIntegrationMetadata("git", public); err != nil {
		return err
	}
	if _, err := store.ReconcileFull(ctx, backend); err != nil {
		return err
	}
	fmt.Fprintln(output, "Git signing ready.")
	fmt.Fprintln(output, "Public signing key:", material.Identity.PublicKey)
	fmt.Fprintln(output, "Add this public key as a signing key to the account that verifies your commits.")
	return nil
}
