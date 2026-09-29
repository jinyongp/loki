package main

import (
	"reflect"
	"testing"
)

func TestParseBootstrapModeSeparatesFrontendUpdateFromInstall(t *testing.T) {
	mode, installArgs, err := parseBootstrapMode([]string{"install", "--distribution", "custom"})
	if err != nil || mode != bootstrapModeInstall ||
		!reflect.DeepEqual(installArgs, []string{"--distribution", "custom"}) {
		t.Fatalf("install mode=%v args=%#v err=%v", mode, installArgs, err)
	}

	mode, installArgs, err = parseBootstrapMode([]string{"update"})
	if err != nil || mode != bootstrapModeUpdate || len(installArgs) != 0 {
		t.Fatalf("update mode=%v args=%#v err=%v", mode, installArgs, err)
	}

	if _, _, err = parseBootstrapMode([]string{"update", "--distribution", "custom"}); err == nil {
		t.Fatal("frontend-only bootstrap update accepted install options")
	}
}
