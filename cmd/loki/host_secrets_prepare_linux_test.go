//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"loki/internal/daemon"
)

// Exercise the shipped installer entry point with the shipped container role
// templates. The caller owns all fixture directories; no operator vault is read.
func TestHostPrepareSecretsFrozenRoles(t *testing.T) {
	if os.Geteuid() != 0 {
		if os.Getenv("LOKI_REQUIRE_HOST_APP_LAYOUT") == "1" {
			t.Fatal("root preparation CLI fixture is required")
		}
		t.Skip("native values suite executes this fixture as root")
	}
	root := t.TempDir()
	runtimeRoot, err := os.MkdirTemp("/dev/shm", "loki-cli-prepare-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtimeRoot)
	if err := os.Chmod(runtimeRoot, 0755); err != nil {
		t.Fatal(err)
	}
	roles := filepath.Join(runtimeRoot, "roles")
	if err := os.Mkdir(roles, 0700); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(root, "resources.json")
	if err := os.WriteFile(catalog, []byte(`{"resources":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(root, "store", "deployment.json")
	templates := filepath.Join(root, "templates")
	if err := os.Mkdir(templates, 0700); err != nil {
		t.Fatal(err)
	}
	// Image COPY installs these public templates as root. Reproduce that
	// ownership instead of trusting the checkout owner's role files as root.
	for _, role := range []string{"launcher", "runtime", "mcp"} {
		raw, err := os.ReadFile(filepath.Join("../../packaging/images/config", role+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(templates, role+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{
		"--uid", "10006", "--gid", "10006", "--agent-uid", "10000", "--metadata-gid", "10001",
		"--state", filepath.Join(root, "store"), "--identity", identity,
		"--runtime", runtimeRoot, "--service-state", "/var/lib/loki/secrets",
		"--config", "../../packaging/images/config/loki.toml",
		"--execution-contract", "../../packaging/images/config/execution-contract.json",
		"--catalog", catalog, "--service-catalog", "/etc/loki/application-resources.json",
		"--role-source", templates,
		"--overlay-directory", roles, "--unit-directory=", "--owner", "loki-tools-1234567890abcdef",
		"--maintenance-operation", "123e4567-e89b-12d3-a456-426614174399", "--interrupt-jobs",
	}
	var firstReport []byte
	for attempt := range 2 {
		var out, diagnostic bytes.Buffer
		if code := runHostPrepareSecrets(args, &out, &diagnostic); code != 0 {
			t.Fatalf("preparation exit %d: %s", code, diagnostic.String())
		}
		if attempt == 0 {
			firstReport = bytes.Clone(out.Bytes())
		} else if !bytes.Equal(firstReport, out.Bytes()) {
			t.Fatal("repeated preparation changed deployment identity or policy")
		}
	}
	for _, role := range []string{"launcher", "runtime", "mcp"} {
		path := filepath.Join(roles, role+".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if json.Unmarshal(raw, &fields) != nil || fields["ApplicationCatalog"] != "/etc/loki/application-resources.json" {
			t.Fatalf("%s has an inconsistent public catalog", role)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0640 {
			t.Fatal("role projection is not private", err)
		}
		switch role {
		case "runtime":
			if fields["ApplicationValuesExternal"] != true {
				t.Fatal("legacy app authority retained")
			}
		case "launcher":
			var inventory hostLauncherLayout
			if err := daemon.ReadJSON(path, &inventory); err != nil {
				t.Fatal("host Job inventory rejected the frozen launcher layout", err)
			}
			if fields["InitialMaintenanceOperation"] != "123e4567-e89b-12d3-a456-426614174399" || fields["InitialMaintenanceInterrupt"] != true {
				t.Fatal("initial host operation or original interruption choice was lost")
			}
			if fields["SecretSocket"] != filepath.Join(runtimeRoot, "jobs", "control.sock") || fields["SecretBrokerUID"] != float64(10006) || fields["DeploymentOwner"] != "loki-tools-1234567890abcdef" {
				t.Fatal("launcher identity binding is inconsistent")
			}
		case "mcp":
			if fields["ApplicationSocket"] != filepath.Join(runtimeRoot, "metadata", "control.sock") || fields["ApplicationUID"] != float64(10006) {
				t.Fatal("metadata identity binding is inconsistent")
			}
		}
	}
	if err := filepath.WalkDir(filepath.Join(root, "store"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "key-slot.json" || entry.Name() == "master.key" {
			t.Fatal("installer initialized cryptographic state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
