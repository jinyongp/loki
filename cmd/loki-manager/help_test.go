package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpForEveryPublicCommandWithoutStateOrHostAccess(t *testing.T) {
	for _, entry := range commandHelpEntries {
		path := strings.Fields(entry.path)
		variants := [][]string{append(append([]string{}, path...), "--help"), append([]string{"help"}, path...)}
		if entry.group {
			variants = append(variants, path)
		}
		for _, args := range variants {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "missing-state")
				args = append([]string{"--root", root, "--host", "ssh", "--address", "unreachable.invalid"}, args...)
				var output, errors bytes.Buffer
				if err := run(t.Context(), args, &output, &errors); err != nil {
					t.Fatal(err)
				}
				if errors.Len() != 0 || !strings.Contains(output.String(), entry.description) || !strings.Contains(output.String(), "Usage:") {
					t.Fatalf("help not readable/successful: stdout=%q stderr=%q", output.String(), errors.String())
				}
				for _, option := range entry.options {
					if !strings.Contains(output.String(), option) {
						t.Fatalf("missing documented option %q", option)
					}
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("help touched management state: %v", err)
				}
			})
		}
	}
}

func TestRootHelpAliasesAndPositionalLeafArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"help"}, {"tools", "serve", "browser", "--help"}, {"tools", "connect", "codex", "-h"}, {"tools", "install", "browser", "--help"}} {
		var output, diagnostics bytes.Buffer
		if err := run(t.Context(), args, &output, &diagnostics); err != nil || diagnostics.Len() != 0 || !strings.Contains(output.String(), "Examples:") {
			t.Fatalf("help alias failed: args=%q err=%v stdout=%q stderr=%q", args, err, output.String(), diagnostics.String())
		}
	}
}

func TestHelpFlagsDoNotConsumeValuesOrLiteralArguments(t *testing.T) {
	for _, args := range [][]string{
		{"tools", "install", "--catalog", "--help", "browser"},
		{"tools", "install", "-catalog", "-h", "browser"},
		{"integrations", "setup", "git", "--identity-name", "--help"},
		{"tools", "remove", "--", "--help"},
	} {
		var output bytes.Buffer
		if handled, err := contextualHelp(args, &output); handled || err != nil || output.Len() != 0 {
			t.Fatalf("flag value/literal interpreted as help: %q", args)
		}
	}
}

func TestUnknownHelpAndCommandsHaveActionableErrors(t *testing.T) {
	for _, args := range [][]string{{"unknown"}, {"help", "unknown"}, {"tools", "unknown", "--help"}, {"integrations", "refresh", "git", "--help"}} {
		var output, diagnostics bytes.Buffer
		err := run(t.Context(), args, &output, &diagnostics)
		if err == nil || !strings.Contains(err.Error(), "loki --help") || output.Len() != 0 {
			t.Fatalf("invalid command lacks useful error: %q err=%v output=%q", args, err, output.String())
		}
	}
}

func TestMissingRequiredArgumentsPointToCommandHelp(t *testing.T) {
	for _, command := range []string{"configure", "install", "update", "enable", "disable", "remove"} {
		var output, diagnostics bytes.Buffer
		err := run(t.Context(), []string{"--root", filepath.Join(t.TempDir(), "missing"), "tools", command}, &output, &diagnostics)
		if err == nil || !strings.Contains(err.Error(), "loki tools "+command+" --help") {
			t.Fatalf("missing arguments lack contextual guidance: %s: %v", command, err)
		}
	}
}
