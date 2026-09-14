//go:build linux

package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type response struct {
	OK   bool
	Data json.RawMessage
}

func requiredExecutable(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Skipf("%s is not configured", name)
	}
	if !filepath.IsAbs(value) {
		t.Fatalf("%s must be absolute", name)
	}
	return value
}

func requestID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func copyProject(t *testing.T, target string) {
	t.Helper()
	source := filepath.Join("testdata", "project")
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0644)
	}); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, workspace, profile, pnpm, node string) {
	t.Helper()
	quoted := func(value string) string { return strconv.Quote(value) }
	configuration := "profile = " + quoted(profile) + "\n\n" +
		"[commands.install]\nexec = [" + quoted(pnpm) + ", \"install\", \"--frozen-lockfile\"]\n\n" +
		"[commands.install-offline]\nexec = [" + quoted(pnpm) + ", \"install\", \"--frozen-lockfile\", \"--offline\"]\n\n" +
		"[commands.test]\nexec = [" + quoted(pnpm) + ", \"exec\", \"playwright\", \"test\"]\n\n" +
		"[commands.hold]\nexec = [" + quoted(node) + ", \"hold.mjs\"]\n"
	if err := os.WriteFile(filepath.Join(workspace, "devtools.toml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, environment []string, directory, binary string, arguments ...string) []byte {
	t.Helper()
	command := exec.Command(binary, arguments...)
	command.Dir = directory
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", binary, arguments, err, output)
	}
	return output
}

func decode(t *testing.T, raw []byte, target any) {
	t.Helper()
	var envelope response
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		t.Fatalf("invalid devtools response: %v %s", err, raw)
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatal(err)
	}
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}

func TestProjectExecutionContract(t *testing.T) {
	devtools := requiredExecutable(t, "LOKI_E2E_DEVTOOLS")
	pnpm := requiredExecutable(t, "LOKI_E2E_PNPM")
	node := requiredExecutable(t, "LOKI_E2E_NODE")
	chromium := requiredExecutable(t, "LOKI_E2E_CHROMIUM")
	t.Setenv("LOKI_E2E_SECRET", "synthetic-secret-must-not-leak")

	root := t.TempDir()
	workspaceA, workspaceB := filepath.Join(root, "project-a"), filepath.Join(root, "project-b")
	copyProject(t, workspaceA)
	copyProject(t, workspaceB)
	profile := fmt.Sprintf("loki-e2e-%d", os.Getpid())
	writeConfig(t, workspaceA, profile, pnpm, node)
	writeConfig(t, workspaceB, profile, pnpm, node)

	cache, state, temp, home := filepath.Join(root, "cache"), filepath.Join(root, "state"), filepath.Join(root, "temp"), filepath.Join(root, "home")
	for _, path := range []string{cache, state, temp, home, filepath.Join(cache, "npm"), filepath.Join(cache, "pnpm"), filepath.Join(cache, "playwright")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(state, "config"),
		"XDG_DATA_HOME=" + filepath.Join(state, "data"),
		"XDG_STATE_HOME=" + filepath.Join(state, "state"),
		"XDG_CACHE_HOME=" + cache,
		"NPM_CONFIG_CACHE=" + filepath.Join(cache, "npm"),
		"npm_config_store_dir=" + filepath.Join(cache, "pnpm"),
		"PLAYWRIGHT_BROWSERS_PATH=" + filepath.Join(cache, "playwright"),
		"LOKI_CHROMIUM_PATH=" + chromium,
		"TMPDIR=" + temp,
		"PATH=" + filepath.Dir(node) + ":/usr/bin:/bin",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_OPTIONAL_LOCKS=0",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}

	run(t, environment, workspaceA, devtools, "run", "install")
	run(t, environment, workspaceA, devtools, "run", "test")
	entries, err := os.ReadDir(filepath.Join(cache, "playwright"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("Playwright downloaded a browser into the candidate cache: %v, %v", entries, err)
	}

	if err = os.RemoveAll(filepath.Join(workspaceA, "node_modules")); err != nil {
		t.Fatal(err)
	}
	run(t, environment, workspaceA, devtools, "run", "install-offline")
	run(t, environment, workspaceA, devtools, "run", "test")

	for _, workspace := range []string{workspaceA, workspaceB} {
		if err = os.RemoveAll(filepath.Join(workspace, "node_modules")); err != nil {
			t.Fatal(err)
		}
	}
	var wait sync.WaitGroup
	failures := make(chan string, 2)
	for _, workspace := range []string{workspaceA, workspaceB} {
		wait.Add(1)
		go func(directory string) {
			defer wait.Done()
			command := exec.Command(devtools, "run", "install-offline")
			command.Dir, command.Env = directory, environment
			if output, runErr := command.CombinedOutput(); runErr != nil {
				failures <- fmt.Sprintf("%s: %v %s", directory, runErr, output)
			}
		}(workspace)
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}

	startID := requestID(t)
	start := run(t, environment, workspaceA, devtools, "process", "start", "hold", "--dir", workspaceA, "--capture-logs", "--request-id", startID)
	if strings.Contains(string(start), "synthetic-secret-must-not-leak") {
		t.Fatal("devtools start response leaked a parent secret")
	}
	var started struct {
		Item struct {
			ID string
		}
	}
	decode(t, start, &started)
	if started.Item.ID == "" {
		t.Fatal("managed process has no execution id")
	}
	stopped := false
	cleanupID := requestID(t)
	t.Cleanup(func() {
		if !stopped {
			command := exec.Command(devtools, "process", "stop", started.Item.ID, "--request-id", cleanupID)
			command.Dir, command.Env = workspaceA, environment
			_ = command.Run()
		}
	})

	holdPath := filepath.Join(workspaceA, "hold.json")
	deadline := time.Now().Add(5 * time.Second)
	var hold struct {
		Parent        int
		Child         int
		SecretPresent bool
	}
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(holdPath)
		if readErr == nil && json.Unmarshal(raw, &hold) == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hold.Parent == 0 || hold.Child == 0 || hold.SecretPresent {
		t.Fatalf("managed process environment = %#v", hold)
	}
	logs := run(t, environment, workspaceA, devtools, "process", "logs", started.Item.ID)
	if !strings.Contains(string(logs), "ready") || strings.Contains(string(logs), "synthetic-secret-must-not-leak") {
		t.Fatalf("unexpected managed logs: %s", logs)
	}
	run(t, environment, workspaceA, devtools, "process", "stop", started.Item.ID, "--request-id", requestID(t))
	stopped = true
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (processExists(hold.Parent) || processExists(hold.Child)) {
		time.Sleep(20 * time.Millisecond)
	}
	if processExists(hold.Parent) || processExists(hold.Child) {
		t.Fatalf("managed process group survived stop: parent=%d child=%d", hold.Parent, hold.Child)
	}
}
