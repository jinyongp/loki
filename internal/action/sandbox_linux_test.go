package action

import (
	"debug/elf"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/process"
	"loki/internal/project"
	"loki/internal/secret"
)

func actionFixture(t *testing.T, argv []string) (secret.ActionPlan, string) {
	t.Helper()
	c, workspace := actionControllerFixture(t, argv)
	plan, err := c.ResolveAction(t.Context(), "fixture", "check", nil)
	if err != nil {
		t.Fatal(err)
	}
	return plan, workspace
}

func actionControllerFixture(t *testing.T, argv []string) (secret.Controller, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	projects, err := project.New(workspace, filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	c := secret.Controller{StateDirectory: filepath.Join(root, "runtime"), Projects: projects}
	if _, err = c.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.CreateProfile(t.Context(), "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ImportValues(t.Context(), "fixture", map[string]string{"TOKEN": "synthetic-action-value"}); err != nil {
		t.Fatal(err)
	}
	policy, _ := json.Marshal(map[string]any{"command": argv, "cwd": ".", "secrets": []string{"TOKEN"}, "all_secrets": false, "required_secrets": []string{"TOKEN"}, "timeout_seconds": 10, "max_output_bytes": 4096})
	if _, err = c.SetAction(t.Context(), "fixture", "check", policy); err != nil {
		t.Fatal(err)
	}
	return c, workspace
}

func candidateBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "loki")
	cmd := exec.CommandContext(t.Context(), filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-trimpath", "-o", binary, "../../cmd/loki")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v %s", err, output)
	}
	executable, err := elf.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()
	for _, program := range executable.Progs {
		if program.Type == elf.PT_INTERP {
			t.Fatal("Go candidate depends on a dynamic ELF interpreter")
		}
	}
	return binary
}

func completed(t *testing.T, manager *process.Manager, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		value, err := manager.Read(id, nil, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if value["status"] == "exited" {
			_, _ = manager.Stop(id)
			value, _ = manager.Read(id, nil, 4096)
			return value
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("action did not complete")
	return nil
}

func TestSandboxExecutionAndGuards(t *testing.T) {
	if os.Getuid() == 0 {
		if os.Getenv("LOKI_REQUIRE_SANDBOX_TESTS") == "1" {
			t.Fatal("sandbox acceptance requires a non-root test user")
		}
		t.Skip("sandbox acceptance requires a non-root test user")
	}
	probe := exec.CommandContext(t.Context(), "/usr/bin/bwrap", "--unshare-all", "--ro-bind", "/usr", "/usr", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--", "/usr/bin/true")
	if output, err := probe.CombinedOutput(); err != nil {
		if os.Getenv("LOKI_REQUIRE_SANDBOX_TESTS") == "1" {
			t.Fatalf("sandbox required: %v %s", err, output)
		}
		t.Skipf("bubblewrap namespaces unavailable: %v", err)
	}
	binary := candidateBinary(t)
	manager, err := process.NewManager(process.ManagerOptions{MaxProcesses: 4, MaxOutputBytes: 4096, Retention: time.Minute, RequireRedactor: true})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	t.Setenv("LOKI_HOST_SENTINEL", "host-only-environment")
	t.Run("sealed-injection-and-confinement", func(t *testing.T) {
		hostFile := filepath.Join(t.TempDir(), "host-only.txt")
		if err := os.WriteFile(hostFile, []byte("host-only-file"), 0600); err != nil {
			t.Fatal(err)
		}
		program := `BEGIN { print ENVIRON["TOKEN"]; print "host-env=" ENVIRON["LOKI_HOST_SENTINEL"]; print "stdin=" (getline input); print ENVIRON["TOKEN"] > "/workspace/received.txt"; print "outside=" (getline outside < "` + hostFile + `"); print "workspace-write-ok" > "/workspace/written.txt" }`
		plan, workspace := actionFixture(t, []string{"awk", program})
		layout := Layout{Workspace: workspace, Binary: binary, Bwrap: "/usr/bin/bwrap", UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
		launch, err := Prepare(layout, plan)
		if err != nil {
			t.Fatal(err)
		}
		defer launch.Close()
		for _, entry := range append(append([]string{}, launch.spec.Argv...), launch.spec.Env...) {
			if strings.Contains(entry, "synthetic-action-value") || strings.Contains(entry, "LOKI_HOST_SENTINEL") {
				t.Fatal("private or ambient data in control argv/environment")
			}
		}
		result, err := launch.Start(manager)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = launch.Start(manager); err == nil {
			t.Fatal("credential input launch was reused")
		}
		launch.Close()
		result = completed(t, manager, result["session_id"].(string))
		if result["exit_code"] != 0 || result["output"] != "[REDACTED]\nhost-env=\nstdin=0\noutside=-1\n" {
			t.Fatalf("sandbox result: %#v", result)
		}
		data, err := os.ReadFile(filepath.Join(workspace, "received.txt"))
		if err != nil || string(data) != "synthetic-action-value\n" {
			t.Fatal("secret was not injected in target namespace")
		}
		data, err = os.ReadFile(filepath.Join(workspace, "written.txt"))
		if err != nil || string(data) != "workspace-write-ok\n" {
			t.Fatal("bound workspace is not writable")
		}
	})
	t.Run("pinned-mount-and-executable", func(t *testing.T) {
		plan, workspace := actionFixture(t, []string{"pwd"})
		copyBinary := filepath.Join(t.TempDir(), "loki")
		data, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(copyBinary, data, 0700); err != nil {
			t.Fatal(err)
		}
		launch, err := Prepare(Layout{Workspace: workspace, Binary: copyBinary, Bwrap: "/usr/bin/bwrap", UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}, plan)
		if err != nil {
			t.Fatal(err)
		}
		defer launch.Close()
		if err = os.Rename(workspace, workspace+"-moved"); err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(t.TempDir(), workspace); err != nil {
			t.Fatal(err)
		}
		if err = os.Rename(copyBinary, copyBinary+"-moved"); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(copyBinary, []byte("#!/bin/sh\nprintf wrong-binary"), 0700); err != nil {
			t.Fatal(err)
		}
		result, err := launch.Start(manager)
		if err != nil {
			t.Fatal(err)
		}
		launch.Close()
		result = completed(t, manager, result["session_id"].(string))
		if result["exit_code"] != 0 || result["output"] != "/workspace\n" {
			t.Fatalf("pinned launch changed: %#v", result)
		}
	})
	t.Run("outside-namespace-rejected", func(t *testing.T) {
		plan, workspace := actionFixture(t, []string{"pwd"})
		launch, err := Prepare(Layout{Workspace: workspace, Binary: binary, Bwrap: "/usr/bin/bwrap", UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}, plan)
		if err != nil {
			t.Fatal(err)
		}
		defer launch.Close()
		cmd := exec.CommandContext(t.Context(), binary, "internal", "action-exec")
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		cmd.Stdin = launch.spec.Stdin
		output, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 125 || string(output) != "action execution failed [sandbox-guards]\n" {
			t.Fatalf("unguarded helper: %v %q", err, output)
		}
	})
}

func TestPayloadSealsBoundsAndPrivacy(t *testing.T) {
	p := payload{Version: 1, Argv: []string{"/usr/bin/true"}, CWD: "/workspace", Environment: []string{"TOKEN=synthetic-action-value"}, UID: 1000, GID: 1000, ParentMountNS: 1, ParentPIDNS: 2, WorkspaceDevice: 3, WorkspaceInode: 4}
	file, err := p.seal()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = file.WriteAt([]byte("x"), 0); !errors.Is(err, unix.EPERM) {
		t.Fatalf("payload is mutable: %v", err)
	}
	if err = file.Truncate(0); !errors.Is(err, unix.EPERM) {
		t.Fatalf("payload can be truncated: %v", err)
	}
	decoded, err := readPayload(file)
	if err != nil || decoded.Environment[0] != p.Environment[0] {
		t.Fatal("sealed payload roundtrip")
	}
	if _, err = json.Marshal(p); err == nil {
		t.Fatal("private payload serialized")
	}
	for _, env := range [][]string{{"TOKEN=a", "TOKEN=b"}, {"TOKEN=embedded\x00value"}, {"TOKEN=" + strings.Repeat("x", 131072)}} {
		invalid := p
		invalid.Environment = env
		if _, err = invalid.seal(); err == nil {
			t.Fatal("invalid payload accepted")
		}
	}
	plain, err := os.CreateTemp(t.TempDir(), "unsealed-")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err = plain.Write([]byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = readPayload(plain); err == nil {
		t.Fatal("unsealed input accepted")
	}
}
