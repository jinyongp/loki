package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUpdateHelpRequested(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"help"},
		{"status", "--help"},
		{"prepare", "-h"},
		{"apply", "--help"},
	} {
		if !updateHelpRequested(args) {
			t.Fatalf("help request not recognized: %#v", args)
		}
	}
	for _, args := range [][]string{
		nil,
		{"status"},
		{"apply", "--approve"},
		{"unknown", "--help"},
	} {
		if updateHelpRequested(args) {
			t.Fatalf("non-help request recognized as help: %#v", args)
		}
	}
}

func TestPrintUpdateHelpExplainsWholeProductAndTwoPhaseLifecycle(t *testing.T) {
	var out bytes.Buffer
	printUpdateHelp(&out)
	got := out.String()
	for _, want := range []string{
		"loki update",
		"loki update prepare",
		"loki update apply",
		"updates the Windows frontend and converges the appliance release",
		"advanced two-phase appliance lifecycle",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("update help missing %q: %s", want, got)
		}
	}
}
