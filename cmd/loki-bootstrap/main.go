package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"loki/internal/host/bootstrap"
)

var (
	releaseMetadataURL string
	trustedRootBase64  string
)

type bootstrapOptions struct {
	requestedRelease string
	stateRoot        string
	system           bool
	installArgs      []string
}

func main() {
	input := io.Reader(os.Stdin)
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		defer tty.Close()
		input = tty
	}
	os.Exit(runBootstrap(os.Args[1:], input, os.Stdout, os.Stderr, releaseMetadataURL, trustedRootBase64))
}

func runBootstrap(args []string, stdin io.Reader, stdout, stderr io.Writer, metadataURL, rootBase64 string) int {
	options, err := parseBootstrapArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	metadataURL = strings.TrimSpace(metadataURL)
	if metadataURL == "" {
		fmt.Fprintln(stderr, "bootstrap release metadata URL is not embedded")
		return 1
	}
	root, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(rootBase64))
	if err != nil || len(root) == 0 {
		fmt.Fprintln(stderr, "bootstrap trusted root is not embedded")
		return 1
	}
	stateRoot := options.stateRoot
	if stateRoot == "" {
		stateRoot, err = bootstrap.DefaultStateRoot(options.system)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	runner := bootstrap.ExecRunner{Stdin: stdin, Stdout: stdout, Stderr: stderr}
	if _, err = bootstrap.Run(context.Background(), bootstrap.Config{
		StateRoot:         stateRoot,
		RemoteMetadataURL: metadataURL,
		TrustedRoot:       root,
		RequestedRelease:  options.requestedRelease,
		InstallArgs:       options.installArgs,
		Runner:            runner,
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func parseBootstrapArgs(args []string) (bootstrapOptions, error) {
	var options bootstrapOptions
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--bootstrap-release":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return bootstrapOptions{}, fmt.Errorf("--bootstrap-release requires a value")
			}
			options.requestedRelease = strings.TrimSpace(args[index])
		case strings.HasPrefix(arg, "--bootstrap-release="):
			options.requestedRelease = strings.TrimSpace(strings.TrimPrefix(arg, "--bootstrap-release="))
			if options.requestedRelease == "" {
				return bootstrapOptions{}, fmt.Errorf("--bootstrap-release requires a value")
			}
		case arg == "--bootstrap-state-root":
			index++
			if index >= len(args) || strings.TrimSpace(args[index]) == "" {
				return bootstrapOptions{}, fmt.Errorf("--bootstrap-state-root requires a value")
			}
			options.stateRoot = strings.TrimSpace(args[index])
		case strings.HasPrefix(arg, "--bootstrap-state-root="):
			options.stateRoot = strings.TrimSpace(strings.TrimPrefix(arg, "--bootstrap-state-root="))
			if options.stateRoot == "" {
				return bootstrapOptions{}, fmt.Errorf("--bootstrap-state-root requires a value")
			}
		case arg == "--bootstrap-release-manifest" || strings.HasPrefix(arg, "--bootstrap-release-manifest="):
			return bootstrapOptions{}, fmt.Errorf("--bootstrap-release-manifest is reserved for the authenticated bootstrap handoff")
		default:
			if arg == "--system" {
				options.system = true
			}
			options.installArgs = append(options.installArgs, arg)
		}
	}
	return options, nil
}
