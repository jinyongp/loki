package windows

import (
	"strings"
	"testing"

	"loki/internal/buildinfo"
)

func TestCurrentReleaseBindingRequiresCompleteIdentity(t *testing.T) {
	oldVersion, oldCommit, oldDate := buildinfo.Version, buildinfo.Commit, buildinfo.Date
	oldWSLSHA, oldWSLLen := WSLApplianceSHA256, WSLApplianceLength
	oldCatalogSHA, oldCatalogLen := HelperCatalogSHA256, HelperCatalogLength
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.Commit, buildinfo.Date = oldVersion, oldCommit, oldDate
		WSLApplianceSHA256, WSLApplianceLength = oldWSLSHA, oldWSLLen
		HelperCatalogSHA256, HelperCatalogLength = oldCatalogSHA, oldCatalogLen
	})

	buildinfo.Version = "1.2.3"
	buildinfo.Commit = strings.Repeat("a", 40)
	buildinfo.Date = "2026-09-27T00:00:00Z"
	WSLApplianceSHA256 = strings.Repeat("b", 64)
	WSLApplianceLength = "10"
	HelperCatalogSHA256 = strings.Repeat("c", 64)
	HelperCatalogLength = "20"

	binding, err := CurrentReleaseBinding()
	if err != nil {
		t.Fatal(err)
	}
	if binding.ReleaseTag != "v1.2.3" || binding.WSLAppliance.Length != 10 || binding.HelperCatalog.Length != 20 {
		t.Fatalf("unexpected binding %#v", binding)
	}

	HelperCatalogLength = "0"
	if _, err = CurrentReleaseBinding(); err == nil {
		t.Fatal("expected incomplete binding failure")
	}
}
