//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/daemon"
	hostapplication "loki/internal/host/application"
	"loki/modules/execution/jobs"
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
	beforeBytes, err := os.ReadFile(identity)
	if err != nil {
		t.Fatal(err)
	}
	var before hostapplication.Identity
	if json.Unmarshal(beforeBytes, &before) != nil {
		t.Fatal("invalid fixture identity")
	}
	beforeArgument, _ := json.Marshal(before)
	activeLayout, err := os.ReadFile(filepath.Join(runtimeRoot, "layout.json"))
	if err != nil {
		t.Fatal(err)
	}
	activeAdmin, err := os.ReadFile(filepath.Join(filepath.Dir(identity), "secrets-admin.json"))
	if err != nil {
		t.Fatal(err)
	}
	candidateRuntime, err := os.MkdirTemp("/dev/shm", "loki-cli-candidate-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(candidateRuntime)
	generation := strings.Repeat("b", 32)
	if generation == before.ActiveGeneration {
		generation = strings.Repeat("c", 32)
	}
	candidateArgs := []string{"--uid", "10006", "--gid", "10006", "--agent-uid", "10000", "--metadata-gid", "10001", "--state", filepath.Join(root, "store"), "--identity", identity, "--runtime", candidateRuntime, "--service-state", "/var/lib/loki/secrets", "--config", "../../packaging/images/config/loki.toml", "--execution-contract", "../../packaging/images/config/execution-contract.json", "--catalog", catalog, "--overlay-directory=", "--unit-directory=", "--candidate-generation", generation, "--candidate-before", string(beforeArgument), "--candidate-operation", "123e4567-e89b-42d3-a456-426614174571"}
	var candidateReport bytes.Buffer
	if code := runHostPrepareSecrets(candidateArgs, &candidateReport, &bytes.Buffer{}); code != 0 {
		t.Fatal("owned candidate preparation failed", code)
	}
	var candidate map[string]any
	if json.Unmarshal(candidateReport.Bytes(), &candidate) != nil || candidate["candidate"] != true || candidate["active_generation"] != generation {
		t.Fatal("candidate metadata did not identify its inactive generation")
	}
	for path, want := range map[string][]byte{identity: beforeBytes, filepath.Join(runtimeRoot, "layout.json"): activeLayout, filepath.Join(filepath.Dir(identity), "secrets-admin.json"): activeAdmin} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("candidate CLI rewrote active identity or role channels", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(candidateRuntime, "admin-layout.json")); err != nil {
		t.Fatal("candidate did not get its own private administrator layout", err)
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
	commit := append(append([]string{}, candidateArgs...), "--commit-candidate", "--candidate-policy", candidate["policy"].(string))
	retained := filepath.Join(root, "store", "generations", before.ActiveGeneration, "dvt", "synthetic-retained-state")
	if err := os.WriteFile(retained, []byte("synthetic-ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := runHostPrepareSecrets(commit, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatal("selector publication discarded already managed state", code)
	}
	if err := os.Remove(retained); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		var report bytes.Buffer
		if code := runHostPrepareSecrets(commit, &report, &bytes.Buffer{}); code != 0 {
			t.Fatal("verified candidate selector publication/retry", code)
		}
		var selected hostapplication.Identity
		want := before
		want.Version, want.ActiveGeneration, want.PolicySHA256 = 2, generation, candidate["policy"].(string)
		if json.Unmarshal(report.Bytes(), &selected) != nil || selected != want {
			t.Fatal("selector did not atomically bind the exact generation and policy")
		}
	}
}

func TestHostOfflineMaintenanceJournalLock(t *testing.T) {
	if os.Geteuid() != 0 {
		if os.Getenv("LOKI_REQUIRE_HOST_APP_LAYOUT") == "1" {
			t.Fatal("root offline maintenance fixture is required")
		}
		t.Skip("native values suite executes this owned fixture as root")
	}
	root := t.TempDir()
	state := filepath.Join(root, "launcher")
	if err := os.MkdirAll(filepath.Join(state, "journal"), 0700); err != nil {
		t.Fatal(err)
	}
	layout := hostLauncherLayout{StateDirectory: state, MaxJobs: 8, MaxOutputBytes: 4096, ResultRetentionSeconds: 60}
	raw, _ := json.Marshal(layout)
	path := filepath.Join(root, "launcher.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	journal, err := jobs.OpenJournal(filepath.Join(state, "journal"), jobs.JournalLimits{MaxRecords: 8, MaxRecordBytes: jobs.JournalRecordBudget(4096), MaxOutputBytes: 4096, Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	id := "123e4567-e89b-12d3-a456-426614174399"
	if _, err := journal.BeginMaintenance(id, true); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if code := runHostAdmission([]string{"status", "--offline", "--launcher-layout", path}, &out, &diagnostic); code != 1 {
		t.Fatal("offline helper raced a live journal owner", code)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diagnostic.Reset()
	if code := runHostAdmission([]string{"end", "--offline", "--launcher-layout", path, "--operation-id", "123e4567-e89b-12d3-a456-426614174388"}, &out, &diagnostic); code != 1 {
		t.Fatal("offline helper cleared a different operation", code)
	}
	out.Reset()
	diagnostic.Reset()
	if code := runHostAdmission([]string{"end", "--offline", "--launcher-layout", path, "--operation-id", id}, &out, &diagnostic); code != 0 {
		t.Fatalf("offline release failed: %s", diagnostic.String())
	}
	var result jobs.Maintenance
	if json.Unmarshal(out.Bytes(), &result) != nil || result.OperationID != id || result.Active {
		t.Fatal("incorrect offline release metadata")
	}
}
