//go:build linux

package secretbroker

import (
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
	provider "loki/modules/secrets/devtools"
	"os"
	"strings"
	"testing"
)

func TestJobChannelRejectsAuthorityDriftBeforeProviderAccess(t *testing.T) {
	resource := controlpolicy.ApplicationResource{ID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", Alias: "fixture", Repository: "repository-id", Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Network: "none", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Execute: true}}}
	s, err := New(Options{Provider: &provider.Provider{}, LauncherUID: uint32(os.Getuid()) + 1, DeploymentID: "deployment-id", PolicySHA256: strings.Repeat("a", 64), RunnerID: resource.ID, InstanceID: resource.ID, Resources: []controlpolicy.ApplicationResource{resource}, BaseEnvironment: map[string]string{"PATH": "/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	open := privateio.JobOpen{Protocol: 1, JobID: strings.Repeat("b", 32), DeploymentID: s.options.DeploymentID, RequestID: resource.ID, RequestSHA256: strings.Repeat("c", 64), PolicySHA256: s.options.PolicySHA256, Principal: "agent", Repository: resource.Repository, Resource: resource.Alias, Environment: "common", Network: "none", InstanceRef: "oci-instance-sha256:" + strings.Repeat("d", 64), BaseEnvironment: []privateio.EnvironmentEntry{{Name: "PATH", Value: "/usr/bin:/bin"}}}
	if _, err := s.authorize(open); err != nil {
		t.Fatal("valid authority rejected")
	}
	for _, field := range []string{"policy", "deployment", "resource", "repository", "principal", "env", "network", "base", "duplicate", "destinations"} {
		changed := open
		switch field {
		case "policy":
			changed.PolicySHA256 = strings.Repeat("e", 64)
		case "deployment":
			changed.DeploymentID = "foreign"
		case "resource":
			changed.Resource = "foreign"
		case "repository":
			changed.Repository = "foreign"
		case "principal":
			changed.Principal = "foreign"
		case "env":
			changed.Environment = "production"
		case "network":
			changed.Network = "dependency-install"
		case "base":
			changed.BaseEnvironment = []privateio.EnvironmentEntry{{Name: "PATH", Value: "/workspace"}}
		case "duplicate":
			changed.BaseEnvironment = append(append([]privateio.EnvironmentEntry(nil), open.BaseEnvironment...), open.BaseEnvironment...)
		case "destinations":
			changed.Destinations = []string{"foreign.example:443"}
		}
		if _, err := s.authorize(changed); err == nil {
			t.Fatal("authority drift accepted", field)
		}
	}
	resource.Keys = map[string]string{"PATH": "secret"}
	options := s.options
	options.Resources = []controlpolicy.ApplicationResource{resource}
	if _, err := New(options); err == nil {
		t.Fatal("reserved execution key granted")
	}
}
