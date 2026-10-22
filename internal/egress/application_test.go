package egress

import (
	"reflect"
	"testing"
)

func TestApplicationRestrictionIntersectsBothGrants(t *testing.T) {
	p := Policy{Version: 1, Profiles: map[string]Profile{
		"dependency-install": {AllowedHosts: []string{"packages.example"}, AllowedPorts: []int{443}},
		"github-api":         {AllowedHosts: []string{"api.github.com", "github.com"}, AllowedPorts: []int{443}},
		"application":        {AllowedHosts: []string{"api.example", "other.example"}, AllowedPorts: []int{443}},
	}}
	selected, err := p.RestrictApplication([]string{"api.example", "outside.example"})
	if err != nil {
		t.Fatal(err)
	}
	if !selected.Allows("application", "api.example", 443) || selected.Allows("application", "other.example", 443) || selected.Allows("application", "outside.example", 443) || selected.Allows("application", "packages.example", 443) {
		t.Fatal("application authority escaped intersection")
	}
	for _, name := range []string{"dependency-install", "github-api"} {
		if !reflect.DeepEqual(p.Profiles[name], selected.Profiles[name]) {
			t.Fatal("generic profile changed")
		}
	}
	if !p.Allows("application", "other.example", 443) {
		t.Fatal("source policy mutated")
	}
	if _, err = p.RestrictApplication([]string{"outside.example"}); err == nil {
		t.Fatal("empty intersection authorized")
	}
	delete(p.Profiles, "application")
	if _, err = p.RestrictApplication([]string{"packages.example"}); err == nil {
		t.Fatal("dependency-install authority borrowed")
	}
}
