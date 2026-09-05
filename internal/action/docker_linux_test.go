package action

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDockerActionRealDaemon(t *testing.T) {
	if os.Getenv("LOKI_REQUIRE_DOCKER_TESTS") != "1" {
		t.Skip("explicit disposable Docker acceptance not requested")
	}
	requireSandbox(t)
	const socket = "/run/docker.sock"
	command := func(input []byte, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), "/usr/bin/docker", append([]string{"--host", "unix://" + socket}, args...)...)
		cmd.Stdin = bytes.NewReader(input)
		return cmd.CombinedOutput()
	}
	if output, err := command(nil, "info", "--format", "{{.ServerVersion}}"); err != nil {
		t.Fatalf("Docker unavailable: %v %s", err, output)
	}
	buildRoot := t.TempDir()
	source := filepath.Join(buildRoot, "probe.go")
	binary := filepath.Join(buildRoot, "probe")
	program := `package main
import("fmt";"os")
func main(){data,err:=os.ReadFile("/probe/input.txt");if err!=nil{os.Exit(3)};fmt.Print(string(data))}
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(t.Context(), filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-trimpath", "-o", binary, source)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("probe build: %v %s", err, output)
	}
	probe, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "probe-exec", Mode: 0755, Size: int64(len(probe))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(probe); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := command(archive.Bytes(), "image", "import", "-")
	if err != nil {
		t.Fatalf("probe import: %v %s", err, output)
	}
	image := strings.TrimSpace(string(output))
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatalf("unexpected image ID: %s", output)
	}
	t.Cleanup(func() {
		cmd := exec.Command("/usr/bin/docker", "--host", "unix://"+socket, "image", "rm", image)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("probe image cleanup: %v %s", err, output)
		}
	})
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "loki-go-probe-" + hex.EncodeToString(nonce[:])
	t.Cleanup(func() {
		exec.Command("/usr/bin/docker", "--host", "unix://"+socket, "container", "rm", "--force", name).Run()
	})
	r, c := runtimeFixture(t, nil)
	r.layout.Binary = candidateBinary(t)
	r.layout.DockerSocket = socket
	r.layout.DockerDirectory = t.TempDir()
	r.layout.DockerUID = 0
	if err := os.WriteFile(filepath.Join(r.layout.Workspace, "input.txt"), []byte("Docker host bind verified\n"), 0600); err != nil {
		t.Fatal(err)
	}
	argv := []string{"docker", "run", "--rm", "--name", name, "--network=none", "--read-only", "--memory=32m", "--pids-limit=32", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), "--mount", "type=bind,src=" + r.layout.Workspace + ",dst=/probe,readonly", image, "/probe-exec"}
	if err := os.WriteFile(filepath.Join(r.layout.Workspace, "docker-check.py"), []byte("import os, sys\nos.execv('/usr/bin/docker', ['docker', *sys.argv[1:]])\n"), 0600); err != nil {
		t.Fatal(err)
	}
	argv = append([]string{"python3", "docker-check.py"}, argv[1:]...)
	encoded, _ := json.Marshal(map[string]any{"command": argv, "cwd": ".", "secrets": []string{"TOKEN"}, "all_secrets": false, "timeout_seconds": 30, "max_output_bytes": 4096, "docker_access": true})
	if _, err := c.SetAction(t.Context(), "fixture", "docker", encoded); err != nil {
		t.Fatal(err)
	}
	started, err := r.Run(t.Context(), RunRequest{Profile: "fixture", Action: "docker"})
	if err != nil {
		t.Fatal(err)
	}
	id := started["session_id"].(string)
	deadline := time.Now().Add(35 * time.Second)
	for {
		result, err := r.Read(id, nil, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if result["status"] == "exited" {
			if result["exit_code"] != 0 || result["output"] != "Docker host bind verified\n" || result["cleanup_error"] != nil {
				t.Fatalf("Docker acceptance: %#v", result)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Docker acceptance timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
	entries, err := os.ReadDir(r.layout.DockerDirectory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("proxy cleanup: %v %v", entries, err)
	}
}
