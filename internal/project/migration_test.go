package project

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestRootLegacyMigrationRunnerBoundary(t *testing.T) {
	if os.Getenv("LOKI_REQUIRE_ROOT_METADATA_TESTS") != "1" {
		t.Skip("explicit root migration acceptance")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root migration acceptance requires root")
	}
	runner := os.Getenv("LOKI_TEST_RUNNER")
	account, err := user.Lookup(runner)
	if err != nil || account.Uid == "0" {
		t.Fatal("a non-root development runner is required")
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)
	root, err := os.MkdirTemp("", "loki-legacy-root-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	os.Chmod(root, 0755)
	workspace := filepath.Join(root, "workspace")
	repo := filepath.Join(workspace, "repo")
	os.Mkdir(workspace, 0755)
	os.Mkdir(repo, 0700)
	os.Chown(repo, uid, gid)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("/usr/sbin/runuser", append([]string{"-u", runner, "--"}, args...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + account.HomeDir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("runner fixture: %s %v", out, err)
		}
		return string(out)
	}
	run("/usr/bin/git", "init", "-q", repo)
	source := legacyFixture(t, repo, false)
	os.Remove(filepath.Join(source, "data", "taskchampion.sqlite3"))
	if err = filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(path, uid, gid)
	}); err != nil {
		t.Fatal(err)
	}
	run("/usr/bin/env", "TASKRC="+filepath.Join(source, "taskrc"), "TASKDATA="+filepath.Join(source, "data"), "/home/linuxbrew/.linuxbrew/bin/task", "rc.hooks=off", "rc.confirmation=off", "add", "project:existing-test-workstream", "description:root migration fixture")
	store, err := New(workspace, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	store.Runner = runner
	store.GroupID = gid
	result, err := store.MigrateLegacy(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	tasks := Tasks{Store: store, Binary: "/home/linuxbrew/.linuxbrew/bin/task", Home: account.HomeDir}
	count, err := tasks.Do(t.Context(), TaskRequest{CWD: repo, Action: "count"})
	if err != nil || count["count"] != 1 {
		t.Fatal(count, err)
	}
	archive := result["legacy_archive"].(string)
	command := exec.Command("/usr/sbin/runuser", "-u", runner, "--", "/usr/bin/test", "-r", filepath.Join(archive, "data", "taskchampion.sqlite3"))
	if err = command.Run(); err == nil {
		t.Fatal("runner can read rollback archive")
	}
	id, err := store.Resolve(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	command = exec.Command("/usr/sbin/runuser", "-u", runner, "--", "/usr/bin/test", "-w", filepath.Join(id.StateDirectory, "taskwarrior", "taskrc"))
	if err = command.Run(); err == nil {
		t.Fatal("runner can modify central taskrc")
	}
	if _, err = store.RollbackLegacy(t.Context(), repo); err != nil {
		t.Fatal("root rollback", err)
	}
	output := run("/usr/bin/env", "TASKRC="+filepath.Join(source, "taskrc"), "TASKDATA="+filepath.Join(source, "data"), "/home/linuxbrew/.linuxbrew/bin/task", "rc.verbose=nothing", "rc.hooks=off", "count")
	if strings.TrimSpace(output) != "1" {
		t.Fatalf("restored runner task data: %q", output)
	}
}

func legacyFixture(t *testing.T, repo string, extra bool) string {
	t.Helper()
	source := filepath.Join(repo, ".tasks")
	for _, path := range []string{"data", "items/existing-test-workstream"} {
		if err := os.MkdirAll(filepath.Join(source, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"data/taskchampion.sqlite3": "synthetic database bytes", "items/existing-test-workstream/manifest.json": `{"slug":"existing-test-workstream"}`, "items/existing-test-workstream/plan.md": "preserved plan\n", "taskrc": "data.location=.tasks/data\nconfirmation=1\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if extra {
		os.Mkdir(filepath.Join(source, "verification"), 0700)
		os.WriteFile(filepath.Join(source, "verification", "latest.json"), []byte(`{"status":"passed"}`), 0600)
	}
	return source
}

func TestLegacyMigrationPreservesStateAndLocalOutputs(t *testing.T) {
	for _, extra := range []bool{false, true} {
		t.Run(map[bool]string{false: "central", true: "local-output"}[extra], func(t *testing.T) {
			store, repo, secondary := fixture(t)
			source := legacyFixture(t, repo, extra)
			before, err := migrationTree(t.Context(), filepath.Join(source, "data"), "")
			if err != nil {
				t.Fatal(err)
			}
			result, err := store.MigrateLegacy(t.Context(), repo)
			if err != nil {
				t.Fatal(err)
			}
			if result["legacy_removed"] != !extra || result["workstreams"] != 1 {
				t.Fatal(result)
			}
			id, err := store.Resolve(t.Context(), repo)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{filepath.Join(id.StateDirectory, "taskwarrior", "data"), filepath.Join(id.StateDirectory, "legacy", "data")} {
				after, err := migrationTree(t.Context(), path, "")
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("database preservation %v %v", after, err)
				}
			}
			config, err := os.ReadFile(filepath.Join(id.StateDirectory, "taskwarrior", "taskrc"))
			if err != nil || !strings.Contains(string(config), "data.location="+filepath.Join(id.StateDirectory, "taskwarrior", "data")) {
				t.Fatal("central taskrc", err)
			}
			if extra {
				raw, err := os.ReadFile(filepath.Join(source, "verification", "latest.json"))
				if err != nil || string(raw) != `{"status":"passed"}` {
					t.Fatal("local output changed")
				}
			}
			status, err := store.Status(t.Context(), secondary)
			if err != nil || status["initialized"] != true {
				t.Fatal("worktree state", status, err)
			}
			read, err := store.ReadArtifact(t.Context(), repo, nil, "plan.md")
			if err != nil || read["content"] != "preserved plan\n" {
				t.Fatal("workstream", read, err)
			}
			again, err := store.MigrateLegacy(t.Context(), repo)
			if err != nil || !reflect.DeepEqual(result, again) {
				t.Fatal("repeat migration", again, err)
			}
		})
	}
}

func TestLegacyMigrationResumesInterruptedStaging(t *testing.T) {
	store, repo, _ := fixture(t)
	source := legacyFixture(t, repo, true)
	id, err := store.Resolve(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.mutationLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	stage := filepath.Join(filepath.Dir(id.StateDirectory), ".migrate-"+id.ProjectID)
	os.Mkdir(stage, 0700)
	record := migrationRecord{Version: 1, WorktreeID: id.WorktreeID, Source: source, UID: os.Getuid(), GID: os.Getgid(), Mode: 0700, Retained: []string{"verification"}}
	if err = writeJSON(filepath.Join(stage, "migration.json"), record, false); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(source, filepath.Join(stage, "legacy")); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(stage, "taskwarrior", "data"), 0700)
	os.WriteFile(filepath.Join(stage, "taskwarrior", "data", "taskchampion.sqlite3"), []byte("partial copy"), 0600)
	if _, err = store.MigrateLegacy(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(id.StateDirectory, "taskwarrior", "data", "taskchampion.sqlite3"))
	if err != nil || string(raw) != "synthetic database bytes" {
		t.Fatal("partial data published")
	}
	// Resume a crash after publication but before local outputs were restored.
	if err = os.Rename(filepath.Join(source, "verification"), filepath.Join(id.StateDirectory, "legacy", "verification")); err != nil {
		t.Fatal(err)
	}
	if err = writeJSON(filepath.Join(id.StateDirectory, "migration.json"), record, true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.MigrateLegacy(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(source, "verification", "latest.json")); err != nil {
		t.Fatal("local output not recovered", err)
	}
}

func TestLegacyMigrationUsesRealTaskwarriorData(t *testing.T) {
	binary := "/home/linuxbrew/.linuxbrew/bin/task"
	if _, err := os.Stat(binary); err != nil {
		t.Skip("Taskwarrior unavailable")
	}
	store, repo, _ := fixture(t)
	source := legacyFixture(t, repo, false)
	os.Remove(filepath.Join(source, "data", "taskchampion.sqlite3"))
	command := exec.Command(binary, "rc.hooks=off", "rc.confirmation=off", "add", "project:existing-test-workstream", "description:preserve real task")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "TASKRC=" + filepath.Join(source, "taskrc"), "TASKDATA=" + filepath.Join(source, "data")}
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("legacy task %s %v", output, err)
	}
	if _, err := store.MigrateLegacy(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	tasks := Tasks{Store: store, Binary: binary, Home: t.TempDir()}
	result, err := tasks.Do(t.Context(), TaskRequest{CWD: repo, Action: "count"})
	if err != nil || result["count"] != 1 {
		t.Fatal("migrated task readback", result, err)
	}
}

func TestLegacyMigrationRejectsUnsafeAndExistingDestinations(t *testing.T) {
	store, repo, _ := fixture(t)
	source := legacyFixture(t, repo, false)
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("outside"), 0600)
	os.Symlink(outside, filepath.Join(source, "data", "link"))
	if _, err := store.MigrateLegacy(t.Context(), repo); err == nil {
		t.Fatal("unsafe source accepted")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("invalid source moved")
	}
	os.Remove(filepath.Join(source, "data", "link"))
	initialize(t, store, repo)
	if _, err := store.MigrateLegacy(t.Context(), repo); err == nil {
		t.Fatal("existing central state replaced")
	}
}
