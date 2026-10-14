package main

import (
	"slices"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

func TestSavedHostChoicesKeepIdentityAndExcludePreparation(t *testing.T) {
	registry := management.HostRegistry{Default: "ubuntu", Hosts: []management.HostRecord{
		{ID: "native", Name: "local", Target: management.ExecutionSelection{Host: tools.Host{Kind: "local"}}},
		{ID: "ubuntu", Name: "ubuntu", Target: management.ExecutionSelection{Host: tools.Host{Kind: "wsl", Distribution: "Ubuntu"}}},
		{ID: "system", Name: "system", Target: management.ExecutionSelection{Host: tools.Host{Kind: "local"}, System: true}},
	}}
	for _, full := range []bool{false, true} {
		choices := setupSavedHostChoices(registry, full)
		values := []string{}
		for _, choice := range choices {
			values = append(values, choice.Value)
			if choice.Value == "registered:ubuntu" && !strings.Contains(choice.Key, "Loki default") {
				t.Fatal("saved host does not identify Loki's default")
			}
		}
		want := []string{"registered:native", "registered:ubuntu", "registered:system", "prepare", ""}
		if full {
			want = want[1:]
		}
		if !slices.Equal(values, want) {
			t.Fatalf("full=%v: saved host identities and preparation separation: %v, want %v", full, values, want)
		}
	}
}

func TestPreparationRoutesMatchHostPlatform(t *testing.T) {
	for _, test := range []struct {
		platform string
		want     []string
	}{
		{"windows", []string{"wsl", "managed-wsl", "ssh", ""}},
		{"linux", []string{"system", "ssh", ""}},
		{"darwin", []string{"ssh", ""}},
	} {
		t.Run(test.platform, func(t *testing.T) {
			values := []string{}
			for _, choice := range setupHostPreparationChoices(test.platform) {
				values = append(values, choice.Value)
			}
			if !slices.Equal(values, test.want) {
				t.Fatalf("preparation routes: %v, want %v", values, test.want)
			}
		})
	}
}
