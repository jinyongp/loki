package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"loki/internal/host/bootstrap"
)

var (
	releaseTag            string
	releaseManifestBase64 string
)

type bootstrapOptions struct {
	stateRoot   string
	system      bool
	info        bool
	installArgs []string
}

type bootstrapInfo = bootstrap.ReleaseBindingInfo

func main() {
	input := io.Reader(os.Stdin)
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		defer tty.Close()
		input = tty
	}
	os.Exit(runBootstrap(os.Args[1:], input, os.Stdout, os.Stderr, releaseTag, releaseManifestBase64))
}

func runBootstrap(args []string, stdin io.Reader, stdout, stderr io.Writer, tag, manifestBase64 string) int {
	options, err := parseBootstrapArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	manifestRaw, releaseInfo, err := bootstrap.DecodeEmbeddedRelease(tag, manifestBase64)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if options.info {
		if err = json.NewEncoder(stdout).Encode(releaseInfo); err != nil {
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
		StateRoot:       stateRoot,
		ReleaseTag:      tag,
		ReleaseManifest: manifestRaw,
		InstallArgs:     options.installArgs,
		Runner:          runner,
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
		case arg == "--bootstrap-release" || strings.HasPrefix(arg, "--bootstrap-release="):
			return bootstrapOptions{}, fmt.Errorf("--bootstrap-release is not supported; the bootstrap is bound to one release")
		case arg == "--bootstrap-release-manifest" || strings.HasPrefix(arg, "--bootstrap-release-manifest="):
			return bootstrapOptions{}, fmt.Errorf("--bootstrap-release-manifest is reserved for the verified bootstrap handoff")
		default:
			if arg == "--system" {
				options.system = true
			}
			options.installArgs = append(options.installArgs, arg)
		}
	}
	if options.info && (options.stateRoot != "" || options.system || len(options.installArgs) != 0) {
		return bootstrapOptions{}, fmt.Errorf("--bootstrap-info cannot be combined with installation options")
	}
	return options, nil
}
