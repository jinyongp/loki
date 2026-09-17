package secret

import (
	"strings"
	"testing"
)

func TestManagedCredentialControllerUsesClosedIdentifiers(t *testing.T) {
	c := managedAccessFixture(t, false)
	managed := c.ManagedCredentials()

	configured, err := managed.Configured(t.Context(), ManagedGitHubAppPrivateKey)
	if err != nil || configured {
		t.Fatalf("unconfigured managed credential = %v, %v", configured, err)
	}
	if _, err = managed.Get(t.Context(), ManagedGitHubAppPrivateKey); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured managed read = %v", err)
	}

	first := "synthetic-platform-first"
	result, err := managed.Set(t.Context(), ManagedGitHubAppPrivateKey, first)
	if err != nil || result["rotated"] != false {
		t.Fatalf("initial managed set = %#v, %v", result, err)
	}
	configured, err = managed.Configured(t.Context(), ManagedGitHubAppPrivateKey)
	if err != nil || !configured {
		t.Fatalf("configured managed credential = %v, %v", configured, err)
	}
	stored, err := managed.Get(t.Context(), ManagedGitHubAppPrivateKey)
	if err != nil || stored != first {
		t.Fatalf("managed credential = %q, %v", stored, err)
	}

	second := "synthetic-platform-second"
	result, err = managed.Set(t.Context(), ManagedGitHubAppPrivateKey, second)
	if err != nil || result["rotated"] != true {
		t.Fatalf("rotated managed set = %#v, %v", result, err)
	}
	stored, err = managed.Get(t.Context(), ManagedGitHubAppPrivateKey)
	if err != nil || stored != second {
		t.Fatalf("rotated managed credential = %q, %v", stored, err)
	}

	invalid := ManagedCredential(255)
	if _, err = managed.Get(t.Context(), invalid); err == nil || !strings.Contains(err.Error(), "unknown managed platform") {
		t.Fatalf("unknown managed read = %v", err)
	}
	if _, err = managed.Set(t.Context(), invalid, "synthetic"); err == nil || !strings.Contains(err.Error(), "unknown managed platform") {
		t.Fatalf("unknown managed set = %v", err)
	}
	if _, err = managed.Configured(t.Context(), invalid); err == nil || !strings.Contains(err.Error(), "unknown managed platform") {
		t.Fatalf("unknown managed status = %v", err)
	}
}
