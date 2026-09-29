package main

import (
	"strings"
	"testing"
)

func TestConnectionSetupCompatibilityAcceptsMatchingReleases(t *testing.T) {
	if err := connectionSetupCompatibilityError("v0.1.29", "0.1.29", false); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionSetupCompatibilityExplainsOlderAppliance(t *testing.T) {
	err := connectionSetupCompatibilityError("v0.1.29", "0.1.28", false)
	if err == nil {
		t.Fatal("expected mismatch")
	}
	message := err.Error()
	for _, required := range []string{
		"Loki appliance v0.1.28 is older than Windows frontend v0.1.29",
		"loki update prepare",
		"loki update apply",
		"loki connection setup openai",
	} {
		if !strings.Contains(message, required) {
			t.Fatalf("message lacks %q: %s", required, message)
		}
	}
}

func TestConnectionSetupCompatibilityUsesPreparedUpdateShortcut(t *testing.T) {
	err := connectionSetupCompatibilityError("v0.1.29", "v0.1.28", true)
	if err == nil {
		t.Fatal("expected mismatch")
	}
	message := err.Error()
	if !strings.Contains(message, "already prepared") || !strings.Contains(message, "loki update apply") {
		t.Fatalf("message=%s", message)
	}
	if strings.Contains(message, "loki update prepare") {
		t.Fatalf("prepared update unnecessarily asks to prepare again: %s", message)
	}
}

func TestConnectionSetupCompatibilityExplainsOlderFrontend(t *testing.T) {
	err := connectionSetupCompatibilityError("v0.1.28", "v0.1.29", false)
	if err == nil || !strings.Contains(err.Error(), "Run 'loki update'") {
		t.Fatalf("err=%v", err)
	}
}
