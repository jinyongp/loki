package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"loki/internal/toolchain"
)

func runToolchain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: loki toolchain install|doctor")
		return 2
	}
	switch args[0] {
	case "install":
		flags := flag.NewFlagSet("toolchain install", flag.ContinueOnError)
		flags.SetOutput(stderr)
		bundle := flags.String("bundle", "", "verified toolchain bundle")
		root := flags.String("root", "/", "target root")
		skipApt := flags.Bool("skip-apt", false, "install artifacts without apt packages")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*bundle) || !filepath.IsAbs(*root) {
			return 2
		}
		raw, err := os.ReadFile(filepath.Join(*bundle, "manifest.json"))
		manifest, loadErr := toolchain.LoadManifest(raw)
		if err == nil {
			err = loadErr
		}
		if err == nil && !*skipApt {
			err = toolchain.InstallApt(context.Background(), manifest)
		}
		if err == nil {
			err = toolchain.InstallArtifacts(context.Background(), manifest, *bundle, *root)
		}
		if err == nil {
			err = toolchain.InstallMetadata(raw, manifest, *root)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "doctor":
		flags := flag.NewFlagSet("toolchain doctor", flag.ContinueOnError)
		flags.SetOutput(stderr)
		manifestPath := flags.String("manifest", "/usr/share/doc/loki/toolchain-manifest.json", "toolchain manifest")
		root := flags.String("root", "/", "target root")
		skipApt := flags.Bool("skip-apt", false, "omit dpkg checks")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*manifestPath) || !filepath.IsAbs(*root) {
			return 2
		}
		raw, err := os.ReadFile(*manifestPath)
		manifest, loadErr := toolchain.LoadManifest(raw)
		if err == nil {
			err = loadErr
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		report := toolchain.Doctor(context.Background(), manifest, *root, !*skipApt)
		_ = json.NewEncoder(stdout).Encode(report)
		if !report.OK {
			return 1
		}
		return 0
	default:
		fmt.Fprintln(stderr, "usage: loki toolchain install|doctor")
		return 2
	}
}
