package management

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"loki/internal/progress"
)

// Setup prepares the host prerequisites. It never grants Docker group access or
// creates persistent passwordless administration privileges for the login user.
func PrepareEnvironment(ctx context.Context, input io.Reader, diagnostics io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if docker, err := exec.LookPath("docker"); err == nil {
		if exec.CommandContext(ctx, docker, "--host", "unix:///var/run/docker.sock", "info").Run() == nil {
			return checkDockerAPI(ctx, docker)
		}
	}
	privileged := os.Geteuid() != 0
	sudo := ""
	if privileged {
		var err error
		sudo, err = exec.LookPath("sudo")
		if err != nil {
			return fmt.Errorf("host preparation needs administrator access; run loki setup as root or provide sudo")
		}
		fmt.Fprintln(diagnostics, "Preparing the Linux execution host. Administrator authentication may be required.")
		command := exec.CommandContext(ctx, sudo, "-v")
		command.Stdin, command.Stdout, command.Stderr = input, diagnostics, diagnostics
		if err := command.Run(); err != nil {
			return fmt.Errorf("administrator authorization failed; retry loki setup: %w", err)
		}
	}
	run := func(label, binary string, args ...string) error {
		if privileged {
			args = append([]string{"-n", binary}, args...)
			binary = sudo
		}
		fmt.Fprintln(diagnostics, label+"...")
		stop := progress.StartHeartbeat(ctx, progress.NewLineReporter(diagnostics), progress.HeartbeatOptions{Operation: "setup", Phase: "environment", Message: "Still preparing the Linux execution host"})
		defer stop()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		command.Stdin, command.Stdout, command.Stderr = input, diagnostics, diagnostics
		if err := command.Run(); err != nil {
			return fmt.Errorf("%s failed; retry loki setup: %w", label, err)
		}
		return nil
	}
	if _, err := exec.LookPath("docker"); err != nil {
		release, err := os.ReadFile("/etc/os-release")
		values := map[string]string{}
		for _, line := range strings.Split(string(release), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				values[key] = strings.Trim(value, "\"'")
			}
		}
		if err != nil || (values["ID"] != "ubuntu" && values["ID"] != "debian") || !regexp.MustCompile(`^[a-z]+$`).MatchString(values["VERSION_CODENAME"]) {
			return fmt.Errorf("automatic Docker preparation supports Ubuntu and Debian; select a prepared Linux execution host for this distribution")
		}
		apt, err := exec.LookPath("apt-get")
		if err != nil {
			return err
		}
		if err := run("Refreshing trusted distribution packages", apt, "update"); err != nil {
			return err
		}
		if err := run("Preparing the official Docker repository", apt, "install", "--yes", "ca-certificates"); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://download.docker.com/linux/"+values["ID"]+"/gpg", nil)
		if err != nil {
			return err
		}
		response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
		if err != nil {
			return err
		}
		key, err := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || fmt.Sprintf("%x", sha256.Sum256(key)) != "1500c1f56fa9e26b9b8f42452a553675796ade0807cdce11975eb98170b3a570" {
			return fmt.Errorf("official Docker signing key failed its trusted digest; host preparation stopped")
		}
		if err := run("Preparing Docker key storage", "install", "-d", "-m", "0755", "/etc/apt/keyrings"); err != nil {
			return err
		}
		stage, err := os.MkdirTemp("", "loki-docker-repository-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		files := map[string][]byte{
			"/etc/apt/keyrings/loki-docker.asc":           key,
			"/etc/apt/sources.list.d/loki-docker.sources": []byte(fmt.Sprintf("# Managed by Loki host preparation.\nTypes: deb\nURIs: https://download.docker.com/linux/%s\nSuites: %s\nComponents: stable\nArchitectures: %s\nSigned-By: /etc/apt/keyrings/loki-docker.asc\n", values["ID"], values["VERSION_CODENAME"], runtime.GOARCH)),
		}
		for target, data := range files {
			if err := realDirectories(filepath.Dir(target)); err != nil {
				return err
			}
			if info, err := os.Lstat(target); err == nil {
				existing, readErr := os.ReadFile(target)
				if !info.Mode().IsRegular() || readErr != nil || string(existing) != string(data) {
					return fmt.Errorf("Docker preparation found a different existing repository file; preserving %s", target)
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			source := filepath.Join(stage, filepath.Base(target))
			if err := os.WriteFile(source, data, 0600); err != nil {
				return err
			}
			if err := run("Installing the verified Docker repository", "install", "-m", "0644", source, target); err != nil {
				return err
			}
		}
		if err := run("Refreshing the official Docker packages", apt, "update"); err != nil {
			return err
		}
		if err := run("Installing the latest stable Docker engine", apt, "install", "--yes", "docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"); err != nil {
			return err
		}
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		if err := run("Starting the Docker service", "systemctl", "enable", "--now", "docker.service"); err != nil {
			return err
		}
	} else if err := run("Starting the Docker service", "service", "docker", "start"); err != nil {
		return err
	}
	if err := run("Checking Docker readiness", "docker", "--host", "unix:///var/run/docker.sock", "info", "--format", "{{.ServerVersion}}"); err != nil {
		return err
	}
	return checkDockerAPI(ctx, "docker")
}

func checkDockerAPI(ctx context.Context, binary string) error {
	command := exec.CommandContext(ctx, binary, "--host", "unix:///var/run/docker.sock", "version", "--format", "{{.Server.APIVersion}}")
	var output boundedDockerOutput
	output.limit = 128
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return fmt.Errorf("Docker API readiness failed: %w", err)
	}
	var major, minor int
	if output.overflow {
		return fmt.Errorf("invalid Docker API version")
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(output.String()), "%d.%d", &major, &minor); err != nil || major != 1 || minor < 47 {
		return fmt.Errorf("selected Docker Engine needs API 1.47 or newer; update its existing installation before retrying loki setup")
	}
	return nil
}
