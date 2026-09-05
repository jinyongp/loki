package secret

import (
	"bytes"
	"reflect"
	"testing"
)

func TestBootstrapPreflight(t *testing.T) {
	c := fixture(t)
	check := func(_ map[string]any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(c.CreateProfile(t.Context(), "web"))
	check(c.ImportValues(t.Context(), "web", map[string]string{"TOKEN": "", "EMPTY": "", "PRIVATE": "synthetic-private", "PUBLIC_API": "http://localhost:41280"}))
	check(c.SetAction(t.Context(), "web", "setup", encoded(t, action("project"))))
	check(c.Register(t.Context(), "project", nil))
	check(c.SetWorkflow(t.Context(), "project", "development", encoded(t, map[string]any{
		"steps": [][]string{{"web", "setup"}}, "required_secrets": map[string][]string{"web": {"TOKEN", "EMPTY"}}, "timeout_seconds": 60,
	})))
	plan, err := c.BootstrapPreflight(t.Context(), "feature", "development")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Metadata["configuration_ready"] != false || plan.TimeoutSeconds != 60 || plan.Metadata["cwd"] != "feature" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if !reflect.DeepEqual(plan.Metadata["missing_required_secrets"], map[string][]string{"web": {"EMPTY", "TOKEN"}}) {
		t.Fatal(plan.Metadata)
	}
	if bytes.Contains(encoded(t, plan), []byte("synthetic-private")) {
		t.Fatal("secret value exposed")
	}
	check(c.ImportValues(t.Context(), "web", map[string]string{"TOKEN": "synthetic-token", "EMPTY": "configured"}))
	plan, err = c.BootstrapPreflight(t.Context(), "feature", "development")
	if err != nil || plan.Metadata["configuration_ready"] != true {
		t.Fatalf("ready: %#v %v", plan, err)
	}
	if _, err := c.BootstrapPreflight(t.Context(), "other", "development"); err == nil {
		t.Fatal("unregistered repository accepted")
	}
	if _, err := c.BootstrapPreflight(t.Context(), "project", "missing"); err == nil {
		t.Fatal("unknown workflow accepted")
	}
}
