package main

import "testing"

func TestNextVersion(t *testing.T) {
	tests := []struct {
		name     string
		tags     []string
		bump     string
		version  string
		tag      string
		previous string
	}{
		{name: "first-patch", bump: "patch", version: "0.1.0", tag: "v0.1.0"},
		{name: "first-minor", bump: "minor", version: "0.1.0", tag: "v0.1.0"},
		{name: "first-major", bump: "major", version: "1.0.0", tag: "v1.0.0"},
		{name: "patch", tags: []string{"v0.1.0", "v0.2.3", "junk"}, bump: "patch", version: "0.2.4", tag: "v0.2.4", previous: "v0.2.3"},
		{name: "minor", tags: []string{"v1.2.9"}, bump: "minor", version: "1.3.0", tag: "v1.3.0", previous: "v1.2.9"},
		{name: "major", tags: []string{"v1.9.9"}, bump: "major", version: "2.0.0", tag: "v2.0.0", previous: "v1.9.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nextVersion(tt.tags, tt.bump)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != tt.version || got.Tag != tt.tag || got.PreviousTag != tt.previous {
				t.Fatalf("next version = %#v", got)
			}
		})
	}
}

func TestNextVersionRejectsUnknownBump(t *testing.T) {
	if _, err := nextVersion(nil, "banana"); err == nil {
		t.Fatal("unknown bump accepted")
	}
}
