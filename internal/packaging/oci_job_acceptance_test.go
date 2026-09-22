package packaging

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestOCIJobAcceptanceRunnerBootstrapsFixtures(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("runner is Linux-only")
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "verify", "accept-oci-jobs.sh")
	dockerfile := filepath.Join(root, "internal", "platform", "sandbox", "testdata", "oci-image", "Dockerfile")
	for _, path := range []string{script, dockerfile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}

	bin := t.TempDir()
	dockerLog := filepath.Join(t.TempDir(), "docker.log")
	goLog := filepath.Join(t.TempDir(), "go.log")
	writeExecutable(t, filepath.Join(bin, "docker"), fakeOCIJobDocker)
	writeExecutable(t, filepath.Join(bin, "go"), fakeOCIJobGo)

	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	tmp := t.TempDir()
	command := exec.Command("sh", script)
	command.Dir = root
	command.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"TMPDIR="+tmp,
		"LOKI_TEST_DOCKER_SOCKET="+socket,
		"LOKI_FAKE_DOCKER_LOG="+dockerLog,
		"LOKI_FAKE_GO_LOG="+goLog,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("runner failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "loki OCI Job acceptance: passed") {
		t.Fatalf("runner output = %q", output)
	}

	dockerCalls := mustRead(t, dockerLog)
	for _, required := range []string{
		"info",
		"buildx version",
		"run --detach --name loki-oci-job-registry-",
		"--publish 127.0.0.1::5000 registry:3.1.1@sha256:fd374bae807c225661adfe2c0c1f9970a0b8fab1761fd7dfb91e0fd9a8748f9b",
		"port registry-container-id 5000/tcp",
		"buildx build --load --file ",
		"internal/platform/sandbox/testdata/oci-image/Dockerfile",
		"--tag 127.0.0.1:49153/loki-oci-job-fixture:",
		"push 127.0.0.1:49153/loki-oci-job-fixture:",
		"rm -f registry-container-id",
	} {
		if !strings.Contains(dockerCalls, required) {
			t.Errorf("docker calls missing %q:\n%s", required, dockerCalls)
		}
	}

	goCall := mustRead(t, goLog)
	for _, required := range []string{
		"args=test ./internal/platform/sandbox -run ^TestRealOCIJob -v -count=1",
		"LOKI_REQUIRE_OCI_JOB_TESTS=1",
		"LOKI_TEST_DOCKER_SOCKET=" + socket,
		"LOKI_TEST_DOCKER_PEER_UID=" + strconv.Itoa(os.Getuid()),
		"LOKI_TEST_DOCKER_IMAGE=127.0.0.1:49153/loki-oci-job-fixture@sha256:" + strings.Repeat("a", 64),
		"LOKI_TEST_EGRESS_ALLOWED_AUTHORITY=registry.npmjs.org:443",
	} {
		if !strings.Contains(goCall, required) {
			t.Errorf("go invocation missing %q:\n%s", required, goCall)
		}
	}
	workspaceLine := findLine(goCall, "LOKI_TEST_DOCKER_WORKSPACE=")
	if workspaceLine == "" {
		t.Fatalf("go invocation did not receive a workspace:\n%s", goCall)
	}
	workspace := strings.TrimPrefix(workspaceLine, "LOKI_TEST_DOCKER_WORKSPACE=")
	if !strings.HasPrefix(workspace, tmp+string(os.PathSeparator)) {
		t.Fatalf("workspace = %q, want child of %q", workspace, tmp)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("temporary workspace was not cleaned: %v", err)
	}
}

func TestOCIJobFixtureImageContainsGatewayAssets(t *testing.T) {
	root := filepath.Join("..", "..")
	body := mustRead(t, filepath.Join(root, "internal", "platform", "sandbox", "testdata", "oci-image", "Dockerfile"))
	for _, required := range []string{
		"docker/dockerfile:1.27.0@sha256:bde3983e9c939224420ddaf6b784cc30e09b035a4dea01f581230c50809f372e",
		"FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm@sha256:",
		"ARG TARGETOS",
		"ARG TARGETARCH",
		"--mount=type=cache,target=/go/pkg/mod",
		"GOOS=\"$TARGETOS\" GOARCH=\"$TARGETARCH\"",
		"go build -trimpath",
		"-o /out/loki ./cmd/loki",
		"FROM alpine:3.24.2@sha256:",
		"COPY --from=build /out/loki /opt/loki/bin/loki",
		"COPY packaging/native/execution-contract.json packaging/native/egress-policy.json /usr/share/doc/loki/",
		"CMD [\"/bin/sh\"]",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("OCI fixture Dockerfile missing %q", required)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func findLine(body, prefix string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

const fakeOCIJobDocker = `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$LOKI_FAKE_DOCKER_LOG"
test "$1" = "--host"
shift 2
case "$1" in
  info)
    exit 0
    ;;
  run)
    printf 'registry-container-id\n'
    ;;
  port)
    printf '127.0.0.1:49153\n'
    ;;
  buildx)
    case "$2" in
      version|build) exit 0 ;;
      *) exit 2 ;;
    esac
    ;;
  push|rm)
    exit 0
    ;;
  image)
    case "$2" in
      inspect)
        case " $* " in
          *" --format "*)
            printf '127.0.0.1:49153/loki-oci-job-fixture@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
            ;;
          *)
            exit 1
            ;;
        esac
        ;;
      rm)
        exit 0
        ;;
      *)
        exit 2
        ;;
    esac
    ;;
  *)
    exit 2
    ;;
esac
`

const fakeOCIJobGo = `#!/bin/sh
set -eu
{
  printf 'args='
  printf '%s ' "$@"
  printf '\n'
  printf 'LOKI_REQUIRE_OCI_JOB_TESTS=%s\n' "$LOKI_REQUIRE_OCI_JOB_TESTS"
  printf 'LOKI_TEST_DOCKER_SOCKET=%s\n' "$LOKI_TEST_DOCKER_SOCKET"
  printf 'LOKI_TEST_DOCKER_PEER_UID=%s\n' "$LOKI_TEST_DOCKER_PEER_UID"
  printf 'LOKI_TEST_DOCKER_IMAGE=%s\n' "$LOKI_TEST_DOCKER_IMAGE"
  printf 'LOKI_TEST_DOCKER_WORKSPACE=%s\n' "$LOKI_TEST_DOCKER_WORKSPACE"
  printf 'LOKI_TEST_EGRESS_ALLOWED_AUTHORITY=%s\n' "$LOKI_TEST_EGRESS_ALLOWED_AUTHORITY"
} >"$LOKI_FAKE_GO_LOG"
`
