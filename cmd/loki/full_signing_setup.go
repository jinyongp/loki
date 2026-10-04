package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"loki/internal/config"
	"loki/internal/tools"
	"loki/modules/git/signing"
)

func runFullSigningSetup(args []string, input io.Reader, output, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("full-signing-setup", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	keygen := flags.String("keygen", "", "Git module-owned OpenSSH key program")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || os.Geteuid() != 0 || !filepath.IsAbs(*keygen) {
		fmt.Fprintln(diagnostics, "signing setup requires its administrator-owned Git role")
		return 2
	}
	gate := config.ToolGate{Path: "/etc/loki/activation/state.json", Release: tools.Release, Mode: tools.Full, Snapshot: true}
	choice, err := gate.Selection("git")
	if err != nil || !slices.Contains(choice.Capabilities, "signing") {
		fmt.Fprintln(diagnostics, "Git signing capability is disabled")
		return 1
	}
	var request struct {
		Name       string `json:"name"`
		Email      string `json:"email"`
		PrivateKey []byte `json:"private_key"`
	}
	if err := fullInput(input, &request); err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	defer clear(request.PrivateKey)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	identity, err := signing.Setup(ctx, "/var/lib/loki/git/signing", *keygen, request.Name, request.Email, request.PrivateKey)
	if err != nil {
		fmt.Fprintln(diagnostics, "Git signing setup failed:", err)
		return 1
	}
	material, err := identity.Material()
	if err != nil {
		fmt.Fprintln(diagnostics, "Cannot prepare public Git signing configuration")
		return 1
	}
	if err := json.NewEncoder(output).Encode(material); err != nil {
		return 1
	}
	return 0
}
