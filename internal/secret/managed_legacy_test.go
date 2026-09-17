package secret

import "testing"

func TestManagedCredentialControllerReadsExistingReservedProfile(t *testing.T) {
	controller := managedAccessFixture(t, true)
	managed := controller.ManagedCredentials()
	configured, err := managed.Configured(t.Context(), ManagedGitHubAppPrivateKey)
	if err != nil || !configured {
		t.Fatalf("existing managed credential availability = %v, %v", configured, err)
	}
	value, err := managed.Get(t.Context(), ManagedGitHubAppPrivateKey)
	if err != nil || value != "synthetic-platform" {
		t.Fatalf("existing managed credential = %q, %v", value, err)
	}
}
