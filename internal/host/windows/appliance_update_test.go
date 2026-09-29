package windows

import (
	"context"
	"testing"
)

type fakeApplianceReleaseFetcher map[string][]byte

func (fetcher fakeApplianceReleaseFetcher) Fetch(_ context.Context, assetURL string, _ int64) ([]byte, error) {
	return append([]byte(nil), fetcher[assetURL]...), nil
}

func TestApplianceReleaseClientResolvesIndependentHostPointer(t *testing.T) {
	const url = "https://example.test/install.sh"
	client := ApplianceReleaseClient{
		Fetcher:      fakeApplianceReleaseFetcher{url: []byte("release_tag='v0.1.29'\n")},
		InstallerURL: url,
	}
	pointer, err := client.Resolve(t.Context())
	if err != nil || pointer.ReleaseTag != "v0.1.29" {
		t.Fatalf("pointer=%#v err=%v", pointer, err)
	}
	if comparison, err := client.Compare("v0.1.28", pointer); err != nil || comparison >= 0 {
		t.Fatalf("comparison=%d err=%v", comparison, err)
	}
	if comparison, err := client.Compare("0.1.29", pointer); err != nil || comparison != 0 {
		t.Fatalf("current comparison=%d err=%v", comparison, err)
	}
}

func TestParseApplianceReleasePointerRejectsInvalidIdentity(t *testing.T) {
	for _, raw := range []string{
		"",
		"release_tag='latest'\n",
		"release_tag='v0.1.28'\nrelease_tag='v0.1.29'\n",
	} {
		if _, err := ParseApplianceReleasePointer([]byte(raw)); err == nil {
			t.Fatalf("invalid pointer accepted: %q", raw)
		}
	}
	if _, err := ParseApplianceReleasePointer([]byte("release_tag='v0.1.29'\n")); err != nil {
		t.Fatalf("valid pointer rejected: %v", err)
	}
}
