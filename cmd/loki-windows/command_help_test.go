package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestSubcommandHelpShowsOnlySelectedCommand(t *testing.T) {
	for _, test := range []struct {
		group   string
		path    []string
		print   func(*bytes.Buffer)
		want    string
		omitted []string
	}{
		{"integration", []string{"status"}, func(out *bytes.Buffer) { printIntegrationUsage(out, "status") }, "[--json] NAME", []string{"integration list", "integration doctor", "integration setup", "--interrupt-active-jobs"}},
		{"integration", []string{"enable"}, func(out *bytes.Buffer) { printIntegrationUsage(out, "enable") }, "[--interrupt-active-jobs] NAME", []string{"integration list", "integration remove", "--json"}},
		{"integration", []string{"setup", "signing"}, func(out *bytes.Buffer) { printIntegrationUsage(out, "setup", "signing") }, "--identity-name", []string{"integration rotate", "integration setup github", "--app-id"}},
		{"update", []string{"status"}, func(out *bytes.Buffer) { printUpdateHelp(out, "status") }, "[--json]", []string{"update prepare", "update apply", "--approve", "--interrupt-active-jobs", "Windows frontend"}},
		{"update", []string{"apply"}, func(out *bytes.Buffer) { printUpdateHelp(out, "apply") }, "--approve", []string{"update status", "update prepare", "Windows frontend"}},
		{"connection", []string{"show"}, func(out *bytes.Buffer) { printConnectionHelp(out, "show") }, "[--json] NAME", []string{"connection setup", "connection start", "--tunnel-id"}},
		{"connection", []string{"setup"}, func(out *bytes.Buffer) { printConnectionHelp(out, "setup") }, "--runtime-key-credential", []string{"connection show", "connection start", "--json"}},
	} {
		t.Run(test.group+"/"+strings.Join(test.path, "/"), func(t *testing.T) {
			var out bytes.Buffer
			test.print(&out)
			got := out.String()
			if !strings.Contains(got, "usage: loki "+test.group+" "+strings.Join(test.path, " ")) || !strings.Contains(got, test.want) {
				t.Fatalf("selected command help missing: %s", got)
			}
			for _, other := range test.omitted {
				if strings.Contains(got, other) {
					t.Errorf("selected command help includes %q: %s", other, got)
				}
			}
		})
	}
}

func TestIntegrationActionArguments(t *testing.T) {
	for _, test := range []struct {
		action, name, failure string
		args                  []string
	}{
		{action: "status", failure: "requires one NAME"},
		{action: "doctor", failure: "requires one NAME"},
		{action: "enable", failure: "requires one NAME"},
		{action: "status", args: []string{"browser", "github"}, failure: "requires one NAME"},
		{action: "list", args: []string{"browser"}, failure: "does not accept a NAME"},
		{action: "status", args: []string{"unknown"}, failure: "integration name must be"},
		{action: "status", args: []string{"--interrupt-active-jobs", "browser"}, failure: "valid only for integration mutations"},
		{action: "enable", args: []string{"--json", "browser"}, failure: "--json is valid only"},
		{action: "status", args: []string{"--unknown"}, failure: "flag provided but not defined"},
		{action: "status", args: []string{"--json", "browser"}, name: "browser"},
		{action: "enable", args: []string{"--interrupt-active-jobs", "signing"}, name: "signing"},
		{action: "list"},
	} {
		t.Run(test.action+"/"+strings.Join(test.args, " "), func(t *testing.T) {
			options, err := parseIntegrationAction(test.action, test.args, "loki-mcp")
			if test.failure != "" {
				if err == nil || !strings.Contains(err.Error(), test.failure) {
					t.Fatalf("err=%v, want %q", err, test.failure)
				}
				return
			}
			if err != nil || options.Name != test.name || options.Distribution != "loki-mcp" {
				t.Fatalf("options=%#v err=%v", options, err)
			}
		})
	}
	if _, err := parseIntegrationAction("status", []string{"--distribution", "test", "--help"}, "loki-mcp"); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help after flags: %v", err)
	}
}

func TestIntegrationHelpRequests(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"help"}, {"status", "--help"}, {"setup", "-h"}, {"rotate", "github", "--help"}} {
		if !integrationHelpRequested(args) {
			t.Errorf("help request not recognized: %v", args)
		}
	}
	for _, args := range [][]string{nil, {"status"}, {"status", "browser"}, {"unknown", "--help"}, {"setup", "browser", "--help"}} {
		if integrationHelpRequested(args) {
			t.Errorf("non-help request recognized: %v", args)
		}
	}
}
