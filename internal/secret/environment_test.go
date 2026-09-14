package secret

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestResolveEnvironmentKeepsValuesPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	controller := Controller{StateDirectory: dir}
	ctx := context.Background()
	if _, err := controller.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateProfile(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.SetSecret(ctx, "project", "TOKEN", "private-token-value", false); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.SetSecret(ctx, "project", "PASSWORD", "private-password-value", false); err != nil {
		t.Fatal(err)
	}
	plan, err := controller.ResolveEnvironment(ctx, "project", []string{"TOKEN", "PASSWORD"})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Names(); !slices.Equal(got, []string{"PASSWORD", "TOKEN"}) {
		t.Fatalf("names = %v", got)
	}
	for _, want := range []string{"PASSWORD=private-password-value", "TOKEN=private-token-value"} {
		if !slices.Contains(plan.Entries(), want) {
			t.Fatalf("entries do not contain %q", want)
		}
	}
	if text := fmt.Sprintf("%v", plan); strings.Contains(text, "private-") || text != "[private environment plan]" {
		t.Fatalf("formatted plan leaked: %q", text)
	}
	if _, err = json.Marshal(plan); err == nil {
		t.Fatal("private plan serialized")
	}
}

func TestResolveEnvironmentRejectsMissingAndDuplicateValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	controller := Controller{StateDirectory: dir}
	ctx := context.Background()
	if _, err := controller.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateProfile(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.SetSecret(ctx, "project", "EMPTY", "", false); err != nil {
		t.Fatal(err)
	}
	for _, names := range [][]string{{"MISSING"}, {"EMPTY"}, {"EMPTY", "EMPTY"}} {
		if _, err := controller.ResolveEnvironment(ctx, "project", names); err == nil {
			t.Fatalf("accepted invalid names: %v", names)
		}
	}
}
