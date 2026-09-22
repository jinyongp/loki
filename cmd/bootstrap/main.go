package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	info             bool
	installArgs      []string
}

type bootstrapInfo struct {
	MetadataURL       string `json:"metadata_url"`
	TrustedRootSHA256 string `json:"trusted_root_sha256"`
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
	if options.info {
		sum := sha256.Sum256(root)
		if err = json.NewEncoder(stdout).Encode(bootstrapInfo{
			MetadataURL: metadataURL, TrustedRootSHA256: hex.EncodeToString(sum[:]),
		}); err != nil {
			fmt.Fprintln(stderr, "encode bootstrap info:", err)
			return 1
		}
		return 0
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
		case arg == "--bootstrap-info":
			options.info = true
		case arg == "--bootstrap-release-manifest" || strings.HasPrefix(arg, "--bootstrap-release-manifest="):
			return bootstrapOptions{}, fmt.Errorf("--bootstrap-release-manifest is reserved for the authenticated bootstrap handoff")
		default:
			if arg == "--system" {
				options.system = true
			}
			options.installArgs = append(options.installArgs, arg)
		}
	}
	if options.info && (options.requestedRelease != "" || options.stateRoot != "" || options.system || len(options.installArgs) != 0) {
		return bootstrapOptions{}, fmt.Errorf("--bootstrap-info cannot be combined with installation options")
	}
	return options, nil
}
