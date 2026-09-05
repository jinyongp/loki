package action

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/process"
	"loki/internal/secret"
)

func materializationFixture(t *testing.T, binary string, fixed bool, program string) (*Runtime, secret.Controller, secret.ActionPlan) {
	t.Helper()
	c, workspace := actionControllerFixture(t, []string{"awk", program})
	if _, err := c.ImportValues(t.Context(), "fixture", map[string]string{"MULTI": "line one\nline \"two\"\\end", "EMPTY": ""}); err != nil {
		t.Fatal(err)
	}
	policy := map[string]any{"command": []string{"awk", program}, "cwd": ".", "secrets": []string{"TOKEN", "MULTI", "EMPTY"}, "all_secrets": false, "timeout_seconds": 10, "max_output_bytes": 4096, "materialize_env_file": "ENV_FILE"}
	if fixed {
		policy["materialize_env_path"] = "fixture.env"
	}
	encoded, _ := json.Marshal(policy)
	if _, err := c.SetAction(t.Context(), "fixture", "check", encoded); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{filepath.Join(workspace, ".tmp")} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	snapshots := filepath.Join(root, "snapshots")
	if err := os.Mkdir(snapshots, 0700); err != nil {
		t.Fatal(err)
	}
	recovery := filepath.Join(root, "recovery")
	if err := os.Mkdir(recovery, 0700); err != nil {
		t.Fatal(err)
	}
	layout := Layout{Workspace: workspace, Binary: binary, Bwrap: "/usr/bin/bwrap", UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), MaterializationDirectory: filepath.Join(root, "materializations"), MaterializationRecoveryDirectory: recovery, SnapshotDirectory: snapshots}
	r, err := NewRuntime(c, layout, process.ManagerOptions{MaxProcesses: 4, MaxOutputBytes: 4096, Retention: time.Minute, StopGrace: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	plan, err := c.ResolveAction(t.Context(), "fixture", "check", nil)
	if err != nil {
		t.Fatal(err)
	}
	return r, c, plan
}

func requireSandbox(t *testing.T) {
	t.Helper()
	probe := exec.CommandContext(t.Context(), "/usr/bin/bwrap", "--unshare-all", "--ro-bind", "/usr", "/usr", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--", "/usr/bin/true")
	if err := probe.Run(); err != nil || os.Getuid() == 0 {
		if os.Getenv("LOKI_REQUIRE_SANDBOX_TESTS") == "1" {
			t.Fatalf("non-root sandbox required: %v", err)
		}
		t.Skip("non-root bubblewrap unavailable")
	}
}

func TestMaterializationSandbox(t *testing.T) {
	requireSandbox(t)
	binary := candidateBinary(t)
	for _, fixed := range []bool{true, false} {
		name := "fixed"
		if !fixed {
			name = "session"
		}
		t.Run(name, func(t *testing.T) {
			program := `BEGIN { print ENVIRON["ENV_FILE"]; print "tmpdir=" ENVIRON["TMPDIR"]; while ((getline line < ENVIRON["ENV_FILE"]) > 0) print line; print "writable=" system("test -w " ENVIRON["ENV_FILE"]); system("stat -c %a " ENVIRON["ENV_FILE"]); while ((getline line < "/proc/self/status") > 0) if (line ~ /^Cap(Inh|Prm|Eff|Amb):|^NoNewPrivs:/) print line; print "remount=" (system("mount -o remount,rw " ENVIRON["ENV_FILE"] " >/dev/null 2>&1") != 0); print "write-denied=" (system("dd if=/dev/zero of=" ENVIRON["ENV_FILE"] " count=1 status=none 2>/dev/null") != 0); print "tmp-exec=" system("cp /usr/bin/true /tmp/executable && /tmp/executable"); system("find /tmp -name '.loki-materialization-*' -print"); print "snapshot" > (ENVIRON["TMPDIR"] "/snapshot.txt"); print "ready"; while ((getline flag < "/workspace/release") < 0) { close("/workspace/release"); system("sleep 0.01") } }`
			r, _, _ := materializationFixture(t, binary, fixed, program)
			started, err := r.Run(t.Context(), RunRequest{Profile: "fixture", Action: "check"})
			if err != nil {
				t.Fatal(err)
			}
			id := started["session_id"].(string)
			deadline := time.Now().Add(10 * time.Second)
			var value map[string]any
			for {
				value, err = r.Read(id, nil, 4096)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(value["output"].(string), "ready\n") {
					break
				}
				if value["status"] == "exited" || time.Now().After(deadline) {
					t.Fatalf("materialization did not run: %#v", value)
				}
				time.Sleep(10 * time.Millisecond)
			}
			output := value["output"].(string)
			for _, expected := range []string{"CapInh:\t0000000000000000\n", "CapPrm:\t0000000000000000\n", "CapEff:\t0000000000000000\n", "CapAmb:\t0000000000000000\n", "NoNewPrivs:\t1\n", "remount=1\n", "write-denied=1\n", "tmp-exec=0\n"} {
				if !strings.Contains(output, expected) {
					t.Fatalf("sandbox protection missing %q: %q", expected, output)
				}
			}
			if strings.Contains(output, ".loki-materialization-") {
				t.Fatal("writable materialization alias remains in /tmp")
			}
			visible := strings.Split(output, "\n")[0]
			if !strings.HasPrefix(visible, "/workspace/") || !strings.Contains(output, "writable=1\n600\n") || !strings.Contains(output, "TOKEN=[REDACTED]") || !strings.Contains(output, "MULTI=[REDACTED]") || strings.Contains(output, "synthetic-action-value") || strings.Contains(output, `line one\nline`) {
				t.Fatalf("materialization output: %q", output)
			}
			target := filepath.Join(r.layout.Workspace, strings.TrimPrefix(visible, "/workspace/"))
			info, err := os.Lstat(target)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 0 {
				t.Fatalf("host placeholder is not private/empty: %v %v", info, err)
			}
			if data, err := os.ReadFile(target); err != nil || len(data) != 0 {
				t.Fatal("secret persisted in host workspace")
			}
			if data, err := os.ReadFile(filepath.Join(r.layout.SnapshotDirectory, "snapshot.txt")); err != nil || string(data) != "snapshot\n" {
				t.Fatal("snapshot mount is not writable")
			}
			if fixed {
				if _, err := r.ClearMaterialization(t.Context(), PrepareRequest{Profile: "fixture", Action: "check"}); err == nil || err.Error() != "action is still running" {
					t.Fatalf("cleared live materialization: %v", err)
				}
				if _, err := r.Run(t.Context(), RunRequest{Profile: "fixture", Action: "check"}); err == nil || err.Error() != "materialized secret target is in use" {
					t.Fatalf("concurrent materialization accepted: %v", err)
				}
			}
			if _, err = r.Stop(id); err != nil {
				t.Fatal(err)
			}
			value, err = r.Read(id, nil, 4096)
			if err != nil || value["cleanup_error"] != nil {
				t.Fatalf("cleanup failed: %#v %v", value, err)
			}
			if _, err = os.Lstat(target); !os.IsNotExist(err) {
				t.Fatal("completed action left its placeholder")
			}
			entries, err := os.ReadDir(r.layout.MaterializationDirectory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(r.layout.MaterializationDirectory, entry.Name()))
				if err != nil || len(data) != 0 {
					t.Fatal("completed materialization journal not cleared")
				}
			}
		})
	}
}

func TestMaterializationMountGuards(t *testing.T) {
	requireSandbox(t)
	binary := candidateBinary(t)
	for _, mode := range []string{"replaced", "edited", "symlink", "hardlink", "parent-symlink", "missing-capability"} {
		t.Run(mode, func(t *testing.T) {
			r, _, plan := materializationFixture(t, binary, true, `BEGIN { print "command-executed" }`)
			if mode == "parent-symlink" {
				plan.Policy.MaterializeEnvPath = "nested/fixture.env"
				if err := os.Mkdir(filepath.Join(r.layout.Workspace, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			launch, err := Prepare(r.layout, plan)
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Close()
			target := filepath.Join(r.layout.Workspace, filepath.FromSlash(launch.materialization.target))
			preserved := target
			want := ""
			switch mode {
			case "replaced":
				if err := os.Rename(target, target+".original"); err != nil {
					t.Fatal(err)
				}
				want = "user data"
				if err := os.WriteFile(target, []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
			case "edited":
				want = "user data"
				if err := os.WriteFile(target, []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				preserved = filepath.Join(r.layout.Workspace, "other.env")
				want = "other user data"
				if err := os.WriteFile(preserved, []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(target, target+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other.env", target); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(target, target+".copy"); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				parent := filepath.Dir(target)
				if err := os.Rename(parent, parent+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("nested-original", parent); err != nil {
					t.Fatal(err)
				}
			case "missing-capability":
				var argv []string
				for i := 0; i < len(launch.spec.Argv); i++ {
					if launch.spec.Argv[i] == "--cap-add" {
						i++
						continue
					}
					argv = append(argv, launch.spec.Argv[i])
				}
				launch.spec.Argv = argv
			}
			started, err := launch.Start(r.processes)
			if err != nil {
				t.Fatal(err)
			}
			launch.Close()
			result := completed(t, r.processes, started["session_id"].(string))
			stage := "materialization-guards"
			if mode == "missing-capability" {
				stage = "sandbox-guards"
			} else if data, err := os.ReadFile(preserved); err != nil || string(data) != want {
				t.Fatalf("user file changed: %q %v", data, err)
			}
			if mode == "symlink" || mode == "parent-symlink" {
				stage += ", errno=40" // ELOOP from RESOLVE_NO_SYMLINKS.
			}
			if result["exit_code"] != 125 || result["output"] != "action execution failed ["+stage+"]\n" {
				t.Fatalf("unsafe materialized command ran: %#v", result)
			}
		})
	}
}

func TestMaterializationRecoveryAndPreservation(t *testing.T) {
	requireSandbox(t)
	binary := candidateBinary(t)
	for _, mode := range []string{"recover", "edited", "replaced", "symlink", "hardlink", "preexisting", "abandoned", "start-failure"} {
		t.Run(mode, func(t *testing.T) {
			r, _, plan := materializationFixture(t, binary, true, `BEGIN { print "ok" }`)
			target := filepath.Join(r.layout.Workspace, "fixture.env")
			if mode == "preexisting" {
				if err := os.WriteFile(target, []byte("user data"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := Prepare(r.layout, plan); err == nil || err.Error() != "materialized secret target already exists" {
					t.Fatalf("existing file accepted: %v", err)
				}
				if data, err := os.ReadFile(target); err != nil || string(data) != "user data" {
					t.Fatal("existing user file changed")
				}
				return
			}
			launch, err := Prepare(r.layout, plan)
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Close()
			switch mode {
			case "recover":
				// Simulate an abrupt controller death: descriptors close without
				// cleanup. The next holder recovers only the recorded empty inode.
				m := launch.materialization
				m.marker.Close()
				m.workspace.Close()
				m.binary.Close()
				launch.materialization = nil
				launch.Close()
				next, err := Prepare(r.layout, plan)
				if err != nil {
					t.Fatal(err)
				}
				next.Close()
			case "edited":
				if err := os.WriteFile(target, []byte("user data"), 0600); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				if err := os.Rename(target, target+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/etc/passwd", target); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(target, target+".copy"); err != nil {
					t.Fatal(err)
				}
			case "start-failure":
				launch.spec.Argv = []string{"/does-not-exist"}
				if _, err := launch.Start(r.processes); err == nil {
					t.Fatal("missing launcher accepted")
				}
			}
			preserve := mode == "edited" || mode == "replaced" || mode == "symlink" || mode == "hardlink"
			if preserve {
				if err := launch.materialization.close(); err == nil || err.Error() != "materialized secret target is unsafe to clear" {
					t.Fatalf("modified placeholder removed: %v", err)
				}
				if _, err := os.Lstat(target); err != nil {
					t.Fatal("modified file was removed")
				}
			} else {
				launch.Close()
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatal("placeholder not cleaned")
				}
			}
		})
	}
}

func TestMaterializationSessionRecoveryAndCompletion(t *testing.T) {
	requireSandbox(t)
	binary := candidateBinary(t)
	t.Run("session-slots", func(t *testing.T) {
		r, _, plan := materializationFixture(t, binary, false, `BEGIN { print "ok" }`)
		first, err := Prepare(r.layout, plan)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		oldTarget := filepath.Join(r.layout.Workspace, first.materialization.target)
		second, err := Prepare(r.layout, plan)
		if err != nil {
			t.Fatal(err)
		}
		defer second.Close()
		if first.materialization.target == second.materialization.target {
			t.Fatal("concurrent sessions share a placeholder")
		}
		m := first.materialization
		m.marker.Close()
		m.workspace.Close()
		m.binary.Close()
		first.materialization = nil
		first.Close()
		for range 5 {
			next, err := Prepare(r.layout, plan)
			if err != nil {
				t.Fatal(err)
			}
			next.Close()
		}
		if _, err := os.Lstat(oldTarget); !os.IsNotExist(err) {
			t.Fatal("stale session placeholder not recovered")
		}
		entries, err := os.ReadDir(r.layout.MaterializationDirectory)
		if err != nil || len(entries) != 2 {
			t.Fatal("session journals grew without bound")
		}
	})
	for _, mode := range []string{"success", "failure", "timeout", "cleanup-warning"} {
		t.Run(mode, func(t *testing.T) {
			program := `BEGIN { print "ok" }`
			if mode == "failure" {
				program = `BEGIN { exit 7 }`
			}
			if mode == "timeout" || mode == "cleanup-warning" {
				program = `BEGIN { system("sleep 30") }`
			}
			r, _, plan := materializationFixture(t, binary, true, program)
			plan.Policy.TimeoutSeconds = 1
			launch, err := Prepare(r.layout, plan)
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Close()
			started, err := launch.Start(r.processes)
			if err != nil {
				t.Fatal(err)
			}
			launch.Close()
			target := filepath.Join(r.layout.Workspace, "fixture.env")
			if mode == "cleanup-warning" {
				if err := os.WriteFile(target, []byte("user data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			value := completed(t, r.processes, started["session_id"].(string))
			if mode == "cleanup-warning" {
				if value["cleanup_error"] != "materialized secret target is unsafe to clear" {
					t.Fatalf("cleanup warning missing: %#v", value)
				}
				if data, err := os.ReadFile(target); err != nil || string(data) != "user data" {
					t.Fatal("edited target removed")
				}
				result, err := r.ClearMaterialization(t.Context(), PrepareRequest{Profile: "fixture", Action: "check"})
				if err != nil || result["cleared"] != true {
					t.Fatalf("explicit registered cleanup failed: %#v %v", result, err)
				}
			} else if value["cleanup_error"] != nil {
				t.Fatalf("unexpected cleanup error: %#v", value)
			}
			if mode == "success" && value["exit_code"] != 0 || mode == "failure" && value["exit_code"] != 7 || mode == "timeout" && value["timed_out"] != true {
				t.Fatalf("completion result: %#v", value)
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatal("completion left placeholder")
			}
		})
	}
}

func TestActionLockProbe(t *testing.T) {
	requireSandbox(t)
	binary := candidateBinary(t)
	plan, workspace := actionFixture(t, []string{"pwd"})
	plan.Policy.LockProbe = "runtime.lock"
	layout := Layout{Workspace: workspace, Binary: binary, Bwrap: "/usr/bin/bwrap", UID: uint32(os.Getuid()), GID: uint32(os.Getgid())}
	launch, err := Prepare(layout, plan)
	if err != nil {
		t.Fatal(err)
	}
	launch.Close()
	if _, err := os.Lstat(filepath.Join(workspace, "runtime.lock")); !os.IsNotExist(err) {
		t.Fatal("missing lock probe created a file")
	}
	file, err := os.OpenFile(filepath.Join(workspace, "runtime.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(layout, plan); err == nil || !strings.Contains(err.Error(), "action runtime is already owned outside the current Loki session: /workspace/runtime.lock") {
		t.Fatalf("held lock accepted: %v", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	launch, err = Prepare(layout, plan)
	if err != nil {
		t.Fatal(err)
	}
	launch.Close()
	if err := os.Symlink("/etc/passwd", filepath.Join(workspace, "symlink.lock")); err != nil {
		t.Fatal(err)
	}
	plan.Policy.LockProbe = "symlink.lock"
	if _, err := Prepare(layout, plan); err == nil {
		t.Fatal("symlink lock probe followed")
	}
}
