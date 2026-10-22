package compose

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"loki/internal/host/assets"
	"loki/internal/platform/safeio"
)

func safelyUnprepared(path, leaf string) string {
	if path != "" {
		return path
	}
	return "/dev/shm/loki-app-unprepared/" + leaf
}

func applicationBind(source, target string, readOnly bool) string {
	fields := []string{"type=bind", "source=" + source, "target=" + target}
	if readOnly {
		fields = append(fields, "readonly")
	}
	var text strings.Builder
	writer := csv.NewWriter(&text)
	_ = writer.Write(fields)
	writer.Flush()
	return strings.TrimSuffix(text.String(), "\n")
}

// prepareApplicationValues creates only owned deployment metadata and an empty
// app generation. It never initializes, unlocks or imports any vault. Root
// preparation runs in a networkless capability-limited container; the broker
// receives only the resulting active generation and its finite role sockets.
func (b *Backend) prepareApplicationValues(ctx context.Context, state *runtimeState) error {
	b.report("application-prepare", "Preparing isolated application service metadata without initializing or unlocking values...")
	ownerHash := sha256.Sum256([]byte(filepath.Clean(b.root)))
	owner := "loki-tools-" + hex.EncodeToString(ownerHash[:8])
	runtimeRoot, err := applicationRuntimeDirectory(b.root, filepath.Join("/dev/shm", owner+"-compose-secrets"))
	if err != nil {
		return err
	}
	projection := filepath.Join(b.runtimeRoot, "application-public")
	if err := os.MkdirAll(projection, 0700); err != nil {
		return err
	}
	var catalog []byte
	catalogPath := filepath.Join(b.root, "application-resources.json")
	info, err := os.Lstat(catalogPath)
	if errors.Is(err, os.ErrNotExist) {
		catalog = []byte(`{"resources":[]}`)
	} else if err != nil {
		return err
	} else {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 1<<20 {
			return errors.New("host application catalog is invalid")
		}
		catalog, err = os.ReadFile(catalogPath)
		if err != nil || len(catalog) > 1<<20 || !json.Valid(catalog) {
			return errors.New("host application catalog is invalid")
		}
	}
	input := filepath.Join(projection, "application-resources.json")
	if err := safeio.PublishPrivate(input, catalog, true); err != nil {
		return err
	}
	materialized, err := assets.Materialize(filepath.Join(b.runtimeRoot, "assets"))
	if err != nil {
		return err
	}
	ingress, err := b.materializeIngressConfig(state.IngressHosts)
	if err != nil {
		return err
	}
	env, err := b.composeEnvironment(ctx, *state, materialized.GitHubConfig, ingress)
	if err != nil {
		return err
	}
	github := materialized.GitHubConfig
	for _, value := range env {
		if strings.HasPrefix(value, "LOKI_GITHUB_CONFIG_FILE=") {
			github = strings.TrimPrefix(value, "LOKI_GITHUB_CONFIG_FILE=")
		}
	}
	volume := b.volumeName("application-state")
	if _, err := b.runner.Run(ctx, nil, "volume", "create", "--label", "io.loki.application.owner="+owner, volume); err != nil {
		return err
	}
	raw, err := b.runner.Run(ctx, nil, "volume", "inspect", volume)
	if err != nil {
		return err
	}
	var volumes []struct {
		Name, Driver, Mountpoint string
		Labels                   map[string]string
		Options                  map[string]string
	}
	if json.Unmarshal(raw, &volumes) != nil || len(volumes) != 1 || volumes[0].Name != volume || volumes[0].Driver != "local" || len(volumes[0].Options) != 0 || volumes[0].Labels["io.loki.application.owner"] != owner || !filepath.IsAbs(volumes[0].Mountpoint) || volumes[0].Mountpoint == "/" || filepath.Clean(volumes[0].Mountpoint) != volumes[0].Mountpoint || strings.ContainsAny(volumes[0].Mountpoint, "\x00\r\n") {
		return errors.New("application data volume is foreign or incompatible; preserving it")
	}
	// Public input copies become root-owned inside the tmpfs role projection.
	// The original files stay in the private host operator directory.
	script := `set -eu
umask 077
test ! -L /run/loki/secrets
if test "$(stat -c %u /run/loki/secrets)" != 0; then
  test -z "$(ls -A /run/loki/secrets)"
fi
install -d -o 0 -g 0 -m 0755 /run/loki/secrets
install -d -o 0 -g 10001 -m 0750 /run/loki/secrets/roles
install -m 0600 /input/application-resources.json /run/preparation/application-resources.json
exec /opt/loki/bin/loki host prepare-secrets --uid 10006 --gid 10006 --agent-uid 10000 --metadata-gid 10001 --identity /var/lib/loki/app-store/deployment.json --state /var/lib/loki/app-store --service-state /var/lib/loki/secrets --runtime /run/loki/secrets --config /etc/loki/loki.toml --github-config /input/github.toml --catalog /run/preparation/application-resources.json --service-catalog /etc/loki/application-resources.json --execution-contract /usr/share/doc/loki/container-execution-contract.json --unit-directory= --overlay-directory=/run/loki/secrets/roles --role-source=/etc/loki --owner ` + owner
	if b.maintenance != nil {
		script += " --maintenance-operation " + b.maintenance.OperationID
		if b.maintenance.Interrupt {
			script += " --interrupt-jobs"
		}
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "134217728", "--pids-limit", "32", "--entrypoint", "/bin/sh", "--mount", "type=volume,source=" + volume + ",target=/var/lib/loki/app-store,volume-nocopy", "--mount", applicationBind(runtimeRoot, "/run/loki/secrets", false), "--mount", applicationBind(input, "/input/application-resources.json", true), "--mount", applicationBind(github, "/input/github.toml", true), state.CoreImage, "-ec", script}
	args = append(args[:1], append([]string{"--tmpfs", "/run/preparation:rw,nosuid,nodev,size=2097152,mode=0700"}, args[1:]...)...)
	if _, err := b.runner.Run(ctx, nil, "image", "inspect", state.CoreImage); err != nil {
		if _, err := b.runner.Run(ctx, nil, "pull", state.CoreImage); err != nil {
			return err
		}
	}
	if private, ok := b.runner.(PrivateRunner); ok {
		raw, err = private.RunPrivate(ctx, nil, nil, args...)
	} else {
		raw, err = b.runner.Run(ctx, nil, args...)
	}
	if err != nil {
		return errors.New("application service metadata preparation failed")
	}
	var prepared struct {
		Prepared   bool   `json:"prepared"`
		UID        uint32 `json:"broker_uid"`
		GID        uint32 `json:"broker_gid"`
		Policy     string `json:"policy"`
		Deployment string `json:"deployment_id"`
		Runner     string `json:"runner_id"`
		Generation string `json:"active_generation"`
	}
	if json.Unmarshal(raw, &prepared) != nil || !prepared.Prepared || prepared.UID != 10006 || prepared.GID != 10006 || !regexp.MustCompile(`^deployment-[0-9a-f]{32}$`).MatchString(prepared.Deployment) || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(prepared.Generation) || !validSHA256(prepared.Policy) || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(prepared.Runner) {
		return fmt.Errorf("application generation preparation was not confirmed")
	}
	state.ApplicationStateSource = filepath.Join(volumes[0].Mountpoint, "generations", prepared.Generation)
	state.ApplicationRuntimeRoot = runtimeRoot
	return nil
}
